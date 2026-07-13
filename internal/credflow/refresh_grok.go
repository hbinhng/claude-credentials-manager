package credflow

import (
	"fmt"
	"time"

	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

var grokRefreshFn = grokoauth.Refresh

// SeamGrokRefresh swaps the grok refresh function. Returns a cleanup that
// restores the original. Test-only. NOT goroutine-safe.
func SeamGrokRefresh(fn func(string) (*grokoauth.TokenResponse, error)) func() {
	prev := grokRefreshFn
	grokRefreshFn = fn
	return func() { grokRefreshFn = prev }
}

func refreshGrokLocked(cred *store.Credential) (*store.Credential, error) {
	if cred.GrokTokens == nil || cred.GrokTokens.RefreshToken == "" {
		return nil, fmt.Errorf("credential is missing tokens; run `ccm login grok` to re-create")
	}
	tr, err := grokRefreshFn(cred.GrokTokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	refresh := tr.RefreshToken
	if refresh == "" {
		refresh = cred.GrokTokens.RefreshToken
	}
	cred.SetTokens(tr.AccessToken, refresh, now.UnixMilli()+int64(tr.ExpiresIn)*1000)
	cred.LastRefreshedAt = now.Format(time.RFC3339)
	cred.LastRefresh = now.Format(time.RFC3339Nano)

	if err := store.Save(cred); err != nil {
		return nil, err
	}
	return cred, nil
}
