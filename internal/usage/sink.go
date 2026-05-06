package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"sync"
	"time"
)

// Sink consumes the response body bytes and produces zero or one
// Record on Finalize. Implementations: sseSink, jsonSink.
type Sink interface {
	io.Writer
	Finalize() *Record
}

// usageBlock mirrors the Anthropic /v1/messages "usage" object with
// pointer fields so we can distinguish "not present" (don't touch)
// from "present and zero" (overwrite to zero).
type usageBlock struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
}

// ---- sseSink -----------------------------------------------------------

const sseLineCap = 32 * 1024

type sseSink struct {
	line     bytes.Buffer
	event    string
	in, out, cr, cw int64
	model    string
	overflow bool
	poisoned bool
	startTS  time.Time
}

func newSSESink() *sseSink {
	return &sseSink{startTS: time.Now().UTC()}
}

func (s *sseSink) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if !s.overflow {
				s.processLine(s.line.Bytes())
			}
			s.line.Reset()
			s.overflow = false
			continue
		}
		if b == '\r' {
			continue
		}
		if s.line.Len() < sseLineCap {
			s.line.WriteByte(b)
		} else {
			s.overflow = true
		}
	}
	return len(p), nil
}

func (s *sseSink) processLine(b []byte) {
	switch {
	case bytes.HasPrefix(b, []byte("event: ")):
		s.event = string(b[7:])
		if s.event == "error" {
			s.poisoned = true
		}
	case bytes.HasPrefix(b, []byte("data: ")):
		if s.event == "message_start" || s.event == "message_delta" {
			s.parseUsageData(b[6:])
		}
	}
}

func (s *sseSink) parseUsageData(data []byte) {
	// message_start nests usage under "message". message_delta places
	// it at the top level. Try both shapes.
	var ms struct {
		Type    string `json:"type"`
		Message struct {
			Model string     `json:"model"`
			Usage usageBlock `json:"usage"`
		} `json:"message"`
		Usage usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(data, &ms); err != nil {
		return
	}
	if ms.Message.Model != "" {
		s.model = ms.Message.Model
	}
	s.applyUsage(ms.Message.Usage)
	s.applyUsage(ms.Usage)
}

func (s *sseSink) applyUsage(u usageBlock) {
	if u.InputTokens != nil {
		s.in = *u.InputTokens
	}
	if u.OutputTokens != nil {
		s.out = *u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		s.cr = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		s.cw = *u.CacheCreationInputTokens
	}
}

func (s *sseSink) Finalize() *Record {
	if s.poisoned {
		return nil
	}
	if s.in == 0 && s.out == 0 && s.cr == 0 && s.cw == 0 {
		return nil
	}
	return &Record{
		TS:     s.startTS,
		Model:  s.model,
		In:     s.in,
		Out:    s.out,
		CR:     s.cr,
		CW:     s.cw,
		Stream: true,
	}
}

// ---- jsonSink ----------------------------------------------------------

type jsonSink struct {
	buf     bytes.Buffer
	startTS time.Time
}

func newJSONSink() *jsonSink {
	return &jsonSink{startTS: time.Now().UTC()}
}

func (s *jsonSink) Write(p []byte) (int, error) { return s.buf.Write(p) }

func (s *jsonSink) Finalize() *Record {
	var resp struct {
		Model string     `json:"model"`
		Usage usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(s.buf.Bytes(), &resp); err != nil {
		return nil
	}
	in := derefOr(resp.Usage.InputTokens)
	out := derefOr(resp.Usage.OutputTokens)
	cr := derefOr(resp.Usage.CacheReadInputTokens)
	cw := derefOr(resp.Usage.CacheCreationInputTokens)
	if in == 0 && out == 0 && cr == 0 && cw == 0 {
		return nil
	}
	return &Record{
		TS:     s.startTS,
		Model:  resp.Model,
		In:     in,
		Out:    out,
		CR:     cr,
		CW:     cw,
		Stream: false,
	}
}

func derefOr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- dispatch ----------------------------------------------------------

// NewSink returns the appropriate Sink based on the Content-Type header.
// Returns nil if Content-Type is malformed (caller should skip the tee).
//
// sessionID is captured for downstream Append; the dispatcher itself
// does not validate it (TeeBody guards path access).
//
// ts is the response start timestamp; it becomes Record.TS on Finalize.
func NewSink(sessionID string, header http.Header, ts time.Time) Sink {
	ct := header.Get("Content-Type")
	if ct == "" {
		s := newJSONSink()
		s.startTS = ts
		return s
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil
	}
	if mediaType == "text/event-stream" {
		s := newSSESink()
		s.startTS = ts
		return s
	}
	s := newJSONSink()
	s.startTS = ts
	return s
}

// ---- TeeBody -----------------------------------------------------------

// debugLog writes a line to stderr only when CCM_DEBUG_USAGE is set
// in the environment. Default is silent — usage tracking is
// observability and must not contaminate proxy stderr in production.
func debugLog(format string, args ...interface{}) {
	if os.Getenv("CCM_DEBUG_USAGE") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "ccm usage: "+format+"\n", args...)
}

// TeeBody wraps an http.Response.Body with a tee that mirrors bytes
// into sink and finalizes (writing one NDJSON line) on the first
// terminal Read return or on Close, whichever comes first. Idempotent
// via sync.Once.
//
// sessionID must already be validated by the caller; if invalid we
// silently drop on Finalize.
func TeeBody(body io.ReadCloser, sink Sink, sessionID string) io.ReadCloser {
	return &teeBody{
		body:      body,
		sink:      sink,
		sessionID: sessionID,
	}
}

type teeBody struct {
	body      io.ReadCloser
	sink      Sink
	sessionID string
	once      sync.Once
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.body.Read(p)
	if n > 0 {
		t.safeWrite(p[:n])
	}
	if err != nil {
		t.finalize()
	}
	return n, err
}

func (t *teeBody) Close() error {
	t.finalize()
	return t.body.Close()
}

func (t *teeBody) safeWrite(p []byte) {
	defer func() {
		if r := recover(); r != nil {
			debugLog("sink panic recovered: %v", r)
		}
	}()
	if t.sink == nil {
		return
	}
	_, _ = t.sink.Write(p)
}

func (t *teeBody) finalize() {
	t.once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("finalize panic recovered: %v", r)
			}
		}()
		if t.sink == nil {
			return
		}
		rec := t.sink.Finalize()
		if rec == nil {
			return
		}
		if !IsValidSessionID(t.sessionID) {
			return
		}
		if err := Append(t.sessionID, *rec); err != nil {
			debugLog("append failed: %v", err)
		}
	})
}
