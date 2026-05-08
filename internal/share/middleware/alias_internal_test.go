package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

// TestRewriteModelField_Fallbacks exercises the defensive return paths in
// rewriteModelField that can't be reached via normal JSON-validated input.
// These branches are intentionally unreachable from Apply (json.Unmarshal
// already validated a well-formed model field); we test them directly to
// satisfy the ≥99% coverage requirement.
func TestRewriteModelField_NoModelKey(t *testing.T) {
	// Body with no "model" key at all: idx < 0 branch.
	result := rewriteModelField([]byte(`{"messages":[]}`), "gpt-5")
	if !bytes.Equal(result, []byte(`{"messages":[]}`)) {
		t.Errorf("no-key: body changed unexpectedly: %s", result)
	}
}

func TestRewriteModelField_NoColon(t *testing.T) {
	// "model" appears but without colon after it: colonIdx < 0 branch.
	result := rewriteModelField([]byte(`{"model"`), "gpt-5")
	if !bytes.Equal(result, []byte(`{"model"`)) {
		t.Errorf("no-colon: body changed unexpectedly: %s", result)
	}
}

func TestRewriteModelField_NoFirstQuote(t *testing.T) {
	// "model": but value is not a string: q1 < 0 branch.
	result := rewriteModelField([]byte(`{"model":123}`), "gpt-5")
	if !bytes.Equal(result, []byte(`{"model":123}`)) {
		t.Errorf("no-first-quote: body changed unexpectedly: %s", result)
	}
}

func TestRewriteModelField_NoSecondQuote(t *testing.T) {
	// Opening quote exists but closing quote is missing: q2 < 0 branch.
	result := rewriteModelField([]byte(`{"model":"unterminated`), "gpt-5")
	if !bytes.Equal(result, []byte(`{"model":"unterminated`)) {
		t.Errorf("no-second-quote: body changed unexpectedly: %s", result)
	}
}

// errReader simulates a failing io.Reader to exercise the io.ReadAll error
// branch in AliasRewrite.Apply.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read error")
}

func TestAliasRewrite_ReadBodyError400(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-*=gpt-5"})
	mw := NewAliasRewrite(m)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("terminal called despite read error")
	})

	req := httptest.NewRequest("POST", "/v1/messages", io.NopCloser(errReader{}))
	req.ContentLength = 10 // non-zero so body is not skipped
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}
