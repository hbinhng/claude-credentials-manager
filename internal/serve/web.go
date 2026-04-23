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

// NewHandler returns the fully wired http.Handler.
func NewHandler(cfg ServerConfig) (http.Handler, error) {
	if cfg.Manager == nil {
		return nil, errors.New("ServerConfig.Manager is required")
	}
	tpls, err := parseTemplatesFunc()
	if err != nil {
		return nil, err
	}
	h := &handler{cfg: cfg, pages: tpls}

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

	// SPA shell — the catch-all. Any unmatched GET returns the app
	// page so hard reloads of a client-only route still boot the
	// SPA; the SPA itself is a single screen so there are no client
	// routes yet, but the shape stays if/when they appear.
	mux.HandleFunc("GET /", h.protected(h.appShell))

	return mux, nil
}

type handler struct {
	cfg   ServerConfig
	pages *pages
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
