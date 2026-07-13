package store_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestGrokCredential_RoundTrip(t *testing.T) {
	exp := time.Now().Add(time.Hour).UnixMilli()
	c := &store.Credential{
		ID:       "gid",
		Name:     "me@example.com",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "acc",
			RefreshToken: "ref",
		},
	}
	c.SetTokens("acc", "ref", exp)

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back store.Credential
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ProviderName() != "grok" {
		t.Fatalf("provider = %q, want grok", back.ProviderName())
	}
	if back.AccessToken() != "acc" || back.RefreshToken() != "ref" {
		t.Fatalf("tokens: got %q/%q", back.AccessToken(), back.RefreshToken())
	}
	if back.ExpiresAtMillis() != exp {
		t.Fatalf("expiry = %d, want %d", back.ExpiresAtMillis(), exp)
	}
}

func TestGrokCredential_ExpiredWhenPast(t *testing.T) {
	c := &store.Credential{ID: "g", Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: "r"}}
	c.SetTokens("a", "r", time.Now().Add(-time.Minute).UnixMilli())
	if !c.IsExpired() {
		t.Fatal("want expired")
	}
}

func TestMarshal_Credential_GrokNilTokens_EmitsEmptyObject(t *testing.T) {
	// When GrokTokens is nil, marshal should emit an empty GrokTokens object.
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "grok", GrokTokens: nil,
		LastRefresh:     "t",
		CreatedAt:       "t",
		LastRefreshedAt: "t",
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tokens"`) {
		t.Fatalf("expected tokens key in output; got %s", b)
	}
}

func TestMarshal_GrokEmitsSubscriptionWhenTierSet(t *testing.T) {
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "grok",
		GrokTokens:      &store.GrokTokens{AccessToken: "a", RefreshToken: "r"},
		LastRefresh:     "t",
		CreatedAt:       "t",
		LastRefreshedAt: "t",
		Subscription:    store.Subscription{Tier: "Pro"},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"subscription"`) {
		t.Fatalf("expected subscription in output when Tier set; got %s", b)
	}
	if !strings.Contains(string(b), `"tier":"Pro"`) {
		t.Fatalf("expected tier:Pro in output; got %s", b)
	}
}

func TestMarshal_GrokOmitsSubscriptionWhenTierEmpty(t *testing.T) {
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "grok",
		GrokTokens:      &store.GrokTokens{AccessToken: "a", RefreshToken: "r"},
		LastRefresh:     "t",
		CreatedAt:       "t",
		LastRefreshedAt: "t",
		// Subscription is zero-value (Tier == "")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"subscription"`) {
		t.Fatalf("expected subscription omitted when Tier empty; got %s", b)
	}
}

func TestUnmarshal_GrokPreservesSubscription(t *testing.T) {
	raw := []byte(`{
		"id":"u","name":"n","provider":"grok",
		"createdAt":"t","lastRefreshedAt":"t",
		"tokens":{"access_token":"a","refresh_token":"r"},
		"expires_at":1,
		"last_refresh":"t",
		"subscription":{"tier":"Pro"}
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Subscription.Tier != "Pro" {
		t.Fatalf("Subscription.Tier = %q, want Pro", c.Subscription.Tier)
	}
}

func TestUnmarshal_GrokBadSubscription_Errors(t *testing.T) {
	// subscription must be an object, not a number.
	raw := []byte(`{
		"id":"u","name":"n","provider":"grok",
		"createdAt":"t","lastRefreshedAt":"t",
		"tokens":{"access_token":"a","refresh_token":"r"},
		"expires_at":1,
		"last_refresh":"t",
		"subscription":42
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when grok subscription is not an object")
	}
}

func TestUnmarshal_GrokBadTokens_Errors(t *testing.T) {
	// tokens must be an object, not a string.
	raw := []byte(`{"id":"x","name":"n","provider":"grok","createdAt":"t","lastRefreshedAt":"t","tokens":"bad","expires_at":1,"last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when grok tokens is not an object")
	}
}

func TestUnmarshal_GrokBadExpiresAt_Errors(t *testing.T) {
	// expires_at must be a number, not a string.
	raw := []byte(`{"id":"x","name":"n","provider":"grok","createdAt":"t","lastRefreshedAt":"t","tokens":{"access_token":"a","refresh_token":"r"},"expires_at":"bad","last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when grok expires_at is not a number")
	}
}

func TestUnmarshal_GrokBadLastRefresh_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","provider":"grok","createdAt":"t","lastRefreshedAt":"t","tokens":{"access_token":"a","refresh_token":"r"},"expires_at":1,"last_refresh":42}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when grok last_refresh is not a string")
	}
}

func TestMarshal_GrokSubscription_RoundTrips(t *testing.T) {
	// Marshal then unmarshal a grok cred with tier; tier must survive.
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "grok",
		GrokTokens:      &store.GrokTokens{AccessToken: "a", RefreshToken: "r"},
		LastRefresh:     "t",
		CreatedAt:       "t",
		LastRefreshedAt: "t",
		Subscription:    store.Subscription{Tier: "Pro"},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var c2 store.Credential
	if err := json.Unmarshal(b, &c2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c2.Subscription.Tier != "Pro" {
		t.Fatalf("Tier after round-trip = %q, want Pro", c2.Subscription.Tier)
	}
}
