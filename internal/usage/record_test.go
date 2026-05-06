package usage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func runtimeIsWindows() bool { return runtime.GOOS == "windows" }

func TestRecord_RoundTrip(t *testing.T) {
	rec := Record{
		TS:     time.Date(2026, 5, 6, 9, 42, 11, 0, time.UTC),
		Model:  "claude-opus-4-7-20251217",
		In:     1523,
		Out:    4102,
		CR:     24190,
		CW:     0,
		Stream: true,
	}
	b, err := rec.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalRecord(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.TS.Equal(rec.TS) || got.Model != rec.Model || got.In != rec.In ||
		got.Out != rec.Out || got.CR != rec.CR || got.CW != rec.CW || got.Stream != rec.Stream {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", rec, got)
	}
}

func TestAppend_CreatesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	rec := Record{TS: time.Now().UTC(), Model: "x", In: 1, Out: 2, CR: 3, CW: 4, Stream: true}
	if err := Append(sid, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(tmp, ".ccm", "usage", sid+".ndjson")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0600 {
		t.Fatalf("file mode = %o, want 0600", mode)
	}
}

func TestAppend_AppendsNewline(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	for i := 0; i < 3; i++ {
		rec := Record{TS: time.Now().UTC(), Model: "x", Out: int64(i + 1)}
		if err := Append(sid, rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(tmp, ".ccm", "usage", sid+".ndjson"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Fatalf("got %d newlines, want 3 (data=%q)", lines, string(data))
	}
}

func TestLoadFile_SkipsCorruptLines(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	good := `{"ts":"2026-05-06T09:42:11Z","model":"opus","in":10,"out":20,"cr":0,"cw":0,"stream":true}`
	bad := `{"ts":"NOT VALID JSON garbage`
	good2 := `{"ts":"2026-05-06T10:00:00Z","model":"sonnet","in":1,"out":2,"cr":0,"cw":0,"stream":false}`
	body := good + "\n" + bad + "\n" + good2 + "\n"
	if err := os.WriteFile(SessionPath(sid), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	recs, err := LoadFile(SessionPath(sid))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (skipped corrupt)", len(recs))
	}
	if recs[0].Model != "opus" || recs[1].Model != "sonnet" {
		t.Fatalf("models = [%q,%q], want [opus,sonnet]", recs[0].Model, recs[1].Model)
	}
}

func TestLoadFile_NotFoundIsError(t *testing.T) {
	tmp := t.TempDir()
	recs, err := LoadFile(filepath.Join(tmp, "nope.ndjson"))
	if err == nil {
		t.Fatalf("LoadFile on missing path should error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if recs != nil {
		t.Fatalf("recs = %v, want nil", recs)
	}
}

// Concurrency relies on POSIX O_APPEND atomicity for sub-PIPE_BUF
// writes; this is the rule on Linux/macOS. Windows handling is
// documented but the test is skipped there to avoid CI flakiness.
func TestAppend_ConcurrentAppendersWholeRecordAtomicity(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("O_APPEND atomicity guarantees differ on Windows; covered by single-threaded test")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	const goroutines, perG = 8, 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				rec := Record{
					TS:    time.Now().UTC(),
					Model: "m",
					Out:   int64(g*1000 + i),
				}
				if err := Append(sid, rec); err != nil {
					t.Errorf("Append(g=%d i=%d): %v", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()
	recs, err := LoadFile(SessionPath(sid))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(recs) != goroutines*perG {
		t.Fatalf("got %d records, want %d (corruption?)", len(recs), goroutines*perG)
	}
}
