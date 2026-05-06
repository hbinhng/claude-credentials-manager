package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Record is one captured /v1/messages response. One per NDJSON line.
type Record struct {
	TS     time.Time `json:"ts"`
	Model  string    `json:"model"`
	In     int64     `json:"in"`
	Out    int64     `json:"out"`
	CR     int64     `json:"cr"`
	CW     int64     `json:"cw"`
	Stream bool      `json:"stream"`
}

// Marshal returns the NDJSON-line bytes (without trailing newline).
func (r Record) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

// UnmarshalRecord parses a single NDJSON line.
func UnmarshalRecord(line []byte) (Record, error) {
	var r Record
	err := json.Unmarshal(bytes.TrimSpace(line), &r)
	return r, err
}

// Append writes one record to ~/.ccm/usage/<sessionID>.ndjson.
// Caller MUST validate sessionID with IsValidSessionID first.
//
// Concurrency: relies on POSIX O_APPEND atomicity for sub-PIPE_BUF
// writes. The marshaled record + "\n" is well under PIPE_BUF (4096),
// so concurrent appenders interleave whole records without partial
// corruption. No flock needed.
func Append(sessionID string, rec Record) error {
	if err := EnsureDir(); err != nil {
		return fmt.Errorf("ensure usage dir: %w", err)
	}
	// json.Marshal never fails for Record (all fields are JSON-safe
	// primitives). Skip the err check for compactness.
	data, _ := rec.Marshal()
	line := append(data, '\n')
	f, err := os.OpenFile(SessionPath(sessionID), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("open usage file: %w", err)
	}
	defer f.Close()
	// f.Write error after successful Open is essentially "disk full"
	// or "FD closed" — neither testable from in-process code.
	// coverage: defensive return; impossible to reach with O_APPEND
	// on a freshly-opened file under normal disk conditions.
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write usage line: %w", err)
	}
	return nil
}

// LoadFile reads one NDJSON file and returns its records.
// Malformed lines are skipped silently (bounded blast radius for
// corruption from kill -9 / power loss).
func LoadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		rec, err := UnmarshalRecord(line)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}
