package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	"github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
)

func TestAliasRewrite_MatchedRewritesBody(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	mw := middleware.NewAliasRewrite(m)

	var captured map[string]any
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if got := captured["model"]; got != "gpt-5-codex" {
		t.Errorf("rewritten model = %v, want gpt-5-codex", got)
	}
}

func TestAliasRewrite_PassThroughOnNoMatch(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	mw := middleware.NewAliasRewrite(m)

	var captured map[string]any
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
	})

	body := bytes.NewBufferString(`{"model":"gemini-pro","messages":[]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if got := captured["model"]; got != "gemini-pro" {
		t.Errorf("pass-through model = %v, want gemini-pro", got)
	}
}

func TestAliasRewrite_ContextPropagates(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	mw := middleware.NewAliasRewrite(m)

	var origModel, effModel string
	var matched bool
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origModel = middleware.OriginalModel(r.Context())
		effModel = middleware.EffectiveModel(r.Context())
		matched = middleware.AliasMatched(r.Context())
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7"}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if origModel != "claude-opus-4.7" {
		t.Errorf("OriginalModel = %q, want claude-opus-4.7", origModel)
	}
	if effModel != "gpt-5-codex" {
		t.Errorf("EffectiveModel = %q, want gpt-5-codex", effModel)
	}
	if !matched {
		t.Error("AliasMatched = false, want true")
	}
}

func TestAliasRewrite_EmptyMapNoChange(t *testing.T) {
	m, _ := alias.Parse(nil)
	mw := middleware.NewAliasRewrite(m)

	var captured map[string]any
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7"}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if got := captured["model"]; got != "claude-opus-4.7" {
		t.Errorf("empty-map: model = %v, want claude-opus-4.7", got)
	}
}

func TestAliasRewrite_MalformedBody400(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-*=gpt-5"})
	mw := middleware.NewAliasRewrite(m)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("terminal called despite malformed body")
	})

	body := bytes.NewBufferString(`{not json`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestAliasRewrite_NoBodyHandled(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-*=gpt-5"})
	mw := middleware.NewAliasRewrite(m)
	called := false
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/v1/messages", nil)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)
	if !called {
		t.Error("requests with no body should pass through (e.g. health checks)")
	}
}

var _ = context.Background // keep context import
