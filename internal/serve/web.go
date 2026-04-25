package serve

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// loginBeginFn and loginCompleteFn are the package-level seams the
// /api/login/{start,finish} handlers go through. Tests swap them to
// avoid real OAuth round-trips. Production points at credflow.
var (
	loginBeginFn    = credflow.BeginLogin
	loginCompleteFn = credflow.CompleteLogin
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// APIVersion is the stable JSON schema version returned by every /api/*
// endpoint. Consumers (the built-in SPA and any external scripts) can
// branch on it; do not rename or remove fields under an existing
// version — bump this and ship a new shape instead.
const APIVersion = 1

// ServerConfig configures NewHandler.
type ServerConfig struct {
	Manager  *Manager
	Token    string // admin token; ignored when Loopback is true
	Loopback bool   // when true, auth middleware bypasses
}

// pages bundles one fully-parsed template per page so that each
// rendered HTML view can define its own "body" without colliding.
// The SPA shell (app.html) is one page; the login form is the other.
// All user-facing data flows through the JSON API — these templates
// only deliver the static shell that boots the SPA.
type pages struct {
	app   *template.Template
	login *template.Template
}

// parseTemplatesFrom parses page templates from fsys.
// Errors from the embedded FS are unreachable in production (the FS
// is baked in at compile time); the parameter makes each error
// branch reachable in tests.
func parseTemplatesFrom(fsys fs.FS) (*pages, error) {
	load := func(body string) (*template.Template, error) {
		return template.ParseFS(fsys, "templates/layout.html", "templates/"+body)
	}
	login, err := load("login.html")
	if err != nil {
		return nil, err
	}
	app, err := load("app.html")
	if err != nil {
		return nil, err
	}
	return &pages{app: app, login: login}, nil
}

// parseTemplatesFunc is called by NewHandler; tests replace it to
// inject parse errors.
var parseTemplatesFunc = func() (*pages, error) { return parseTemplatesFrom(templatesFS) }

// storeListFn is called by the /api/credentials handler; tests
// replace it to inject errors. store.List uses filepath.Glob whose
// only error is ErrBadPattern — the pattern is a fixed string and
// never malformed in production. The indirection makes the error
// branch reachable in tests without relying on OS-level fault
// injection.
var storeListFn = store.List

// storeLoadFn is called by the single-credential handler; tests
// replace it to inject errors without creating real store files.
var storeLoadFn = store.Load

func staticSub() fs.FS {
	sub, _ := fs.Sub(staticFS, "static")
	return sub
}

// NewHandler returns the fully wired http.Handler. The returned value
// also satisfies io.Closer; callers should type-assert and Close it
// during shutdown so the in-memory login-handshake sweeper goroutine
// exits cleanly.
func NewHandler(cfg ServerConfig) (http.Handler, error) {
	if cfg.Manager == nil {
		return nil, errors.New("ServerConfig.Manager is required")
	}
	tpls, err := parseTemplatesFunc()
	if err != nil {
		return nil, err
	}
	h := &handler{cfg: cfg, pages: tpls, handshakes: newHandshakeStore()}

	mux := http.NewServeMux()
	// Public endpoints: health probe + login form.
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /login", h.loginGET)
	mux.HandleFunc("POST /login", h.loginPOST)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub()))))

	// Authenticated JSON API.
	mux.HandleFunc("GET /api/credentials", h.protected(h.apiListCredentials))
	mux.HandleFunc("GET /api/credentials/{id}", h.protected(h.apiGetCredential))
	mux.HandleFunc("POST /api/credentials/{id}", h.protected(h.apiStartSession))
	mux.HandleFunc("DELETE /api/credentials/{id}", h.protected(h.apiStopSession))
	mux.HandleFunc("POST /api/credentials/{id}/refresh", h.protected(h.apiRefreshCredential))

	// Authenticated OAuth-add-credential flow. Distinct from /login
	// (the dashboard token form) — these endpoints drive the in-app
	// "+" button that runs the same PKCE flow `ccm login` does on
	// the CLI.
	mux.HandleFunc("POST /api/login/start", h.protected(h.apiLoginStart))
	mux.HandleFunc("POST /api/login/finish", h.protected(h.apiLoginFinish))

	// SPA shell — the catch-all. Any unmatched GET returns the app
	// page so hard reloads of a client-only route still boot the
	// SPA; the SPA itself is a single screen so there are no client
	// routes yet, but the shape stays if/when they appear.
	mux.HandleFunc("GET /", h.protected(h.appShell))

	h.mux = mux
	return h, nil
}

type handler struct {
	cfg        ServerConfig
	pages      *pages
	handshakes *handshakeStore
	mux        *http.ServeMux
}

// ServeHTTP delegates to the routing mux assembled in NewHandler.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// Close stops the in-memory handshake-sweeper goroutine. Safe to call
// twice. cmd/serve.go invokes it after http.Server.Shutdown.
func (h *handler) Close() error {
	h.handshakes.Close()
	return nil
}

// templateData is the minimal context passed to the layout + body
// templates. The SPA pulls all live data through the JSON API, so
// there is no credential / session state to thread through here.
type templateData struct {
	Title string
}

// protected wraps next with the auth middleware.
func (h *handler) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.Loopback {
			next(w, r)
			return
		}
		tok := requestToken(r)
		if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(h.cfg.Token)) == 1 {
			if r.URL.Query().Get("token") != "" {
				setTokenCookie(w, tok)
			}
			next(w, r)
			return
		}
		// JSON clients get 401 with a JSON body; browsers navigating to
		// a protected page get a 302 redirect to the login form.
		if wantsJSON(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "unauthorized",
			})
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

func wantsJSON(r *http.Request) bool {
	// Anything starting with /api is treated as a JSON client regardless
	// of Accept header. This avoids browsers that navigate directly to
	// /api/... (e.g. from the address bar) rendering a redirect chain.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
}

func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("ccm_serve_token"); err == nil {
		return c.Value
	}
	if v := r.URL.Query().Get("token"); v != "" {
		return v
	}
	return ""
}

func setTokenCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "ccm_serve_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (h *handler) loginGET(w http.ResponseWriter, _ *http.Request) {
	_ = h.pages.login.ExecuteTemplate(w, "layout", templateData{Title: "Sign in"})
}

func (h *handler) loginPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	tok := r.PostForm.Get("token")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(h.cfg.Token)) == 1 {
		setTokenCookie(w, tok)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// appShell serves the SPA shell HTML. All dynamic data is fetched
// via /api/credentials on boot; the shell is cacheable and contains
// nothing user-specific.
func (h *handler) appShell(w http.ResponseWriter, _ *http.Request) {
	_ = h.pages.app.ExecuteTemplate(w, "layout", templateData{Title: "Sessions"})
}

// ---- JSON API ----------------------------------------------------

// APIListResponse is the body of GET /api/credentials.
type APIListResponse struct {
	Version     int              `json:"version"`
	Credentials []APICredential  `json:"credentials"`
}

// APICredential is one entry in the list response. It intentionally
// omits quota — quota is fetched on demand via GET /api/credentials/{id}
// so the list poll stays cheap.
type APICredential struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Tier       string      `json:"tier"`        // empty string when missing
	CredStatus string      `json:"credStatus"`  // "valid" | "expiring_soon" | "expired"
	Session    *APISession `json:"session"`     // null when idle
}

// APISession is the projection of a live share.Session returned to
// the SPA. Ticket + reach are included in the list response so the
// View Ticket dialog can render without a second round-trip.
type APISession struct {
	Mode      string `json:"mode"`      // "tunnel" | "lan"
	Reach     string `json:"reach"`     // tunnel URL or http://host:port
	Ticket    string `json:"ticket"`    // base64 ticket envelope
	StartedAt string `json:"startedAt"` // RFC3339
}

// APICredentialDetail is the body of GET /api/credentials/{id}. It
// is APICredential plus a quota block fetched live when the caller
// opens the View Usage dialog.
type APICredentialDetail struct {
	Version    int         `json:"version"`
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Tier       string      `json:"tier"`
	CredStatus string      `json:"credStatus"`
	Session    *APISession `json:"session"`
	Quota      APIQuota    `json:"quota"`
}

// APIQuota carries the fetch outcome. When Fetched is false, the
// credential was expired; when Fetched is true and Error is set,
// the fetch hit an upstream error; otherwise Windows contains the
// fresh per-bucket usage.
type APIQuota struct {
	Fetched bool             `json:"fetched"`
	Error   string           `json:"error,omitempty"`
	Windows []APIQuotaWindow `json:"windows,omitempty"`
}

// APIQuotaWindow is one usage bucket returned by the upstream.
type APIQuotaWindow struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used"`     // 0-100 percent
	ResetsAt string  `json:"resetsAt"` // raw RFC3339 from upstream
	ResetsIn string  `json:"resetsIn"` // preformatted relative string ("in 1h12m")
}

// APIStartSessionRequest is the POST body for /api/credentials/{id}.
type APIStartSessionRequest struct {
	Mode     string `json:"mode"`     // "tunnel" (default) | "lan"
	BindHost string `json:"bindHost"` // required when mode == "lan"
	BindPort int    `json:"bindPort"` // optional
}

func (h *handler) apiListCredentials(w http.ResponseWriter, _ *http.Request) {
	creds, err := storeListFn()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	out := make([]APICredential, 0, len(creds))
	for _, c := range creds {
		out = append(out, toAPICredential(c, h.cfg.Manager))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, APIListResponse{Version: APIVersion, Credentials: out})
}

func (h *handler) apiGetCredential(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("id")
	cred, err := storeLoadFn(credID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown credential"})
		return
	}
	sess, _ := h.cfg.Manager.Get(credID)
	detail := APICredentialDetail{
		Version:    APIVersion,
		ID:         cred.ID,
		Name:       cred.Name,
		Tier:       cred.Subscription.Tier,
		CredStatus: strings.ReplaceAll(cred.Status(), " ", "_"),
		Session:    toAPISession(sess),
		Quota:      fetchQuota(cred),
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) apiStartSession(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("id")
	cred, err := storeLoadFn(credID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown credential"})
		return
	}
	// Tolerate an empty body: an empty POST is equivalent to a
	// tunnel-mode request with no bind options. Anything non-empty
	// that fails to decode is a client error.
	var body APIStartSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	opts := share.Options{BindHost: body.BindHost, BindPort: body.BindPort}
	if body.Mode == "lan" {
		if body.BindHost == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bindHost is required in lan mode"})
			return
		}
	} else {
		// Tunnel mode — the starter treats empty BindHost as tunnel,
		// so clear whatever the client happened to send.
		opts.BindHost = ""
		opts.BindPort = 0
	}
	sess, err := h.cfg.Manager.Start(cred, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrAlreadyStarted) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, APICredentialDetail{
		Version:    APIVersion,
		ID:         cred.ID,
		Name:       cred.Name,
		Tier:       cred.Subscription.Tier,
		CredStatus: strings.ReplaceAll(cred.Status(), " ", "_"),
		Session:    toAPISession(sess),
		Quota:      APIQuota{Fetched: false},
	})
}

func (h *handler) apiStopSession(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("id")
	if err := h.cfg.Manager.Stop(credID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiRefreshCredential runs an OAuth refresh round-trip for the
// credential at :id. If the credential is currently live as a share
// session, the session's own credState picks up the new tokens on
// its next request (the store file was rewritten). The handler
// returns the updated credential detail so the SPA can re-render
// the row without a second GET.
func (h *handler) apiRefreshCredential(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("id")
	if _, err := storeLoadFn(credID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown credential"})
		return
	}
	cred, err := refreshCredentialFn(credID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sess, _ := h.cfg.Manager.Get(credID)
	writeJSON(w, http.StatusOK, APICredentialDetail{
		Version:    APIVersion,
		ID:         cred.ID,
		Name:       cred.Name,
		Tier:       cred.Subscription.Tier,
		CredStatus: strings.ReplaceAll(cred.Status(), " ", "_"),
		Session:    toAPISession(sess),
		Quota:      APIQuota{Fetched: false},
	})
}

// refreshCredentialFn is the seam web tests override so HTTP
// handler tests don't reach for real OAuth endpoints.
var refreshCredentialFn = credflow.RefreshCredential

// APILoginStartResponse is the body of POST /api/login/start. The
// SPA opens AuthorizeURL in a new tab and stashes HandshakeID for
// the subsequent /api/login/finish call.
type APILoginStartResponse struct {
	Version      int    `json:"version"`
	HandshakeID  string `json:"handshakeId"`
	AuthorizeURL string `json:"authorizeUrl"`
}

// APILoginFinishRequest is the body of POST /api/login/finish.
type APILoginFinishRequest struct {
	HandshakeID string `json:"handshakeId"`
	Code        string `json:"code"`
}

// APILoginFinishResponse is the body of POST /api/login/finish on
// success. It carries the freshly saved credential in the same shape
// the list endpoint returns so the SPA can render the row without a
// follow-up GET.
type APILoginFinishResponse struct {
	Version    int           `json:"version"`
	Credential APICredential `json:"credential"`
}

// apiLoginStart kicks off an OAuth login handshake. The PKCE pair is
// generated server-side and stashed in the in-memory handshake store;
// the response gives the SPA both the URL the user must visit and the
// opaque ID it'll send back to /finish.
func (h *handler) apiLoginStart(w http.ResponseWriter, _ *http.Request) {
	hs, err := loginBeginFn()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "begin login: " + err.Error()})
		return
	}
	h.handshakes.Put(hs)
	writeJSON(w, http.StatusOK, APILoginStartResponse{
		Version:      APIVersion,
		HandshakeID:  hs.ID,
		AuthorizeURL: hs.AuthorizeURL,
	})
}

// apiLoginFinish exchanges the paste-code for an OAuth token pair
// and persists the credential. The handshake is consumed on success
// (and on save-failure, since the upstream code is single-use). On a
// pure exchange failure the handshake is retained so the user can
// fix a typo and retry without restarting.
func (h *handler) apiLoginFinish(w http.ResponseWriter, r *http.Request) {
	var body APILoginFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}
	hs, ok := h.handshakes.Peek(body.HandshakeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "login session expired — start over"})
		return
	}
	cred, err := loginCompleteFn(hs, code)
	if err != nil {
		// "save credentials" wraps the post-exchange persist failure;
		// once the OAuth code has been spent upstream the handshake
		// is useless, so drop it and force the user to begin again.
		// Anything else (network blip, exchange rejection) leaves the
		// PKCE pair valid for a typo retry.
		if strings.Contains(err.Error(), "save credentials") {
			h.handshakes.Delete(body.HandshakeID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	h.handshakes.Delete(body.HandshakeID)
	writeJSON(w, http.StatusCreated, APILoginFinishResponse{
		Version:    APIVersion,
		Credential: toAPICredential(cred, h.cfg.Manager),
	})
}

// toAPICredential projects a store.Credential plus manager state
// into the list-response shape.
func toAPICredential(c *store.Credential, mgr *Manager) APICredential {
	out := APICredential{
		ID:         c.ID,
		Name:       c.Name,
		Tier:       c.Subscription.Tier,
		CredStatus: strings.ReplaceAll(c.Status(), " ", "_"),
	}
	if sess, ok := mgr.Get(c.ID); ok {
		out.Session = toAPISession(sess)
	}
	return out
}

// toAPISession returns nil when sess is nil so the JSON field
// renders as `null`, which the SPA branches on.
func toAPISession(sess share.Session) *APISession {
	if sess == nil {
		return nil
	}
	return &APISession{
		Mode:      sess.Mode(),
		Reach:     sess.Reach(),
		Ticket:    sess.Ticket(),
		StartedAt: sess.StartedAt().UTC().Format(rfc3339),
	}
}

// rfc3339 is spelled out so a future switch to a different timestamp
// format (e.g. including nanoseconds) only has to change one place.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

// fetchQuota runs the single-credential equivalent of
// fetchUsagesParallel: one synchronous FetchUsageFn call when the
// credential is valid, or a "not fetched" stub when it's expired.
func fetchQuota(c *store.Credential) APIQuota {
	if c.IsExpired() {
		return APIQuota{Fetched: false}
	}
	u := oauth.FetchUsageFn(c.ClaudeAiOauth.AccessToken)
	out := APIQuota{Fetched: true}
	if u == nil || u.Error != "" {
		if u != nil {
			out.Error = u.Error
		}
		return out
	}
	out.Windows = make([]APIQuotaWindow, 0, len(u.Quotas))
	for _, q := range u.Quotas {
		out.Windows = append(out.Windows, APIQuotaWindow{
			Name:     q.Name,
			Used:     q.Used,
			ResetsAt: q.ResetsAt,
			ResetsIn: oauth.FormatResetTime(q.ResetsAt),
		})
	}
	return out
}

// writeJSON encodes body as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
