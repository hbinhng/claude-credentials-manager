package credflow

import (
	"fmt"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

var codexRefreshFn = codexoauth.Refresh

// SeamCodexRefresh swaps the codex refresh function. Returns a cleanup
// that restores the original. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamCodexRefresh(fn func(string) (*codexoauth.TokenResponse, error)) func() {
	prev := codexRefreshFn
	codexRefreshFn = fn
	return func() { codexRefreshFn = prev }
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
	if err := store.Save(cred); err != nil {
		return nil, err
	}
	return cred, nil
}
