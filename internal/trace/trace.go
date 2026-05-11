// Package trace implements a request-tracing logger that emits one
// JSONL line per event when CCM_TRACE=1 is set.
//
// Four event directions are emitted by ccm's share/launch pipelines:
//
//   - "in.raw"             — request body received from the client
//   - "upstream.req"       — request body sent to the upstream API
//   - "upstream.resp.event" — one event per chunk of the upstream
//     response (per SSE event for streaming, full body otherwise)
//   - "out.event"          — one event per chunk written back to
//     the client (per SSE event for streaming, full body otherwise)
//
// Each event carries a per-request reqId (UUIDv7) so the four streams
// can be correlated with `jq 'select(.reqId == "...")'`. The reqId is
// minted by the outer trace middleware and propagated through request
// context to the upstream RoundTripper.
//
// Output goes to os.Stderr by default. When clog.Init has redirected
// stderr to CCM_LOG_FILE, trace events land in that file too.
package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EnvVar is the env var name that gates trace logging.
const EnvVar = "CCM_TRACE"

// reqIDContextKey is the request-context key for the reqId minted by
// the outer trace middleware. Defined here (not in the middleware
// package) so the upstream RoundTripper can read it without an import
// cycle.
type reqIDContextKey struct{}

// WithReqID returns a copy of req with the given reqId stored in
// context. Used by the outer trace middleware.
func WithReqID(req *http.Request, id string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), reqIDContextKey{}, id))
}

// ReqIDFrom extracts the reqId from a request's context. Returns ""
// when not set (e.g., trace disabled or middleware skipped).
func ReqIDFrom(req *http.Request) string {
	if v := req.Context().Value(reqIDContextKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// MintReqID returns a new UUIDv7 string for use as a reqId. Falls back
// to a timestamp-based id if the kernel RNG is unavailable.
func MintReqID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// coverage: unreachable — uuid.NewV7 only fails on a kernel
		// RNG outage, which is not exercisable in tests.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return id.String()
}

// Enabled reports whether CCM_TRACE is set to a truthy value.
func Enabled() bool {
	v := os.Getenv(EnvVar)
	return v == "1" || v == "true" || v == "TRUE"
}

// sensitiveHeaders lists header keys redacted from every emitted
// event. Compared case-insensitively.
var sensitiveHeaders = map[string]struct{}{
	"authorization": {},
	"cookie":        {},
	"set-cookie":    {},
	"proxy-authorization": {},
}

// redactHeaders returns a copy of h with sensitive values replaced by
// "[REDACTED]". Single-valued representation is used to keep the JSONL
// flat.
func redactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		lower := lowerASCII(k)
		if _, ok := sensitiveHeaders[lower]; ok {
			out[k] = "[REDACTED]"
			continue
		}
		if len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	return out
}

func lowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// emitMu serializes JSON line writes so concurrent requests don't
// interleave bytes mid-line.
var emitMu sync.Mutex

// Emit writes one JSONL line to os.Stderr. Safe to call when trace is
// disabled — it's a no-op in that case.
//
// extra carries direction-specific keys (e.g. "body", "headers",
// "event", "data", "url"). Standard keys ("ts", "reqId", "dir") are
// added by Emit itself; callers should not set them in extra.
func Emit(reqID, dir string, extra map[string]any) {
	if !Enabled() {
		return
	}
	rec := make(map[string]any, len(extra)+3)
	for k, v := range extra {
		rec[k] = v
	}
	rec["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	rec["reqId"] = reqID
	rec["dir"] = dir
	line, err := json.Marshal(rec)
	if err != nil {
		// coverage: unreachable in practice — json.Marshal only fails
		// on cycles or unsupported types; callers pass strings/[]byte/
		// map[string]any populated from request data, none of which
		// trigger encode errors.
		return
	}
	emitMu.Lock()
	defer emitMu.Unlock()
	_, _ = os.Stderr.Write(append(line, '\n'))
}

// EmitRequest is a convenience wrapper that emits a request-side
// event with redacted headers and (optionally) a body.
func EmitRequest(reqID, dir, url string, headers http.Header, body []byte) {
	if !Enabled() {
		return
	}
	rec := map[string]any{}
	if url != "" {
		rec["url"] = url
	}
	if headers != nil {
		rec["headers"] = redactHeaders(headers)
	}
	if body != nil {
		rec["body"] = string(body)
	}
	Emit(reqID, dir, rec)
}

// EmitEvent emits a streaming-event row. event is the SSE event name
// (or "" for non-SSE chunks); data is the raw event data string.
func EmitEvent(reqID, dir, event, data string) {
	if !Enabled() {
		return
	}
	rec := map[string]any{}
	if event != "" {
		rec["event"] = event
	}
	rec["data"] = data
	Emit(reqID, dir, rec)
}
