package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleUsageMissingBearer(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"

	req := httptest.NewRequest("GET", "/ccm-share/usage", nil)
	rr := httptest.NewRecorder()
	p.handleUsage(rr, req)

	if rr.Code != 401 {
		t.Errorf("code = %d, want 401", rr.Code)
	}
}

func TestHandleUsageSingleCredUnconstrained(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"
	// pool is nil → single-cred mode

	req := httptest.NewRequest("GET", "/ccm-share/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	p.handleUsage(rr, req)

	if rr.Code != 200 {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if body["v"].(float64) != 1 {
		t.Errorf("v = %v, want 1", body["v"])
	}
	if body["unconstrained"] != true {
		t.Errorf("unconstrained = %v, want true", body["unconstrained"])
	}
	if body["feasibility_seconds"] != nil {
		t.Errorf("feasibility_seconds should be null; got %v", body["feasibility_seconds"])
	}
}

func TestHandleUsagePoolActivated(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"

	fst := &fakeTokenSource{token: "tk"}
	e := newEntry("a", "n", statusActivated, fst)
	override := 1800.0
	e.feasibilityOverride = &override
	e.lastFeasibility = override
	pool := makePool("a", false, map[string]*poolEntry{"a": e})
	p.pool = pool

	req := httptest.NewRequest("GET", "/ccm-share/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	p.handleUsage(rr, req)

	if rr.Code != 200 {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["feasibility_seconds"].(float64) != 1800.0 {
		t.Errorf("feasibility = %v", body["feasibility_seconds"])
	}
	if body["activated"] != true {
		t.Errorf("activated = %v", body["activated"])
	}
}

func TestHandleUsagePoolNoActivated(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"

	fst := &fakeTokenSource{token: "tk"}
	e := newEntry("a", "n", statusDegraded, fst)
	pool := makePool("", false, map[string]*poolEntry{"a": e})
	p.pool = pool

	req := httptest.NewRequest("GET", "/ccm-share/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	p.handleUsage(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["activated"] != false {
		t.Errorf("activated should be false")
	}
	if body["degraded"] != true {
		t.Errorf("degraded should be true")
	}
}
