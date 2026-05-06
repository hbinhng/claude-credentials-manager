package share

import (
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

// installUsageTee wraps r.Body with a usage.TeeBody when conditions
// allow capture:
//   - request path is exactly /v1/messages (so non-message endpoints
//     like /v1/messages/count_tokens or /v1/models don't pollute stats)
//   - valid Claude Code session ID
//   - no Content-Encoding (we'd see compressed bytes, not parseable)
//   - parseable Content-Type
//
// Best-effort; never blocks the proxy.
func installUsageTee(r *http.Response) {
	if r == nil || r.Request == nil || r.Request.URL == nil {
		return
	}
	if r.Request.URL.Path != "/v1/messages" {
		return
	}
	sid := r.Request.Header.Get("X-Claude-Code-Session-Id")
	if !usage.IsValidSessionID(sid) {
		return
	}
	if r.Header.Get("Content-Encoding") != "" {
		return
	}
	sink := usage.NewSink(sid, r.Header, time.Now().UTC())
	if sink == nil {
		return
	}
	r.Body = usage.TeeBody(r.Body, sink, sid)
}
