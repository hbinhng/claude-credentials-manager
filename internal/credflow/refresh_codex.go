package credflow

import (
	"fmt"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

var codexRefreshFn = codexoauth.Refresh

// codexUsageFn is the seam tests replace to inject a canned usage response
// without spinning up an httptest server. Production: codexoauth.FetchUsage.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
var codexUsageFn func(string, string) *oauth.UsageInfo = codexoauth.FetchUsage

// SeamCodexRefresh swaps the codex refresh function. Returns a cleanup
// that restores the original. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamCodexRefresh(fn func(string) (*codexoauth.TokenResponse, error)) func() {
	prev := codexRefreshFn
	codexRefreshFn = fn
	return func() { codexRefreshFn = prev }
}

// SeamCodexUsage swaps the codex usage function. Returns a cleanup that
// restores the original. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamCodexUsage(fn func(string, string) *oauth.UsageInfo) func() {
	prev := codexUsageFn
	codexUsageFn = fn
	return func() { codexUsageFn = prev }
}

func refreshCodexLocked(cred *store.Credential) (*store.Credential, error) {
	if cred.Tokens == nil || cred.Tokens.RefreshToken == "" {
		return nil, fmt.Errorf("credential is missing tokens; run `ccm login codex` to re-create")
	}
	tr, err := codexRefreshFn(cred.Tokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	cred.Tokens.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		cred.Tokens.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		cred.Tokens.IDToken = tr.IDToken
	}
	now := time.Now().UTC()
	cred.LastRefreshedAt = now.Format(time.RFC3339)
	cred.LastRefresh = now.Format(time.RFC3339Nano)

	// Best-effort tier refresh from usage endpoint. Don't fail refresh on
	// usage errors — the tokens are good either way.
	if usage := codexUsageFn(cred.Tokens.AccessToken, cred.Tokens.AccountID); usage != nil && usage.Tier != "" {
		cred.Subscription.Tier = usage.Tier
	}

	if err := store.Save(cred); err != nil {
		return nil, err
	}
	return cred, nil
}
