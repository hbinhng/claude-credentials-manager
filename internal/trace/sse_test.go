package trace

import (
	"reflect"
	"testing"
)

type evt struct{ name, data string }

func collect(t *testing.T, chunks ...string) []evt {
	t.Helper()
	var got []evt
	s := &SSESplitter{OnEvent: func(name, data string) {
		got = append(got, evt{name, data})
	}}
	for _, c := range chunks {
		_, _ = s.Write([]byte(c))
	}
	return got
}

func TestSSESplitter_SingleEvent(t *testing.T) {
	got := collect(t, "event: foo\ndata: {\"x\":1}\n\n")
	want := []evt{{"foo", `{"x":1}`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSSESplitter_MultipleEvents(t *testing.T) {
	got := collect(t,
		"event: a\ndata: 1\n\n",
		"event: b\ndata: 2\n\n",
	)
	want := []evt{{"a", "1"}, {"b", "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSSESplitter_EventSplitAcrossWrites(t *testing.T) {
	got := collect(t, "event: foo\nda", "ta: hello\n\n")
	want := []evt{{"foo", "hello"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSSESplitter_MultilineData(t *testing.T) {
	got := collect(t, "event: x\ndata: line1\ndata: line2\n\n")
	want := []evt{{"x", "line1\nline2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSSESplitter_NoEventName(t *testing.T) {
	got := collect(t, "data: solo\n\n")
	want := []evt{{"", "solo"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSSESplitter_BufferedHoldsUnterminated(t *testing.T) {
	s := &SSESplitter{OnEvent: func(name, data string) {
		t.Fatalf("unexpected dispatch: %q %q", name, data)
	}}
	_, _ = s.Write([]byte("event: foo\ndata: not-yet"))
	if got := string(s.Buffered()); got != "event: foo\ndata: not-yet" {
		t.Errorf("Buffered = %q", got)
	}
}

func TestSSESplitter_NilOnEventDoesNotPanic(t *testing.T) {
	s := &SSESplitter{}
	_, _ = s.Write([]byte("event: x\ndata: y\n\n"))
}

func TestSSESplitter_FirstEventNameWinsOnDuplicateEventLines(t *testing.T) {
	got := collect(t, "event: first\nevent: second\ndata: x\n\n")
	want := []evt{{"first", "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
