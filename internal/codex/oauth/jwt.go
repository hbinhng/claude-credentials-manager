package codexoauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type Claims struct {
	Email          string
	AccountID      string
	PlanType       string
	ExpUnixSeconds int64
}

// ParseClaims decodes a JWT's payload (no signature verification) and
// extracts the fields used by ccm display logic.
func ParseClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("codexoauth: token has %d segments, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("codexoauth: decode payload: %w", err)
	}
	var raw struct {
		Email string  `json:"email"`
		Exp   float64 `json:"exp"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			PlanType  string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("codexoauth: parse claims: %w", err)
	}
	return Claims{
		Email:          raw.Email,
		AccountID:      raw.Auth.AccountID,
		PlanType:       raw.Auth.PlanType,
		ExpUnixSeconds: int64(raw.Exp),
	}, nil
}
