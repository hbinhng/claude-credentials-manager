package store

import "time"

type OAuthTokens struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
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
