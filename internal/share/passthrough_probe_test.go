package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchPassthroughUsage200OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ccm-share/usage" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tk" {
			t.Errorf("missing/wrong bearer")
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"v":                   1,
			"feasibility_seconds": 1800.0,
			"activated":           true,
			"degraded":            false,
			"unconstrained":       false,
		})
	}))
	defer srv.Close()

	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	r, err := fetchPassthroughUsage(tk)
	if err != nil {
		t.Fatalf("fetchPassthroughUsage: %v", err)
	}
	if r.HTTPStatus != 200 || r.Feasibility == nil || *r.Feasibility != 1800.0 {
		t.Errorf("unexpected: %+v", r)
	}
	if r.Unconstrained {
		t.Errorf("unconstrained should be false")
	}
}

func TestFetchPassthroughUsage503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"v":                   1,
			"feasibility_seconds": 0,
			"activated":           false,
			"degraded":            true,
			"unconstrained":       false,
		})
	}))
	defer srv.Close()

	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	r, err := fetchPassthroughUsage(tk)
	if err != nil {
		t.Fatalf("503 should not error: %v", err)
	}
	if r.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503", r.HTTPStatus)
	}
}

func TestFetchPassthroughUsageUnconstrainedNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"v":1,"feasibility_seconds":null,"activated":true,"degraded":false,"unconstrained":true}`))
	}))
	defer srv.Close()

	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	r, err := fetchPassthroughUsage(tk)
	if err != nil {
		t.Fatalf("unconstrained should not error: %v", err)
	}
	if !r.Unconstrained || r.Feasibility != nil {
		t.Errorf("unexpected: %+v", r)
	}
}

func TestFetchPassthroughUsage401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := fetchPassthroughUsage(tk)
	if err == nil {
		t.Fatalf("401 should error")
	}
}

func TestFetchPassthroughUsage404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	if _, err := fetchPassthroughUsage(tk); err == nil {
		t.Fatalf("404 should error")
	}
}

func TestFetchPassthroughUsageMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	if _, err := fetchPassthroughUsage(tk); err == nil {
		t.Fatalf("malformed JSON should error")
	}
}

func TestFetchPassthroughUsageWrongVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"v":99,"feasibility_seconds":0,"activated":true,"degraded":false,"unconstrained":false}`))
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	if _, err := fetchPassthroughUsage(tk); err == nil {
		t.Fatalf("v != 1 should error")
	}
}

func TestProbePassthroughOK(t *testing.T) {
	defer setFetchPassthroughUsage(func(t Ticket) (passthroughUsage, error) {
		f := 1800.0
		return passthroughUsage{HTTPStatus: 200, Feasibility: &f}, nil
	})()

	pt := newPassthroughEntryState(Ticket{Scheme: "https", Host: "x", Token: "tk"})
	result, err := probePassthrough(pt)
	if err != nil {
		t.Fatalf("probePassthrough: %v", err)
	}
	if result.override == nil || *result.override != 1800.0 {
		t.Errorf("unexpected: %+v", result)
	}
}

func TestProbePassthroughUnconstrainedSaturates(t *testing.T) {
	defer setFetchPassthroughUsage(func(t Ticket) (passthroughUsage, error) {
		return passthroughUsage{HTTPStatus: 200, Unconstrained: true}, nil
	})()

	pt := newPassthroughEntryState(Ticket{Scheme: "https", Host: "x", Token: "tk"})
	result, err := probePassthrough(pt)
	if err != nil {
		t.Fatalf("probePassthrough: %v", err)
	}
	if result.override == nil || *result.override < 1e308 {
		t.Errorf("unconstrained should saturate to MaxFloat64; got %v", result.override)
	}
}

func TestProbePassthrough503OverrideZero(t *testing.T) {
	defer setFetchPassthroughUsage(func(t Ticket) (passthroughUsage, error) {
		f := 0.0
		return passthroughUsage{HTTPStatus: 503, Feasibility: &f}, nil
	})()

	pt := newPassthroughEntryState(Ticket{Scheme: "https", Host: "x", Token: "tk"})
	result, err := probePassthrough(pt)
	if err != nil {
		t.Fatalf("503 should not error from probePassthrough: %v", err)
	}
	if result.override == nil || *result.override != 0 {
		t.Errorf("503 should set override=0; got %v", result.override)
	}
}

func TestBootstrapPassthroughProbeAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := BootstrapPassthroughProbe(tk)
	if err == nil || !strings.Contains(err.Error(), "rejected by upstream") {
		t.Errorf("401 should produce 'rejected by upstream' message; got %v", err)
	}
}

func TestBootstrapPassthroughProbeEndpointMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := BootstrapPassthroughProbe(tk)
	if err == nil || !strings.Contains(err.Error(), "endpoint missing") {
		t.Errorf("404 should produce 'endpoint missing' message; got %v", err)
	}
}

func TestBootstrapPassthroughProbeGenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := BootstrapPassthroughProbe(tk)
	if err == nil || !strings.Contains(err.Error(), "not a ccm share endpoint") {
		t.Errorf("500 should produce 'not a ccm share endpoint' message; got %v", err)
	}
}

func TestBootstrapPassthroughProbeDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"v":1,"feasibility_seconds":0,"activated":false,"degraded":true,"unconstrained":false}`))
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	seed, err := BootstrapPassthroughProbe(tk)
	if err != nil {
		t.Fatalf("503 should admit-as-degraded, not error: %v", err)
	}
	if !seed.Degraded {
		t.Errorf("seed.Degraded should be true")
	}
	if seed.Feasibility == nil || *seed.Feasibility != 0 {
		t.Errorf("degraded seed feasibility should be 0; got %v", seed.Feasibility)
	}
}

// TestBootstrapPassthroughProbeVersionMismatch pins the operator-facing
// error string for v != 1 responses (spec §3/§6).
func TestBootstrapPassthroughProbeVersionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"v":2,"feasibility_seconds":1.0,"activated":true,"degraded":false,"unconstrained":false}`))
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := BootstrapPassthroughProbe(tk)
	if err == nil || !strings.Contains(err.Error(), "unsupported usage-API version") || !strings.Contains(err.Error(), "got v=2") {
		t.Errorf("v=2 should produce 'unsupported usage-API version (got v=2…)' message; got %v", err)
	}
}

// TestBootstrapPassthroughProbeMissingV pins the operator-facing error
// for a 200 response missing the v field (spec §3/§6).
func TestBootstrapPassthroughProbeMissingV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"feasibility_seconds":1.0,"activated":true}`))
	}))
	defer srv.Close()
	tk := Ticket{Scheme: "http", Host: srv.Listener.Addr().String(), Token: "tk"}
	_, err := BootstrapPassthroughProbe(tk)
	if err == nil || !strings.Contains(err.Error(), "unrecognized response from /ccm-share/usage") {
		t.Errorf("missing v should produce 'unrecognized response from /ccm-share/usage'; got %v", err)
	}
}
