package responses

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// ErrNoMessage is returned by AssembleMessage when the SSE carried no
// message_start (nothing to assemble).
var ErrNoMessage = errors.New("responses: stream carried no message")

// assembleMaxLine caps one SSE line; matches Collect's 64 MiB buffer cap.
// A var so tests can lower it.
var assembleMaxLine = 64 << 20

// AssembleMessage folds Anthropic SSE (as produced by StreamTranslator) into
// one Anthropic Message JSON for stream:false callers. An `error` event's
// data is returned verbatim as the body.
func AssembleMessage(sse []byte) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(sse))
	sc.Buffer(make([]byte, 0, min(64*1024, assembleMaxLine)), assembleMaxLine)

	var msg map[string]any
	var blocks []map[string]any
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if event == "error" {
			return []byte(data), nil
		}
		var ev struct {
			Type         string         `json:"type"`
			Message      map[string]any `json:"message"`
			Index        int            `json:"index"`
			ContentBlock map[string]any `json:"content_block"`
			Delta        map[string]any `json:"delta"`
			Usage        map[string]any `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			msg = ev.Message
		case "content_block_start":
			for len(blocks) <= ev.Index {
				blocks = append(blocks, nil)
			}
			blocks[ev.Index] = ev.ContentBlock
		case "content_block_delta":
			b := blockAt(blocks, ev.Index)
			if b == nil {
				continue
			}
			switch ev.Delta["type"] {
			case "text_delta":
				b["text"] = str(b["text"]) + str(ev.Delta["text"])
			case "thinking_delta":
				b["thinking"] = str(b["thinking"]) + str(ev.Delta["thinking"])
			case "signature_delta":
				b["signature"] = str(ev.Delta["signature"])
			case "input_json_delta":
				b["_partial"] = str(b["_partial"]) + str(ev.Delta["partial_json"])
			}
		case "content_block_stop":
			b := blockAt(blocks, ev.Index)
			if b == nil {
				continue
			}
			if p := str(b["_partial"]); p != "" {
				var input any
				if json.Unmarshal([]byte(p), &input) == nil {
					b["input"] = input
				}
			}
			delete(b, "_partial")
		case "message_delta":
			if msg == nil {
				continue
			}
			for k, v := range ev.Delta {
				msg[k] = v
			}
			if ev.Usage != nil {
				msg["usage"] = ev.Usage
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, ErrNoMessage
	}
	content := make([]any, 0, len(blocks))
	for _, b := range blocks {
		if b != nil {
			content = append(content, b)
		}
	}
	msg["content"] = content
	return json.Marshal(msg)
}

func blockAt(blocks []map[string]any, i int) map[string]any {
	if i < 0 || i >= len(blocks) {
		return nil
	}
	return blocks[i]
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
