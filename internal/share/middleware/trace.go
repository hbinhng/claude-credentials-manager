package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/share/pipeline"
	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

// NewTrace returns a pipeline.Step that emits trace events for every
// inbound request body and outbound response chunk when CCM_TRACE=1
// is set. When trace is disabled, the step is a transparent
// passthrough — no body buffering, no writer wrapping.
//
// The step is provider-agnostic: it sits at the outermost layer of
// the share/launch pipeline so claude and codex traffic is captured
// uniformly.
//
// reqId minted here flows through request context and is read by the
// upstream RoundTripper wrapper to correlate the four streams
// (in.raw, upstream.req, upstream.resp.event, out.event).
func NewTrace() pipeline.Step { return traceStep{} }

type traceStep struct{}

func (traceStep) Apply(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trace.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		reqID := trace.MintReqID()

		// Tee inbound body so we can log it AND let the inner handler
		// read it. r.Body is already nil for some methods (GET); guard.
		var bodyBytes []byte
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err == nil {
				bodyBytes = b
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		trace.EmitRequest(reqID, "in.raw", r.URL.Path, r.Header, bodyBytes)

		r = trace.WithReqID(r, reqID)
		tw := newTraceWriter(w, reqID)
		defer tw.flushTrailing()
		next.ServeHTTP(tw, r)
	})
}

// traceWriter wraps http.ResponseWriter to capture outbound bytes.
// For SSE responses (Content-Type: text/event-stream) every event is
// emitted as a separate "out.event" trace line. For non-SSE responses
// the full body is buffered and emitted on flushTrailing.
//
// When the upstream returns Content-Encoding: gzip on an SSE response,
// the gzip tap goroutine decompresses bytes through a pipe so the
// splitter sees plain text. Client-facing bytes are unchanged.
type traceWriter struct {
	w        http.ResponseWriter
	reqID    string
	splitter *trace.SSESplitter // non-nil only for SSE responses
	body     bytes.Buffer       // populated for non-SSE responses
	wroteHdr bool
	isSSE    bool
	gzipped  bool
	pipeW    *io.PipeWriter // non-nil while gzip-tap goroutine is running
	pipeDone chan struct{}  // closed when gzip-tap goroutine exits
}

func newTraceWriter(w http.ResponseWriter, reqID string) *traceWriter {
	tw := &traceWriter{w: w, reqID: reqID}
	return tw
}

func (tw *traceWriter) Header() http.Header { return tw.w.Header() }

func (tw *traceWriter) WriteHeader(code int) {
	if !tw.wroteHdr {
		tw.detectSSE()
		tw.wroteHdr = true
	}
	tw.w.WriteHeader(code)
}

func (tw *traceWriter) detectSSE() {
	hdr := tw.w.Header()
	ct := hdr.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		return
	}
	tw.isSSE = true
	tw.splitter = &trace.SSESplitter{OnEvent: func(name, data string) {
		trace.EmitEvent(tw.reqID, "out.event", name, data)
	}}
	if strings.EqualFold(hdr.Get("Content-Encoding"), "gzip") {
		tw.gzipped = true
		pr, pw := io.Pipe()
		tw.pipeW = pw
		tw.pipeDone = make(chan struct{})
		go tw.runGzipTap(pr)
	}
}

func (tw *traceWriter) runGzipTap(pr *io.PipeReader) {
	defer close(tw.pipeDone)
	gz, err := gzip.NewReader(pr)
	if err != nil {
		// Bad gzip header — drain and drop. Trace tap is lossy in this
		// case; the client stream is untouched.
		_, _ = io.Copy(io.Discard, pr)
		return
	}
	defer gz.Close()
	buf := make([]byte, 4096)
	for {
		n, err := gz.Read(buf)
		if n > 0 {
			_, _ = tw.splitter.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (tw *traceWriter) Write(p []byte) (int, error) {
	// Implicit WriteHeader (200) on first Write — detect SSE if we
	// haven't yet.
	if !tw.wroteHdr {
		tw.detectSSE()
		tw.wroteHdr = true
	}
	if tw.isSSE {
		if tw.gzipped {
			_, _ = tw.pipeW.Write(p)
		} else {
			_, _ = tw.splitter.Write(p)
		}
	} else {
		tw.body.Write(p)
	}
	return tw.w.Write(p)
}

// Flush implements http.Flusher when the underlying writer does.
func (tw *traceWriter) Flush() {
	if f, ok := tw.w.(http.Flusher); ok {
		f.Flush()
	}
}

// flushTrailing emits any leftover bytes after the inner handler
// returns. For non-SSE responses, this is the full body. For SSE
// responses, this catches any unterminated trailing event.
func (tw *traceWriter) flushTrailing() {
	if tw.isSSE {
		if tw.gzipped && tw.pipeW != nil {
			_ = tw.pipeW.Close()
			<-tw.pipeDone
		}
		if rem := tw.splitter.Buffered(); len(rem) > 0 {
			trace.EmitEvent(tw.reqID, "out.event", "", string(rem))
		}
		return
	}
	if tw.body.Len() > 0 {
		trace.EmitEvent(tw.reqID, "out.event", "", tw.body.String())
	}
}
