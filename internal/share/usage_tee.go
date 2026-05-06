package share

import (
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

// installUsageTee wraps r.Body with a usage.TeeBody when conditions
// allow capture: valid Claude Code session ID, no Content-Encoding,
// parseable Content-Type. Best-effort; never blocks the proxy.
func installUsageTee(r *http.Response) {
	if r == nil || r.Request == nil {
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
