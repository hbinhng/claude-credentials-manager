package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// ---- helpers -----------------------------------------------------

// newTestServer wires a NewHandler-backed httptest.Server with cleanup.
func newTestServer(t *testing.T, cfg ServerConfig) *httptest.Server {
	t.Helper()
	h, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// redirectBlocker refuses to follow redirects so tests can assert on
// the 30x status directly.
func redirectBlocker() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// stubStore replaces storeListFn + storeLoadFn with table-driven
// fakes for the duration of a test. The returned restore() reverts
// both globals. Use defer restore() immediately.
func stubStore(t *testing.T, creds []*store.Credential, listErr error) func() {
	t.Helper()
	byID := make(map[string]*store.Credential, len(creds))
	for _, c := range creds {
		byID[c.ID] = c
	}
	origList, origLoad := storeListFn, storeLoadFn
	storeListFn = func() ([]*store.Credential, error) {
		if listErr != nil {
			return nil, listErr
		}
		return creds, nil
	}
	storeLoadFn = func(id string) (*store.Credential, error) {
		if c, ok := byID[id]; ok {
			return c, nil
		}
		return nil, fmt.Errorf("unknown credential: %s", id)
	}
	return func() {
		storeListFn = origList
		storeLoadFn = origLoad
	}
}

// stubUsage replaces oauth.FetchUsageFn for the duration of a test.
func stubUsage(t *testing.T, fn func(string) *oauth.UsageInfo) func() {
	t.Helper()
	orig := oauth.FetchUsageFn
	oauth.FetchUsageFn = fn
	return func() { oauth.FetchUsageFn = orig }
}

// fakeCred builds a minimal store.Credential that is not expired.
func fakeCredModel(id, name, tier string) *store.Credential {
	return &store.Credential{
		ID:   id,
		Name: name,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "tok-" + id,
			RefreshToken: "ref-" + id,
			ExpiresAt:    time.Now().Add(1 * time.Hour).UnixMilli(),
		},
		Subscription: store.Subscription{Tier: tier},
	}
}

// expiredCred builds a minimal store.Credential that reports expired.
func expiredCred(id, name string) *store.Credential {
	c := fakeCredModel(id, name, "Claude Pro")
	c.ClaudeAiOauth.ExpiresAt = time.Now().Add(-1 * time.Hour).UnixMilli()
	return c
}

// ---- auth matrix -------------------------------------------------

func TestNewHandler_RequiresManager(t *testing.T) {
	if _, err := NewHandler(ServerConfig{}); err == nil {
		t.Fatalf("NewHandler with zero ServerConfig succeeded; want error")
	}
}

func TestHealthzBypassesAuth(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

func TestAuth_LoopbackBypass(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: true,
	})
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

func TestAuth_BearerAccepted(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

func TestAuth_CookieAccepted(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "ccm_serve_token", Value: "secret"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

func TestAuth_QueryParamSetsCookie(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := http.Get(srv.URL + "/?token=secret")
	if err != nil {
		t.Fatalf("GET /?token=: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "ccm_serve_token=secret") {
		t.Errorf("Set-Cookie=%q, want ccm_serve_token cookie", setCookie)
	}
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Strict") {
		t.Errorf("cookie missing HttpOnly/SameSite=Strict: %q", setCookie)
	}
}

func TestAuth_UnauthenticatedHTMLRedirectsToLogin(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := redirectBlocker().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status=%d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("Location=%q, want /login", loc)
	}
}

func TestAuth_UnauthenticatedJSONReturns401(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := redirectBlocker().Get(srv.URL + "/api/credentials")
	if err != nil {
		t.Fatalf("GET /api/credentials: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unauthorized" {
		t.Errorf("body=%v, want {error:unauthorized}", body)
	}
}

func TestAuth_ConstantTimeRejectsPartialMatch(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "abcdefghijklmnop",
		Loopback: false,
	})
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer abcdefg")
	resp, err := redirectBlocker().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status=%d, want 302 (partial match rejected)", resp.StatusCode)
	}
}

func TestAuth_AcceptHeaderJSON(t *testing.T) {
	// A client with Accept: application/json on a non-/api/ path should
	// still get a JSON 401 rather than the HTML login redirect.
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := redirectBlocker().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

// ---- login flow --------------------------------------------------

func TestLoginGET(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Admin token required") {
		t.Errorf("body missing login heading: %s", body)
	}
}

func TestLoginPOST_Correct(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := redirectBlocker().PostForm(srv.URL+"/login", map[string][]string{"token": {"secret"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status=%d, want 303", resp.StatusCode)
	}
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "ccm_serve_token=secret") {
		t.Errorf("cookie missing: %q", setCookie)
	}
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Strict") {
		t.Errorf("cookie missing HttpOnly/SameSite=Strict: %q", setCookie)
	}
}

func TestLoginPOST_Wrong(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	resp, err := redirectBlocker().PostForm(srv.URL+"/login", map[string][]string{"token": {"wrong"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status=%d, want 302", resp.StatusCode)
	}
}

func TestLoginPOST_BadForm(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Token:    "secret",
		Loopback: false,
	})
	// ParseForm fails on malformed percent-encoding. "token=%" is a
	// truncated escape sequence which url.ParseQuery rejects.
	req, _ := http.NewRequest("POST", srv.URL+"/login", strings.NewReader("token=%"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// ---- static ------------------------------------------------------

func TestStaticCSSServed(t *testing.T) {
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Loopback: true,
	})
	resp, err := http.Get(srv.URL + "/static/style.css")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

// ---- SPA shell ---------------------------------------------------

func TestAppShell(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{
		Manager:  NewManager(&fakeStarter{}, nil),
		Loopback: true,
	})
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, `id="app"`) {
		t.Errorf("shell missing app root: %s", s)
	}
	if !strings.Contains(s, `src="/static/app.js"`) {
		t.Errorf("shell missing app.js script: %s", s)
	}
}

// ---- GET /api/credentials ----------------------------------------

func TestAPIListCredentials_EmptyStore(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	var body APIListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != APIVersion {
		t.Errorf("Version=%d, want %d", body.Version, APIVersion)
	}
	if body.Credentials == nil {
		t.Errorf("Credentials=nil, want empty slice (scripts expect .credentials to be iterable)")
	}
	if len(body.Credentials) != 0 {
		t.Errorf("Credentials len=%d, want 0", len(body.Credentials))
	}
}

func TestAPIListCredentials_Sorted(t *testing.T) {
	creds := []*store.Credential{
		fakeCredModel("id-c", "charlie", "Claude Pro"),
		fakeCredModel("id-a", "alpha", "Claude Max 20x"),
		fakeCredModel("id-b", "bravo", ""),
	}
	defer stubStore(t, creds, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})

	resp, err := http.Get(srv.URL + "/api/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body APIListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotNames := []string{}
	for _, c := range body.Credentials {
		gotNames = append(gotNames, c.Name)
	}
	want := []string{"alpha", "bravo", "charlie"}
	if fmt.Sprint(gotNames) != fmt.Sprint(want) {
		t.Errorf("order=%v, want %v", gotNames, want)
	}
	// Verify tier passthrough including empty.
	for _, c := range body.Credentials {
		switch c.Name {
		case "alpha":
			if c.Tier != "Claude Max 20x" {
				t.Errorf("alpha.Tier=%q", c.Tier)
			}
		case "bravo":
			if c.Tier != "" {
				t.Errorf("bravo.Tier=%q, want empty", c.Tier)
			}
		}
	}
}

func TestAPIListCredentials_SessionInlined(t *testing.T) {
	cred := fakeCredModel("live-1", "running", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()

	mgr := NewManager(&fakeStarter{}, nil)
	sess, _ := mgr.Start(cred, share.Options{})
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	resp, err := http.Get(srv.URL + "/api/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body APIListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Credentials) != 1 {
		t.Fatalf("got %d, want 1", len(body.Credentials))
	}
	got := body.Credentials[0].Session
	if got == nil {
		t.Fatalf("Session is nil; want populated")
	}
	if got.Mode != sess.Mode() || got.Reach != sess.Reach() || got.Ticket != sess.Ticket() {
		t.Errorf("Session mismatch: %+v vs %v/%v/%v", got, sess.Mode(), sess.Reach(), sess.Ticket())
	}
	if got.StartedAt == "" {
		t.Errorf("StartedAt empty")
	}
}

func TestAPIListCredentials_StoreError(t *testing.T) {
	defer stubStore(t, nil, errors.New("store boom"))()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", resp.StatusCode)
	}
}

// ---- GET /api/credentials/{id} -----------------------------------

func TestAPIGetCredential_WithQuota(t *testing.T) {
	cred := fakeCredModel("q-1", "has-quota", "Claude Max 20x")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	defer stubUsage(t, func(tok string) *oauth.UsageInfo {
		if tok != cred.ClaudeAiOauth.AccessToken {
			t.Errorf("FetchUsageFn got token %q, want %q", tok, cred.ClaudeAiOauth.AccessToken)
		}
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 33, ResetsAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
		}}
	})()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})

	resp, err := http.Get(srv.URL + "/api/credentials/q-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	var d APICredentialDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !d.Quota.Fetched {
		t.Errorf("Quota.Fetched=false, want true")
	}
	if len(d.Quota.Windows) != 1 || d.Quota.Windows[0].Name != "5h" || d.Quota.Windows[0].Used != 33 {
		t.Errorf("Quota.Windows=%+v", d.Quota.Windows)
	}
	if d.Quota.Windows[0].ResetsIn == "" {
		t.Errorf("ResetsIn empty; want preformatted string")
	}
}

func TestAPIGetCredential_Expired(t *testing.T) {
	cred := expiredCred("e-1", "old")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	// FetchUsageFn must never be called for expired credentials.
	defer stubUsage(t, func(string) *oauth.UsageInfo {
		t.Fatalf("FetchUsageFn called for expired credential")
		return nil
	})()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials/e-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var d APICredentialDetail
	_ = json.NewDecoder(resp.Body).Decode(&d)
	if d.Quota.Fetched {
		t.Errorf("Quota.Fetched=true for expired, want false")
	}
	if d.CredStatus != "expired" {
		t.Errorf("CredStatus=%q, want expired", d.CredStatus)
	}
}

func TestAPIGetCredential_QuotaFetchError(t *testing.T) {
	cred := fakeCredModel("err-1", "failing", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	defer stubUsage(t, func(string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Error: "HTTP 429: too many requests"}
	})()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials/err-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var d APICredentialDetail
	_ = json.NewDecoder(resp.Body).Decode(&d)
	if !d.Quota.Fetched {
		t.Errorf("Quota.Fetched=false, want true (attempt was made)")
	}
	if d.Quota.Error != "HTTP 429: too many requests" {
		t.Errorf("Quota.Error=%q", d.Quota.Error)
	}
	if len(d.Quota.Windows) != 0 {
		t.Errorf("Windows=%+v, want empty when Error set", d.Quota.Windows)
	}
}

func TestAPIGetCredential_QuotaNilResponse(t *testing.T) {
	// Paranoia: FetchUsageFn returning nil (never happens in prod,
	// but we do not want a nil dereference).
	cred := fakeCredModel("nil-1", "nil-quota", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	defer stubUsage(t, func(string) *oauth.UsageInfo { return nil })()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials/nil-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var d APICredentialDetail
	_ = json.NewDecoder(resp.Body).Decode(&d)
	if !d.Quota.Fetched {
		t.Errorf("Fetched=false, want true")
	}
	if d.Quota.Error != "" {
		t.Errorf("Error=%q, want empty on nil UsageInfo", d.Quota.Error)
	}
}

func TestAPIGetCredential_Unknown(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Get(srv.URL + "/api/credentials/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

// ---- POST /api/credentials/{id} -----------------------------------

func TestAPIStartSession_TunnelDefaultMode(t *testing.T) {
	cred := fakeCredModel("s-1", "new", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()

	mgr := NewManager(&fakeStarter{}, nil)
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	resp, err := http.Post(srv.URL+"/api/credentials/s-1", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status=%d, want 201", resp.StatusCode)
	}
	var d APICredentialDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Session == nil || d.Session.Mode != "tunnel" {
		t.Errorf("Session=%+v, want Mode=tunnel", d.Session)
	}
	if _, ok := mgr.Get("s-1"); !ok {
		t.Errorf("manager does not contain the started session")
	}
}

func TestAPIStartSession_ExplicitTunnel(t *testing.T) {
	cred := fakeCredModel("s-2", "exp-tunnel", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	body := bytes.NewBufferString(`{"mode":"tunnel"}`)
	resp, err := http.Post(srv.URL+"/api/credentials/s-2", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status=%d, want 201", resp.StatusCode)
	}
}

func TestAPIStartSession_LANHappyPath(t *testing.T) {
	cred := fakeCredModel("s-3", "lan", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	body := bytes.NewBufferString(`{"mode":"lan","bindHost":"192.168.1.5","bindPort":8787}`)
	resp, err := http.Post(srv.URL+"/api/credentials/s-3", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status=%d, want 201", resp.StatusCode)
	}
	// Verify the fake starter was called with the LAN options.
	starter := extractFakeStarter(t, mgr)
	if len(starter.received) != 1 {
		t.Fatalf("starter received %d calls, want 1", len(starter.received))
	}
	got := starter.received[0].opts
	if got.BindHost != "192.168.1.5" || got.BindPort != 8787 {
		t.Errorf("Options=%+v", got)
	}
}

func TestAPIStartSession_LANMissingHost(t *testing.T) {
	cred := fakeCredModel("s-4", "lan-nohost", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	body := bytes.NewBufferString(`{"mode":"lan"}`)
	resp, err := http.Post(srv.URL+"/api/credentials/s-4", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
	if _, ok := mgr.Get("s-4"); ok {
		t.Errorf("manager started a session despite missing bindHost")
	}
}

func TestAPIStartSession_MalformedJSON(t *testing.T) {
	cred := fakeCredModel("s-5", "bad", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})

	body := bytes.NewBufferString(`{not json`)
	resp, err := http.Post(srv.URL+"/api/credentials/s-5", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPIStartSession_UnknownCredential(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Post(srv.URL+"/api/credentials/nope", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestAPIStartSession_StarterError(t *testing.T) {
	cred := fakeCredModel("s-6", "fail", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{errOnStart: errors.New("starter boom")}, nil)
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	resp, err := http.Post(srv.URL+"/api/credentials/s-6", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", resp.StatusCode)
	}
}

func TestAPIStartSession_AlreadyRunningReturns409(t *testing.T) {
	cred := fakeCredModel("s-7", "dup", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	_, _ = mgr.Start(cred, share.Options{})
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	resp, err := http.Post(srv.URL+"/api/credentials/s-7", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status=%d, want 409", resp.StatusCode)
	}
}

// ---- DELETE /api/credentials/{id} ---------------------------------

func TestAPIStopSession_Running(t *testing.T) {
	cred := fakeCredModel("d-1", "bye", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	_, _ = mgr.Start(cred, share.Options{})
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/credentials/d-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status=%d, want 204", resp.StatusCode)
	}
	if _, ok := mgr.Get("d-1"); ok {
		t.Errorf("session still present after DELETE")
	}
}

func TestAPIStopSession_ManagerError(t *testing.T) {
	cred := fakeCredModel("d-2", "errstop", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	mgr := NewManager(&fakeStarter{}, nil)
	sess, _ := mgr.Start(cred, share.Options{})
	sess.(*fakeSession).stopErr = errors.New("stop boom")
	srv := newTestServer(t, ServerConfig{Manager: mgr, Loopback: true})

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/credentials/d-2", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "stop boom") {
		t.Errorf("error=%q, want to contain 'stop boom'", body["error"])
	}
}

func TestAPIStopSession_Unknown(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/credentials/nope", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status=%d, want 204 (idempotent)", resp.StatusCode)
	}
}

// ---- POST /api/credentials/{id}/refresh --------------------------

// stubRefresh swaps the refreshCredentialFn seam. Restore via the
// returned closure.
func stubRefresh(fn func(string) (*store.Credential, error)) func() {
	orig := refreshCredentialFn
	refreshCredentialFn = fn
	return func() { refreshCredentialFn = orig }
}

func TestAPIRefreshCredential_HappyPath(t *testing.T) {
	cred := fakeCredModel("r-1", "refreshable", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	defer stubRefresh(func(id string) (*store.Credential, error) {
		if id != "r-1" {
			t.Errorf("refresh got id=%q, want r-1", id)
		}
		// Simulate the post-refresh state.
		out := fakeCredModel(id, "refreshable", "Claude Max 20x")
		out.ClaudeAiOauth.AccessToken = "refreshed-token"
		return out, nil
	})()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Post(srv.URL+"/api/credentials/r-1/refresh", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	var d APICredentialDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Tier != "Claude Max 20x" {
		t.Errorf("Tier=%q, want Claude Max 20x (refreshed profile)", d.Tier)
	}
}

func TestAPIRefreshCredential_Unknown(t *testing.T) {
	defer stubStore(t, nil, nil)()
	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Post(srv.URL+"/api/credentials/nope/refresh", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestAPIRefreshCredential_RefreshError(t *testing.T) {
	cred := fakeCredModel("r-fail", "revoked", "Claude Pro")
	defer stubStore(t, []*store.Credential{cred}, nil)()
	defer stubRefresh(func(string) (*store.Credential, error) {
		return nil, errors.New("refresh token expired or revoked. Re-authenticate with `ccm login`")
	})()

	srv := newTestServer(t, ServerConfig{Manager: NewManager(&fakeStarter{}, nil), Loopback: true})
	resp, err := http.Post(srv.URL+"/api/credentials/r-fail/refresh", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "Re-authenticate") {
		t.Errorf("error=%q, want friendly re-authenticate message", body["error"])
	}
}

// ---- parseTemplatesFrom error branches ---------------------------

func TestParseTemplatesFrom_LoginMissing(t *testing.T) {
	// No templates/login.html in the FS.
	fsys := fstest.MapFS{
		"templates/layout.html": &fstest.MapFile{Data: []byte(`{{define "layout"}}{{end}}`)},
		"templates/app.html":    &fstest.MapFile{Data: []byte(`{{define "body"}}{{end}}`)},
	}
	if _, err := parseTemplatesFrom(fsys); err == nil {
		t.Fatalf("parseTemplatesFrom succeeded; want error for missing login.html")
	}
}

func TestParseTemplatesFrom_AppMissing(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/layout.html": &fstest.MapFile{Data: []byte(`{{define "layout"}}{{end}}`)},
		"templates/login.html":  &fstest.MapFile{Data: []byte(`{{define "body"}}{{end}}`)},
	}
	if _, err := parseTemplatesFrom(fsys); err == nil {
		t.Fatalf("parseTemplatesFrom succeeded; want error for missing app.html")
	}
}

func TestNewHandler_ParseError(t *testing.T) {
	orig := parseTemplatesFunc
	defer func() { parseTemplatesFunc = orig }()
	parseTemplatesFunc = func() (*pages, error) { return nil, errors.New("parse boom") }
	if _, err := NewHandler(ServerConfig{Manager: NewManager(&fakeStarter{}, nil)}); err == nil {
		t.Fatalf("NewHandler succeeded; want parse error")
	}
}

// ---- fake starter / session exposure -----------------------------

// extractFakeStarter lifts the fakeStarter out of a Manager. The
// Manager stores starter as a private field; the test uses reflection-
// free access via a typed interface assertion on the starter it
// passed in at construction time. This keeps the test file the only
// place that reaches inside the Manager.
func extractFakeStarter(t *testing.T, mgr *Manager) *fakeStarter {
	t.Helper()
	starter, ok := mgr.starter.(*fakeStarter)
	if !ok {
		t.Fatalf("manager starter is not a *fakeStarter (%T)", mgr.starter)
	}
	return starter
}
