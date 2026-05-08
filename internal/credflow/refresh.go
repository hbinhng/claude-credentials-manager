// Package credflow holds shared credential-lifecycle flows (refresh,
// profile sync) that multiple entry points (cmd/refresh, ccm serve,
// any future TUI or RPC surface) delegate to. Keeping the flow in
// one place prevents split-brain between consumers — the same rule
// the share package applies to StartSession.
package credflow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// oauthRefreshFn and oauthFetchProfileFn are the package-level seams
// tests replace to avoid real HTTP round-trips. Consumers outside
// this package never touch them.
var (
	oauthRefreshFn      = oauth.Refresh
	oauthFetchProfileFn = oauth.FetchProfile
)

// SeamBetweenResolveAndLock fires after store.Resolve and before lock
// acquisition. Tests use it to simulate cross-process rotation during
// that window. Production: nil (no-op).
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
var SeamBetweenResolveAndLock func(id string)

// RefreshCredential rotates the access token (and refresh token when
// the provider rotates them). The token-mutating section runs inside
// store.WithCredentialLock to prevent two ccm processes from racing
// on a rotating refresh_token.
func RefreshCredential(id string) (*store.Credential, error) {
	cred, err := store.Resolve(id)
	if err != nil {
		return nil, err
	}

	if SeamBetweenResolveAndLock != nil {
		SeamBetweenResolveAndLock(cred.ID)
	}

	var refreshed *store.Credential
	err = store.WithCredentialLock(cred.ID, func() error {
		fresh, err := store.Load(cred.ID)
		if err != nil {
			return err
		}
		if accessTokenDiffers(fresh, cred) {
			// Cross-process winner already wrote new tokens; trust disk.
			refreshed = fresh
			return nil
		}
		switch fresh.ProviderName() {
		case "claude":
			out, err := refreshClaudeLocked(fresh)
			if err != nil {
				return err
			}
			refreshed = out
			return nil
		case "codex":
			out, err := refreshCodexLocked(fresh)
			if err != nil {
				if errors.Is(err, codexoauth.ErrRefreshRotated) {
					disk2, derr := store.Load(cred.ID)
					if derr == nil && accessTokenDiffers(disk2, cred) {
						refreshed = disk2
						return nil
					}
					return fmt.Errorf("refresh token has been invalidated; run `ccm login codex` to re-authenticate")
				}
				return err
			}
			refreshed = out
			return nil
		default:
			// untestable: store.UnmarshalJSON rejects unknown providers before reaching this switch
			return fmt.Errorf("credflow: unknown provider %q", fresh.ProviderName())
		}
	})
	return refreshed, err
}

func accessTokenDiffers(disk, mem *store.Credential) bool {
	switch disk.ProviderName() {
	case "claude":
		return disk.ClaudeAiOauth.AccessToken != mem.ClaudeAiOauth.AccessToken
	case "codex":
		if disk.Tokens == nil || mem.Tokens == nil {
			return false
		}
		return disk.Tokens.AccessToken != mem.Tokens.AccessToken
	}
	return false
}

// refreshClaudeLocked is the existing claude refresh+save logic, now
// called inside the per-credential lock. Behavior preserved verbatim
// including the 401/403 friendly-error branch.
func refreshClaudeLocked(cred *store.Credential) (*store.Credential, error) {
	tokens, err := oauthRefreshFn(cred.ClaudeAiOauth.RefreshToken)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			return nil, fmt.Errorf("refresh token expired or revoked. Re-authenticate with `ccm login claude`")
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
