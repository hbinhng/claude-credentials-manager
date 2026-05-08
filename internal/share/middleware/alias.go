package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

// AliasRewrite consults the alias map and rewrites the inbound body's
// `model` field. On match: stashes original + effective model in
// context. On no match: marks AliasMatched=false in context, body
// untouched.
type AliasRewrite struct {
	aliasMap *alias.Map
}

// NewAliasRewrite constructs an AliasRewrite step.
func NewAliasRewrite(m *alias.Map) *AliasRewrite {
	return &AliasRewrite{aliasMap: m}
}

// Apply wraps next.
func (a *AliasRewrite) Apply(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		r.Body.Close()

		// Peek at model field. We use a minimal struct rather than
		// generic map[string]any so order-preserving libraries aren't
		// needed for the rewrite.
		var probe struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			http.Error(w, "parse body: "+err.Error(), http.StatusBadRequest)
			return
		}

		original := probe.Model
		target, matched := a.aliasMap.Lookup(original)
		var newBody []byte
		if matched && target != original {
			newBody = rewriteModelField(body, target)
		} else {
			newBody = body
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, keyOriginalModel, original)
		ctx = context.WithValue(ctx, keyEffectiveModel, target)
		ctx = context.WithValue(ctx, keyAliasMatched, matched)

		r2 := r.WithContext(ctx)
		r2.Body = io.NopCloser(bytes.NewReader(newBody))
		r2.ContentLength = int64(len(newBody))
		next.ServeHTTP(w, r2)
	})
}

// rewriteModelField does a best-effort textual rewrite of the body's
// "model" value. We avoid full JSON re-serialization to preserve key
// ordering (prompt-cache prefix discipline). Falls back to a regex-style
// replacement of the first `"model":"..."` occurrence.
func rewriteModelField(body []byte, target string) []byte {
	const key = `"model"`
	idx := bytes.Index(body, []byte(key))
	if idx < 0 {
		return body
	}
	// Skip past "model" + : + optional whitespace + opening quote.
	rest := body[idx+len(key):]
	colonIdx := bytes.IndexByte(rest, ':')
	if colonIdx < 0 {
		return body
	}
	rest = rest[colonIdx+1:]
	// Find first quote.
	q1 := bytes.IndexByte(rest, '"')
	if q1 < 0 {
		return body
	}
	rest = rest[q1+1:]
	q2 := bytes.IndexByte(rest, '"')
	if q2 < 0 {
		return body
	}
	// Reconstruct: original prefix up to and including the opening quote
	// + new value + closing quote and rest.
	prefix := body[:len(body)-len(rest)] // up to and including opening quote
	suffix := rest[q2:]                  // closing quote + rest
	return append(append([]byte{}, prefix...), append([]byte(target), suffix...)...)
}
