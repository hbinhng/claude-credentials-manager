package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
