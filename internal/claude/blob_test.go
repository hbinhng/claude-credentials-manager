package claude

import (
	"encoding/json"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestEncodeBlob_RoundTrip(t *testing.T) {
	cred := &store.Credential{
		ID: "cred-1",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken: "a", RefreshToken: "r", ExpiresAt: 1234, Scopes: []string{"s"},
		},
	}
	blob, err := encodeBlob(cred.ID, cred.ClaudeAiOauth)
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}
	id, tokens, ok, err := decodeBlob(blob)
	if err != nil {
		t.Fatalf("decodeBlob: %v", err)
	}
	if !ok || id != "cred-1" || tokens.AccessToken != "a" || tokens.ExpiresAt != 1234 {
		t.Errorf("got id=%q tokens=%+v ok=%v", id, tokens, ok)
	}
}

func TestDecodeBlob_PlainClaudeFile_NoMarker(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "x", "expiresAt": 9},
	})
	id, tokens, ok, err := decodeBlob(body)
	if err != nil {
		t.Fatalf("decodeBlob: %v", err)
	}
	if !ok {
		t.Errorf("ok = false, want true (claudeAiOauth was present)")
	}
	if id != "" {
		t.Errorf("id = %q, want empty (no marker)", id)
	}
	if tokens.AccessToken != "x" {
		t.Errorf("AccessToken = %q, want x", tokens.AccessToken)
	}
}

func TestDecodeBlob_Empty_OkFalse(t *testing.T) {
	id, _, ok, err := decodeBlob(nil)
	if err == nil {
		t.Error("decodeBlob(nil): err = nil, want error")
	}
	if ok || id != "" {
		t.Errorf("got id=%q ok=%v, want empty/false", id, ok)
	}
}

func TestDecodeBlob_Garbage_Errors(t *testing.T) {
	_, _, _, err := decodeBlob([]byte("not json"))
	if err == nil {
		t.Error("decodeBlob garbage: err = nil, want error")
	}
}

func TestEncodeBlob_NarrowsToTwoTopLevelKeys(t *testing.T) {
	blob, err := encodeBlob("id-x", store.OAuthTokens{AccessToken: "a"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("blob has %d top-level keys, want 2 (ccmSourceId + claudeAiOauth)", len(got))
	}
	if _, has := got["ccmSourceId"]; !has {
		t.Error("missing ccmSourceId")
	}
	if _, has := got["claudeAiOauth"]; !has {
		t.Error("missing claudeAiOauth")
	}
}
