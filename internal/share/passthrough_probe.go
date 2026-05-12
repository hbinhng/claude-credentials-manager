package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

// passthroughUsage is the parsed shape of the /ccm-share/usage
// response. HTTPStatus is the response status code (200 or 503 only;
// other codes surface as errors).
type passthroughUsage struct {
	HTTPStatus    int
	Feasibility   *float64 // nil when Unconstrained=true OR when 503
	Unconstrained bool
	Activated     bool
	Degraded      bool
}

// usageResponseBody is the JSON shape on the wire. Mirrors §3 of
// the share-passthrough design spec.
type usageResponseBody struct {
	V                  int      `json:"v"`
	FeasibilitySeconds *float64 `json:"feasibility_seconds"`
	Activated          bool     `json:"activated"`
	Degraded           bool     `json:"degraded"`
	Unconstrained      bool     `json:"unconstrained"`
}

// bootstrapProbeTimeout bounds the cmd-time bootstrap and the
// scheduler-tick passthrough probe. Overridable in tests so negative-
// path cases don't burn 10s each.
var bootstrapProbeTimeout = 10 * time.Second

// Sentinel errors returned by fetchPassthroughUsage so callers
// (BootstrapPassthroughProbe) can map HTTP status to specific
// operator-facing messages per spec §6.
var (
	errUnauthorizedUpstream = errors.New("ticket bearer rejected by upstream (HTTP 401)")
	errEndpointMissing      = errors.New("endpoint missing — upstream may be older ccm or not a ccm share (HTTP 404)")
)

// fetchPassthroughUsage performs the GET /ccm-share/usage call
// against the supplied ticket. Returns parsed usage on 200 or 503;
// any other condition (transport error, non-200/503 status, malformed
// body, missing/wrong v) yields a non-nil error.
//
// Overridable in tests via setFetchPassthroughUsage.
func fetchPassthroughUsage(t Ticket) (passthroughUsage, error) {
	url := t.Scheme + "://" + t.Host + "/ccm-share/usage"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return passthroughUsage{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)

	client := &http.Client{Transport: httpx.Transport(), Timeout: bootstrapProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return passthroughUsage{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 503 {
		// Differentiated errors so BootstrapPassthroughProbe can map
		// each known status to a specific operator-facing message.
		switch resp.StatusCode {
		case 401:
			return passthroughUsage{}, errUnauthorizedUpstream
		case 404:
			return passthroughUsage{}, errEndpointMissing
		default:
			return passthroughUsage{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// coverage: unreachable in practice — httptest servers return
		// io.EOF after body; ReadAll consumes through it cleanly.
		return passthroughUsage{}, fmt.Errorf("read body: %w", err)
	}

	var b usageResponseBody
	if err := json.Unmarshal(body, &b); err != nil {
		return passthroughUsage{}, fmt.Errorf("parse body: %w", err)
	}
	if b.V != 1 {
		return passthroughUsage{}, fmt.Errorf("unsupported usage-API version (got v=%d, want v=1)", b.V)
	}

	return passthroughUsage{
		HTTPStatus:    resp.StatusCode,
		Feasibility:   b.FeasibilitySeconds,
		Unconstrained: b.Unconstrained,
		Activated:     b.Activated,
		Degraded:      b.Degraded,
	}, nil
}

// fetchPassthroughUsageFn is the test seam parallel to oauth.FetchUsageFn.
var fetchPassthroughUsageFn = fetchPassthroughUsage

// setFetchPassthroughUsage installs a test stub and returns a
// restorer the caller must defer. Matches the existing
// SetCaptureCredFnForTest stash-and-restore pattern; callers MUST
// NOT run tests using this seam in parallel.
func setFetchPassthroughUsage(fn func(Ticket) (passthroughUsage, error)) func() {
	orig := fetchPassthroughUsageFn
	fetchPassthroughUsageFn = fn
	return func() { fetchPassthroughUsageFn = orig }
}

// probePassthrough is the scheduler-side wrapper. Converts the
// upstream usage response into a probeResult{override: ...}.
//
// Conversion rules (per spec §3):
//   - 200, unconstrained=true       → override = MaxFloat64 (saturating "no pressure")
//   - 200, feasibility_seconds=N    → override = N
//   - 503                           → override = 0 (drops to bottom of ranking; not an error)
func probePassthrough(state *passthroughEntryState) (probeResult, error) {
	u, err := fetchPassthroughUsageFn(state.ticket)
	if err != nil {
		return probeResult{}, fmt.Errorf("usage probe: %w", err)
	}
	var f float64
	switch {
	case u.HTTPStatus == 503:
		f = 0
	case u.Unconstrained:
		f = math.MaxFloat64
	case u.Feasibility != nil:
		f = *u.Feasibility
	default:
		return probeResult{}, fmt.Errorf("usage probe: 200 response without feasibility_seconds and unconstrained=false")
	}
	return probeResult{override: &f}, nil
}
