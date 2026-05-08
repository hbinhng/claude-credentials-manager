package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type OAuthTokens struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`

	extras map[string]json.RawMessage
}

func (t *OAuthTokens) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil { return err }
	if v, ok := raw["accessToken"]; ok {
		if err := json.Unmarshal(v, &t.AccessToken); err != nil { return err }
		delete(raw, "accessToken")
	}
	if v, ok := raw["refreshToken"]; ok {
		if err := json.Unmarshal(v, &t.RefreshToken); err != nil { return err }
		delete(raw, "refreshToken")
	}
	if v, ok := raw["expiresAt"]; ok {
		if err := json.Unmarshal(v, &t.ExpiresAt); err != nil { return err }
		delete(raw, "expiresAt")
	}
	if v, ok := raw["scopes"]; ok {
		if err := json.Unmarshal(v, &t.Scopes); err != nil { return err }
		delete(raw, "scopes")
	}
	if len(raw) > 0 { t.extras = raw } else { t.extras = nil }
	return nil
}

func (t OAuthTokens) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 4+len(t.extras))
	out["accessToken"] = t.AccessToken
	out["refreshToken"] = t.RefreshToken
	out["expiresAt"] = t.ExpiresAt
	out["scopes"] = t.Scopes
	for k, v := range t.extras { out[k] = v }
	return json.Marshal(out)
}

type Subscription struct {
	Tier string `json:"tier"`
}

type CodexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// Credential is a tagged union. ClaudeAiOauth/Subscription stay
// value-typed because 25+ existing read sites dereference them
// without nil-guards; making them pointers would force a far larger
// refactor for no payoff. Shape isolation between providers comes
// from the custom MarshalJSON/UnmarshalJSON below — codex credentials
// carry zero-value claude fields in memory but never serialize them.
type Credential struct {
	ID              string `json:"-"`
	Name            string `json:"-"`
	Provider        string `json:"-"` // "claude" | "codex"; empty == "claude"
	CreatedAt       string `json:"-"`
	LastRefreshedAt string `json:"-"`

	// Claude shape
	ClaudeAiOauth OAuthTokens  `json:"-"`
	Subscription  Subscription `json:"-"`

	// Codex shape
	AuthMode     string       `json:"-"`
	OpenAIAPIKey *string      `json:"-"` // null when chatgpt mode; ALWAYS emitted for codex
	Tokens       *CodexTokens `json:"-"`
	LastRefresh  string       `json:"-"`

	expiresAtMillis int64 // cached at unmarshal; keeps IsExpired O(1)
}

func (c *Credential) ProviderName() string {
	if c.Provider == "" { return "claude" }
	return c.Provider
}

func (c *Credential) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 12)
	out["id"] = c.ID
	out["name"] = c.Name
	if c.Provider != "" { out["provider"] = c.Provider }
	out["createdAt"] = c.CreatedAt
	out["lastRefreshedAt"] = c.LastRefreshedAt

	switch c.ProviderName() {
	case "claude":
		out["claudeAiOauth"] = c.ClaudeAiOauth
		out["subscription"] = c.Subscription
	case "codex":
		out["auth_mode"] = c.AuthMode
		if c.OpenAIAPIKey != nil { out["OPENAI_API_KEY"] = *c.OpenAIAPIKey } else { out["OPENAI_API_KEY"] = nil }
		if c.Tokens != nil { out["tokens"] = c.Tokens } else { out["tokens"] = CodexTokens{} }
		out["last_refresh"] = c.LastRefresh
		if c.Subscription.Tier != "" {
			out["subscription"] = c.Subscription
		}
	default:
		return nil, fmt.Errorf("store: unknown provider %q", c.Provider)
	}
	return json.Marshal(out)
}

func (c *Credential) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil { return err }

	getString := func(key string) (string, error) {
		v, ok := raw[key]
		if !ok { return "", nil }
		var s string
		if err := json.Unmarshal(v, &s); err != nil { return "", err }
		return s, nil
	}
	var err error
	if c.ID, err = getString("id"); err != nil { return err }
	if c.Name, err = getString("name"); err != nil { return err }
	if c.Provider, err = getString("provider"); err != nil { return err }
	if c.CreatedAt, err = getString("createdAt"); err != nil { return err }
	if c.LastRefreshedAt, err = getString("lastRefreshedAt"); err != nil { return err }

	provider := c.Provider
	if provider == "" { provider = "claude" }

	switch provider {
	case "claude":
		if v, ok := raw["claudeAiOauth"]; ok {
			if err := json.Unmarshal(v, &c.ClaudeAiOauth); err != nil { return err }
		}
		if v, ok := raw["subscription"]; ok {
			if err := json.Unmarshal(v, &c.Subscription); err != nil { return err }
		}
		c.expiresAtMillis = c.ClaudeAiOauth.ExpiresAt
	case "codex":
		if c.AuthMode, err = getString("auth_mode"); err != nil { return err }
		if v, ok := raw["OPENAI_API_KEY"]; ok {
			if string(v) == "null" {
				c.OpenAIAPIKey = nil
			} else {
				var s string
				if err := json.Unmarshal(v, &s); err != nil { return err }
				c.OpenAIAPIKey = &s
			}
		}
		if v, ok := raw["tokens"]; ok {
			c.Tokens = &CodexTokens{}
			if err := json.Unmarshal(v, c.Tokens); err != nil { return err }
		}
		if c.LastRefresh, err = getString("last_refresh"); err != nil { return err }
		if v, ok := raw["subscription"]; ok {
			if err := json.Unmarshal(v, &c.Subscription); err != nil { return err }
		}
		if c.Tokens != nil {
			c.expiresAtMillis = parseJWTExpMillis(c.Tokens.AccessToken)
		}
	default:
		return fmt.Errorf("store: unknown provider %q", provider)
	}
	return nil
}

// parseJWTExpMillis returns exp claim in millis, or 0 if unparseable.
// No signature verification.
func parseJWTExpMillis(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 { return 0 }
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil { return 0 }
	var claims struct{ Exp float64 `json:"exp"` }
	if err := json.Unmarshal(payload, &claims); err != nil { return 0 }
	return int64(claims.Exp * 1000)
}

// expiry returns the effective expiresAtMillis. When the field is zero
// (struct built directly rather than via UnmarshalJSON, which happens in
// tests and cmd/ helpers), we fall back to ClaudeAiOauth.ExpiresAt for
// the claude provider. This keeps IsExpired O(1) while avoiding breakage
// at the 25+ existing call-sites that construct Credential literals.
func (c *Credential) expiry() int64 {
	if c.expiresAtMillis == 0 && c.ProviderName() == "claude" {
		return c.ClaudeAiOauth.ExpiresAt
	}
	return c.expiresAtMillis
}

func (c *Credential) IsExpired() bool {
	return time.Now().UnixMilli() >= c.expiry()
}

func (c *Credential) IsExpiringSoon() bool {
	return !c.IsExpired() && c.expiry()-time.Now().UnixMilli() < 5*60*1000
}

func (c *Credential) Status() string {
	if c.IsExpired() { return "expired" }
	if c.IsExpiringSoon() { return "expiring soon" }
	return "valid"
}

func (c *Credential) ExpiresIn() string {
	diff := c.expiry() - time.Now().UnixMilli()
	if diff <= 0 {
		ago := -diff / 1000
		if ago < 60 { return "just now" }
		if ago < 3600 { return formatUnit(ago/60, "min") + " ago" }
		return formatUnit(ago/3600, "hr") + " ago"
	}
	secs := diff / 1000
	if secs < 60 { return "in " + formatUnit(secs, "sec") }
	if secs < 3600 { return "in " + formatUnit(secs/60, "min") }
	return "in " + formatUnit(secs/3600, "hr")
}

func formatUnit(val int64, unit string) string {
	s := intToStr(val) + " " + unit
	if val != 1 { s += "s" }
	return s
}

func intToStr(v int64) string {
	if v == 0 { return "0" }
	neg := v < 0
	if neg { v = -v }
	digits := make([]byte, 0, 20)
	for v > 0 { digits = append(digits, byte('0'+v%10)); v /= 10 }
	if neg { digits = append(digits, '-') }
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}
