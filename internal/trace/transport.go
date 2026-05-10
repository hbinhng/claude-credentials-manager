package trace

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Doer is a one-method interface matching the codex Transport's
// surface. trace.WrapDoer wraps any Doer to emit upstream.req and
// upstream.resp.event trace lines. Defined here so the share proxy
// can wrap codex's *transport.Transport without importing it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// WrapDoer is the Doer-shaped twin of WrapTransport. Used by the
// share proxy to wrap codex's *transport.Transport (which has Do, not
// RoundTrip). When CCM_TRACE is disabled, returns d unchanged.
func WrapDoer(d Doer) Doer {
	if !Enabled() {
		return d
	}
	return &traceDoer{d: d}
}

type traceDoer struct{ d Doer }

func (t *traceDoer) Do(req *http.Request) (*http.Response, error) {
	// Reuse the RoundTripper logic by adapting the Doer to a
	// RoundTripper at the call boundary.
	return (&traceTransport{rt: doerAsRoundTripper{t.d}}).RoundTrip(req)
}

type doerAsRoundTripper struct{ d Doer }

func (a doerAsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return a.d.Do(req)
}

// WrapTransport returns a RoundTripper that emits "upstream.req" and
// "upstream.resp.event" trace events for every HTTP round-trip when
// CCM_TRACE=1 is set. When trace is disabled, it returns rt unchanged
// so there is zero overhead in the common path.
//
// The reqId is read from req.Context() (set by WithReqID at the
// outer middleware). When absent, the events are still emitted with
// reqId="" — this just means correlation is impossible, not that we
// drop the event.
//
// rt may be nil — the wrapper falls back to http.DefaultTransport in
// that case (matching net/http convention).
func WrapTransport(rt http.RoundTripper) http.RoundTripper {
	if !Enabled() {
		return rt
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &traceTransport{rt: rt}
}

type traceTransport struct {
	rt http.RoundTripper
}

func (t *traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqID := ReqIDFrom(req)

	// Tee the request body for logging.
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err == nil {
			bodyBytes = b
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		// Restore ContentLength for downstream reuse.
		if req.ContentLength == 0 {
			req.ContentLength = int64(len(bodyBytes))
		}
	}
	url := ""
	if req.URL != nil {
		url = req.URL.String()
	}
	EmitRequest(reqID, "upstream.req", url, req.Header, bodyBytes)

	resp, err := t.rt.RoundTrip(req)
	if err != nil {
		// coverage: covered by an explicit test using a transport
		// stub that returns an error. Logging the error keeps the
		// trace timeline complete.
		EmitRequest(reqID, "upstream.resp.error", url, nil, []byte(err.Error()))
		return nil, err
	}

	// Wrap the response body so we can tee bytes as the caller
	// reads them.
	resp.Body = newRespTeeReader(resp.Body, reqID, isSSEResp(resp))
	return resp, nil
}

func isSSEResp(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
}

// respTeeReader implements io.ReadCloser. It forwards reads from the
// upstream body but also feeds an SSE splitter (or a plain buffer for
// non-SSE) so trace events fire as bytes arrive.
type respTeeReader struct {
	src        io.ReadCloser
	reqID      string
	isSSE      bool
	sseDecided bool
	splitter   *SSESplitter // non-nil iff isSSE
	body       bytes.Buffer // populated when not SSE
	closed     bool
}

func newRespTeeReader(src io.ReadCloser, reqID string, sse bool) *respTeeReader {
	r := &respTeeReader{src: src, reqID: reqID, isSSE: sse}
	if sse {
		r.sseDecided = true
		r.splitter = &SSESplitter{OnEvent: func(name, data string) {
			EmitEvent(reqID, "upstream.resp.event", name, data)
		}}
	}
	return r
}

func (r *respTeeReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		if !r.sseDecided {
			// First chunk: sniff for SSE shape if header was inconclusive.
			if !r.isSSE && looksLikeSSE(p[:n]) {
				r.isSSE = true
				r.splitter = &SSESplitter{OnEvent: func(name, data string) {
					EmitEvent(r.reqID, "upstream.resp.event", name, data)
				}}
			}
			r.sseDecided = true
		}
		if r.isSSE {
			_, _ = r.splitter.Write(p[:n])
		} else {
			r.body.Write(p[:n])
		}
	}
	if err == io.EOF {
		r.flushTrailing()
	}
	return n, err
}

func (r *respTeeReader) Close() error {
	if !r.closed {
		r.closed = true
		r.flushTrailing()
	}
	return r.src.Close()
}

func (r *respTeeReader) flushTrailing() {
	if r.isSSE {
		if rem := r.splitter.Buffered(); len(rem) > 0 {
			EmitEvent(r.reqID, "upstream.resp.event", "", string(rem))
			// Drain the splitter's buffer so a subsequent flush
			// (e.g. Close after EOF) doesn't double-emit.
			_, _ = r.splitter.buf.ReadString(0)
			r.splitter.buf.Reset()
		}
		return
	}
	if r.body.Len() > 0 {
		EmitEvent(r.reqID, "upstream.resp.event", "", r.body.String())
		r.body.Reset()
	}
}

// looksLikeSSE reports whether p starts with bytes that look like an
// SSE event stream — specifically a leading `event:` or `data:` line.
// Used as a fallback when the response Content-Type doesn't match
// the canonical text/event-stream MIME (chatgpt.com's codex endpoint
// returns SSE under a non-standard Content-Type).
func looksLikeSSE(p []byte) bool {
	trimmed := bytes.TrimLeft(p, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
}
