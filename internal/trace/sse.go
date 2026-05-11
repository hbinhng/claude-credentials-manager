package trace

import (
	"bytes"
	"strings"
)

// SSESplitter splits a stream of bytes (fed via Write) on SSE event
// boundaries. Each complete event (delimited by "\n\n") is reported
// once via the OnEvent callback as a (name, data) pair, where name is
// the value of the first "event:" line and data is the concatenation
// of all "data:" line payloads.
//
// Trailing bytes (an unterminated event at end of stream) are NOT
// emitted automatically. Call Flush to emit them as a final chunk if
// desired — but most well-behaved SSE producers terminate every event
// with the double newline, so Flush is rarely needed in practice.
type SSESplitter struct {
	buf     bytes.Buffer
	OnEvent func(name, data string)
}

// Write appends p to the splitter's buffer and emits a callback for
// every complete event found. Always returns len(p), nil — Write
// never fails for an in-memory buffer.
func (s *SSESplitter) Write(p []byte) (int, error) {
	s.buf.Write(p)
	for {
		raw := s.buf.Bytes()
		idx := bytes.Index(raw, []byte("\n\n"))
		if idx < 0 {
			break
		}
		event := string(raw[:idx])
		// Drop the "\n\n" terminator from the buffer.
		_ = s.buf.Next(idx + 2)
		s.dispatch(event)
	}
	return len(p), nil
}

func (s *SSESplitter) dispatch(event string) {
	if s.OnEvent == nil {
		return
	}
	var name string
	var data strings.Builder
	for _, line := range strings.Split(event, "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			if name == "" {
				name = strings.TrimSpace(line[len("event:"):])
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	s.OnEvent(name, data.String())
}

// Buffered returns any unflushed bytes (incomplete trailing event).
// Callers that want to log them should do so explicitly on stream
// close.
func (s *SSESplitter) Buffered() []byte {
	return s.buf.Bytes()
}
