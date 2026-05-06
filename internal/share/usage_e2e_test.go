//go:build e2e

// One-shot end-to-end test that drives a real /v1/messages request
// through the LocalProxy using a real ccm credential. Gated behind
// the "e2e" build tag so it doesn't run in `make test`.
//
// Usage:
//
//	CCM_E2E_CRED=<cred-id-or-name> go test -tags=e2e \
//	    -run TestE2E_LiveLocalProxyRecordsUsage \
//	    -v ./internal/share/
package share

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

func TestE2E_LiveLocalProxyRecordsUsage(t *testing.T) {
	credSpec := os.Getenv("CCM_E2E_CRED")
	if credSpec == "" {
		t.Skip("CCM_E2E_CRED not set — skipping live-API e2e test")
	}
	cred, err := store.Resolve(credSpec)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", credSpec, err)
	}

	home, _ := os.UserHomeDir()
	usageDir := filepath.Join(home, ".ccm", "usage")
	preSnap := snapshotUsage(t, usageDir)

	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	sid := "e2e0e2e0-1111-2222-3333-444455556666"
	streaming := os.Getenv("CCM_E2E_STREAM") != ""
	bodyJSON := `{
		"model": "claude-haiku-4-5",
		"max_tokens": 8,
		"messages": [{"role": "user", "content": "say hi in 2 words"}]
	}`
	if streaming {
		bodyJSON = `{
			"model": "claude-haiku-4-5",
			"max_tokens": 8,
			"stream": true,
			"messages": [{"role": "user", "content": "say hi in 2 words"}]
		}`
	}
	body := bytes.NewReader([]byte(bodyJSON))
	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", sid)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")

	t.Logf("sending /v1/messages via %s", lp.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("response %d, %d bytes", resp.StatusCode, len(respBody))
	if resp.StatusCode >= 400 {
		t.Fatalf("upstream returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Wait briefly for tee to finalize.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		postSnap := snapshotUsage(t, usageDir)
		if added := diffUsage(preSnap, postSnap); len(added) > 0 {
			for _, f := range added {
				path := filepath.Join(usageDir, f)
				recs, err := usage.LoadFile(path)
				t.Logf("new file %s: %d records (err=%v)", f, len(recs), err)
				for i, r := range recs {
					t.Logf("  rec[%d]: model=%s in=%d out=%d cr=%d cw=%d stream=%v",
						i, r.Model, r.In, r.Out, r.CR, r.CW, r.Stream)
				}
				if len(recs) == 0 {
					t.Errorf("usage file %s has no records", f)
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no new usage file appeared in %s after request", usageDir)
}

func snapshotUsage(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = info.Size()
	}
	return out
}

func diffUsage(pre, post map[string]int64) []string {
	var added []string
	for k, v := range post {
		if pv, ok := pre[k]; !ok || pv != v {
			added = append(added, k)
		}
	}
	return added
}
