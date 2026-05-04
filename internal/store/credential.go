package store

import (
	"encoding/json"
	"time"
)

// OAuthTokens is ccm's typed view of Claude's OAuth credential blob.
//
// Forward-compat: any JSON keys we don't have a typed field for are
// captured in the unexported `extras` map by UnmarshalJSON and re-
// emitted by MarshalJSON. This preserves fields Claude Code adds in
// future versions without ccm needing to know about them. Currently
// observed extras include `rateLimitTier` and `subscriptionType`.
type OAuthTokens struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`

	// extras holds JSON keys we didn't recognise so we can round-trip
	// them. Lower-cased so it doesn't enter the typed surface; the
	// custom MarshalJSON re-emits its contents alongside the typed
	// fields.
	extras map[string]json.RawMessage
}

// UnmarshalJSON decodes a Claude OAuth blob and captures any unknown
// keys into the extras map.
func (t *OAuthTokens) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Pull out the known fields. Each Unmarshal here can't fail
	// catastrophically — JSON decoded fine above; type-mismatch errors
	// from a malformed Claude write would surface here, and we let
	// them propagate so the caller knows the blob is broken.
	if v, ok := raw["accessToken"]; ok {
		if err := json.Unmarshal(v, &t.AccessToken); err != nil {
			return err
		}
		delete(raw, "accessToken")
	}
	if v, ok := raw["refreshToken"]; ok {
		if err := json.Unmarshal(v, &t.RefreshToken); err != nil {
			return err
		}
		delete(raw, "refreshToken")
	}
	if v, ok := raw["expiresAt"]; ok {
		if err := json.Unmarshal(v, &t.ExpiresAt); err != nil {
			return err
		}
		delete(raw, "expiresAt")
	}
	if v, ok := raw["scopes"]; ok {
		if err := json.Unmarshal(v, &t.Scopes); err != nil {
			return err
		}
		delete(raw, "scopes")
	}

	if len(raw) > 0 {
		t.extras = raw
	} else {
		t.extras = nil
	}
	return nil
}

// MarshalJSON emits the typed fields plus any extras captured from a
// previous Unmarshal.
func (t OAuthTokens) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 4+len(t.extras))
	out["accessToken"] = t.AccessToken
	out["refreshToken"] = t.RefreshToken
	out["expiresAt"] = t.ExpiresAt
	out["scopes"] = t.Scopes
	for k, v := range t.extras {
		out[k] = v
	}
	return json.Marshal(out)
}

type Subscription struct {
	Tier string `json:"tier"`
}

type Credential struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	ClaudeAiOauth   OAuthTokens  `json:"claudeAiOauth"`
	Subscription    Subscription `json:"subscription"`
	CreatedAt       string       `json:"createdAt"`
	LastRefreshedAt string       `json:"lastRefreshedAt"`
}

func (c *Credential) IsExpired() bool {
	return time.Now().UnixMilli() >= c.ClaudeAiOauth.ExpiresAt
}

func (c *Credential) IsExpiringSoon() bool {
	return !c.IsExpired() && c.ClaudeAiOauth.ExpiresAt-time.Now().UnixMilli() < 5*60*1000
}

func (c *Credential) Status() string {
	if c.IsExpired() {
		return "expired"
	}
	if c.IsExpiringSoon() {
		return "expiring soon"
	}
	return "valid"
}

// ExpiresIn returns human-readable relative time.
func (c *Credential) ExpiresIn() string {
	diff := c.ClaudeAiOauth.ExpiresAt - time.Now().UnixMilli()
	if diff <= 0 {
		ago := -diff / 1000
		if ago < 60 {
			return "just now"
		}
		if ago < 3600 {
			return formatUnit(ago/60, "min") + " ago"
		}
		return formatUnit(ago/3600, "hr") + " ago"
	}
	secs := diff / 1000
	if secs < 60 {
		return "in " + formatUnit(secs, "sec")
	}
	if secs < 3600 {
		return "in " + formatUnit(secs/60, "min")
	}
	return "in " + formatUnit(secs/3600, "hr")
}

func formatUnit(val int64, unit string) string {
	s := intToStr(val) + " " + unit
	if val != 1 {
		s += "s"
	}
	return s
}

func intToStr(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	digits := make([]byte, 0, 20)
	for v > 0 {
		digits = append(digits, byte('0'+v%10))
		v /= 10
	}
	if neg {
		digits = append(digits, '-')
	}
	// reverse
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}
