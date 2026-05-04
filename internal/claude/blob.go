package claude

import (
	"encoding/json"
	"errors"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// blobShape is the on-wire representation of an active credential, used
// identically by both file and keychain backends. ccmSourceId is the
// marker — when present, ccm owns this entry. claudeAiOauth holds the
// tokens Claude Code reads.
type blobShape struct {
	CCMSourceID   string            `json:"ccmSourceId,omitempty"`
	ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
}

// encodeBlob produces the JSON that lives in the file or keychain entry
// when ccm activates a credential.
func encodeBlob(ccmSourceID string, tokens store.OAuthTokens) ([]byte, error) {
	return json.MarshalIndent(blobShape{
		CCMSourceID:   ccmSourceID,
		ClaudeAiOauth: tokens,
	}, "", "  ")
}

// decodeBlob parses raw bytes from a backend.
//   - id is the ccmSourceId (empty when no marker is present).
//   - tokens is the parsed claudeAiOauth (zero value when absent).
//   - ok is true when the blob contains a recognisable claudeAiOauth
//     section. ok=false + nil err means the blob is structurally valid
//     JSON but Claude has nothing in it.
//   - err is non-nil only on JSON parse failures.
func decodeBlob(data []byte) (id string, tokens store.OAuthTokens, ok bool, err error) {
	if len(data) == 0 {
		return "", store.OAuthTokens{}, false, errors.New("empty blob")
	}
	var b blobShape
	if err := json.Unmarshal(data, &b); err != nil {
		return "", store.OAuthTokens{}, false, err
	}
	ok = b.ClaudeAiOauth.AccessToken != "" || b.ClaudeAiOauth.RefreshToken != "" || b.ClaudeAiOauth.ExpiresAt != 0
	return b.CCMSourceID, b.ClaudeAiOauth, ok, nil
}
