package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLoopReplay_NoToolResultExceeds64KB asserts the Phase 9
// truncation cap is enforced: every function_call_output.output in
// the outbound input[] is <= toolResultMaxBytes.
func TestLoopReplay_NoToolResultExceeds64KB(t *testing.T) {
	for i, body := range loadFixtureBodies(t) {
		out, err := TranslateRequest(body, RequestOpts{TargetModel: "gpt-5"})
		if err != nil {
			t.Fatalf("turn %d: TranslateRequest: %v", i, err)
		}
		var probe struct {
			Input []struct {
				Type   string `json:"type"`
				Output string `json:"output,omitempty"`
			} `json:"input"`
		}
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("turn %d: unmarshal: %v", i, err)
		}
		for _, it := range probe.Input {
			if it.Type == "function_call_output" && len(it.Output) > toolResultMaxBytes {
				t.Errorf("turn %d: tool_result %d bytes exceeds cap %d",
					i, len(it.Output), toolResultMaxBytes)
			}
		}
	}
}

// TestLoopReplay_DistinctMessageIDsPerTurn replays each turn's
// upstream response events through StreamTranslator and asserts
// that the outbound message_start event for each turn carries a
// non-empty, distinct id. This locks the fix from commit 769769d:
// without translation of chatgpt.com's response.id into a unique
// Anthropic msg_<id>, Claude Code's normalizeMessagesForAPI merges
// assistant turns into one accumulated message and triggers the
// observed output loop.
func TestLoopReplay_DistinctMessageIDsPerTurn(t *testing.T) {
	turns := loadFixtureTurns(t)
	if len(turns) == 0 {
		t.Fatalf("no turns loaded")
	}

	seen := make(map[string]int, len(turns))
	for i, sse := range turns {
		tr := NewStreamTranslator(StreamOpts{
			Model:     "test-model",
			MessageID: "msg_fallback",
		})
		var out bytes.Buffer
		if err := tr.Pipe(context.Background(), strings.NewReader(sse), &out); err != nil {
			t.Fatalf("turn %d: Pipe: %v", i, err)
		}

		// Find the message_start payload and extract message.id.
		var startID string
		for _, line := range strings.Split(out.String(), "\n") {
			rest, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			if !strings.Contains(rest, `"message_start"`) {
				continue
			}
			var env struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(rest), &env); err != nil {
				t.Fatalf("turn %d: unmarshal message_start: %v", i, err)
			}
			startID = env.Message.ID
			break
		}
		if startID == "" {
			t.Fatalf("turn %d: no message_start.id found in output:\n%s", i, out.String())
		}
		if prev, dup := seen[startID]; dup {
			t.Fatalf("turn %d duplicates id from turn %d: %q", i, prev, startID)
		}
		seen[startID] = i
	}
}

// loadFixtureTurns reconstructs one SSE stream per turn from the
// fixture's upstream.resp.event lines. Groups events by reqId in
// trace-recording order; returns a slice of SSE strings ready to feed
// to StreamTranslator.Pipe.
func loadFixtureTurns(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/loop_replay/sample.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Preserve first-seen order of reqIds.
	var order []string
	events := make(map[string][]string)
	for _, ln := range strings.Split(string(raw), "\n") {
		if len(ln) == 0 || ln[0] != '{' {
			continue
		}
		var l struct {
			Dir   string `json:"dir"`
			ReqID string `json:"reqId"`
			Data  string `json:"data"`
		}
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			continue
		}
		if l.Dir != "upstream.resp.event" {
			continue
		}
		if _, seen := events[l.ReqID]; !seen {
			order = append(order, l.ReqID)
		}
		events[l.ReqID] = append(events[l.ReqID], l.Data)
	}
	turns := make([]string, 0, len(order))
	for _, id := range order {
		// Reassemble SSE: each fixture line is one complete event body
		// (e.g. "event: response.created\ndata: {...}"). Join with the
		// SSE event terminator "\n\n" and append a final terminator.
		turns = append(turns, strings.Join(events[id], "\n\n")+"\n\n")
	}
	return turns
}

// loadFixtureBodies extracts the in.raw /v1/messages bodies from the
// fixture file. Returns them in trace-recording order.
func loadFixtureBodies(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/loop_replay/sample.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var bodies [][]byte
	for _, ln := range strings.Split(string(raw), "\n") {
		if len(ln) == 0 || ln[0] != '{' {
			continue
		}
		var l struct {
			Dir  string `json:"dir"`
			URL  string `json:"url"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			continue
		}
		if l.Dir == "in.raw" && l.URL == "/v1/messages" {
			bodies = append(bodies, []byte(l.Body))
		}
	}
	if len(bodies) < 5 {
		t.Fatalf("fixture has only %d inbound bodies; expected ≥5", len(bodies))
	}
	return bodies
}
