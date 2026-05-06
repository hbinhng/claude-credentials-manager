package share

import (
	"fmt"
	"net/http"
	"os"
	"strings"
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
		usageTeeDebug("skip: nil request/url")
		return
	}
	if r.Request.URL.Path != "/v1/messages" {
		usageTeeDebug("skip: path=%q (not /v1/messages)", r.Request.URL.Path)
		return
	}
	sid := r.Request.Header.Get("X-Claude-Code-Session-Id")
	if !usage.IsValidSessionID(sid) {
		usageTeeDebug("skip: invalid session-id %q", sid)
		return
	}
	// Anthropic gzip-compresses BOTH streaming and non-streaming
	// /v1/messages responses when Accept-Encoding allows (Go's
	// default transport does). NewSink + gzip wrapper handles this:
	// jsonSink decompresses on Finalize via magic-byte detection;
	// sseSink needs streaming gzip, provided by usage.NewGzipSink.
	ce := r.Header.Get("Content-Encoding")
	ct := r.Header.Get("Content-Type")
	if ce != "" && ce != "gzip" {
		usageTeeDebug("skip: unsupported Content-Encoding=%q", ce)
		return
	}
	sink := usage.NewSink(sid, r.Header, time.Now().UTC())
	if sink == nil {
		usageTeeDebug("skip: NewSink returned nil (Content-Type=%q)", ct)
		return
	}
	if ce == "gzip" && strings.HasPrefix(ct, "text/event-stream") {
		sink = usage.NewGzipSink(sink)
	}
	usageTeeDebug("install: sid=%s ct=%q", sid, r.Header.Get("Content-Type"))
	r.Body = usage.TeeBody(r.Body, sink, sid)
}

func usageTeeDebug(format string, args ...interface{}) {
	if os.Getenv("CCM_DEBUG_USAGE") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "ccm usage tee: "+format+"\n", args...)
}
