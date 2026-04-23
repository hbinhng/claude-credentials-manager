// Package credflow holds shared credential-lifecycle flows (refresh,
// profile sync) that multiple entry points (cmd/refresh, ccm serve,
// any future TUI or RPC surface) delegate to. Keeping the flow in
// one place prevents split-brain between consumers — the same rule
// the share package applies to StartSession.
package credflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// RefreshCredential runs one OAuth refresh round-trip against the
// credential identified by id (must be a stored credential ID, not
// a prefix or name — callers that need fuzzy resolution should
// store.Resolve first). On success it persists the updated tokens,
// opportunistically resyncs the subscription tier via FetchProfile,
// and returns the updated credential.
//
// Returns a friendlier error when the refresh token is dead (401/403)
// so callers can surface "re-authenticate" messaging without parsing
// raw HTTP status strings themselves.
func RefreshCredential(id string) (*store.Credential, error) {
	cred, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	tokens, err := oauthRefreshFn(cred.ClaudeAiOauth.RefreshToken)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			return nil, fmt.Errorf("refresh token expired or revoked. Re-authenticate with `ccm login`")
		}
		return nil, err
	}

	scopes := strings.Fields(tokens.Scope)
	if len(scopes) == 0 {
		scopes = cred.ClaudeAiOauth.Scopes
	}

	cred.ClaudeAiOauth.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		cred.ClaudeAiOauth.RefreshToken = tokens.RefreshToken
	}
	cred.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tokens.ExpiresIn*1000
	cred.ClaudeAiOauth.Scopes = scopes
	cred.LastRefreshedAt = time.Now().UTC().Format(time.RFC3339)

	if profile := oauthFetchProfileFn(cred.ClaudeAiOauth.AccessToken); profile.Tier != "" {
		cred.Subscription.Tier = profile.Tier
	}

	if err := store.Save(cred); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}
	return cred, nil
}

// oauthRefreshFn and oauthFetchProfileFn are the package-level seams
// tests replace to avoid real HTTP round-trips. Consumers outside
// this package never touch them.
var (
	oauthRefreshFn      = oauth.Refresh
	oauthFetchProfileFn = oauth.FetchProfile
)
