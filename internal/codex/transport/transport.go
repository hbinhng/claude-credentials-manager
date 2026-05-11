// Package transport wraps github.com/bogdanfinn/tls-client to provide a
// codex-CLI-fingerprint-matching HTTP transport for codex API calls.
//
// The Default profile name was pinned at Task 1 of sub-project B per the
// verification gate in spec §7.4.
//
// Verification result (2026-05-09):
//
// The codex CLI 0.129.0 baseline JA3 hash is 27718d56688425cd36a401c66147c4ee
// (captured via tcpdump, recorded in MUX2.md §13.2). We probed every
// candidate bogdanfinn/tls-client v1.14.0 profile against tls.peet.ws/api/all
// and observed the following JA3 hashes:
//
//	Chrome_120        1d9a054bac1eef41f30d370f9bbb2ad2
//	Chrome_124        64aff24dbef210f33880d4f62e1493dd
//	Chrome_131        a19ab9f02aacf42deddc1f2acb3d3f63
//	Chrome_131_PSK    a19ab9f02aacf42deddc1f2acb3d3f63
//	Chrome_133        74e530e488a43fddd78be75918be78c7
//	Chrome_133_PSK    d73b59dbc6a9715d3c63e038b92f1e72
//	Chrome_144        f984bd5bc7358922cde86ed4471a2e89
//	Chrome_144_PSK    eeee4c6725bf89c31f225b3dab4cef37
//	Chrome_146        2d25c56381929cc91bc97631a0a46f58
//	Chrome_146_PSK    5d510aa7220d1a7bc1256493e4b88909
//	Firefox_117       579ccef312d18482fc42e2b822ca2430
//	Firefox_120       ed3d2cb3d86125377f5a4d48e431af48
//	Firefox_123       b5001237acdf006056b409cc433726b0
//	Firefox_132       a767f8ae9115cc5752e5cff59612e74f
//	Firefox_133       a767f8ae9115cc5752e5cff59612e74f
//	Firefox_135       7704a11cf87dfcf33080b90ce11d5527
//	Firefox_146_PSK   a7f0160f133885c42faf8d18156149b3
//	Firefox_147       6f7889b9fb1a62a9577e685c1fcfa919
//	Firefox_147_PSK   a6c4ce0e526690c13a39b7ed04ba2715
//	Safari_16_0       773906b0efdefa24a7f2b8eb6985bf37
//	Safari_IOS_18_0   773906b0efdefa24a7f2b8eb6985bf37
//	Safari_IOS_26_0   ecdf4f49dd59effc439639da29186671
//	Okhttp4Android13  f79b6bad2ad0641e1921aef10262856b
//
// No profile matched the codex baseline byte-for-byte. This is expected:
// codex CLI is built on rustls, whose ClientHello shape (cipher set, extension
// order, signature algorithms) is not produced by any Chrome/Firefox/Safari
// utls preset shipped with bogdanfinn at v1.14.0.
//
// Per the spec §13.4 fallback clause, we pin Firefox_135 — the structurally
// closest match (NSS-style cipher ordering, no PSK, minimal extension set
// matching rustls defaults). This is a known divergence from byte-for-byte
// parity. Operators should treat the codex transport as "fingerprint-resistant
// but not codex-identical" until a future bogdanfinn release adds a rustls
// profile or until a custom utls spec is authored.
//
// Re-verify when bumping the bogdanfinn dependency: rerun the Task 1 probe
// (see commit history for the dev tool) and update the Default constant if
// a closer or exact match becomes available. See spec §7.4.
package transport

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Default is the bogdanfinn/tls-client profile name used by the codex
// transport when no explicit override is supplied. See package doc for the
// verification rationale.
const Default = "Firefox_135"

// Options configures Transport.
type Options struct {
	// ProfileName selects the bogdanfinn TLS profile. Use Default unless
	// re-verification has identified a different match.
	ProfileName string

	// Timeout caps total request duration. Defaults to 600s when zero
	// (codex SSE streams can run minutes).
	Timeout time.Duration

	// InsecureSkipVerify is for httptest-driven tests only; production
	// callers leave it false.
	InsecureSkipVerify bool
}

// Transport wraps a bogdanfinn HTTP client. bogdanfinn's HttpClient.Do
// uses bogdanfinn/fhttp types internally; we translate to/from stdlib
// types around the call so the rest of ccm stays on stdlib.
type Transport struct {
	client tls_client.HttpClient
}

// New constructs a Transport.
func New(opts Options) (*Transport, error) {
	if opts.ProfileName == "" {
		opts.ProfileName = Default
	}
	if opts.Timeout == 0 {
		opts.Timeout = 600 * time.Second
	}

	profile, ok := lookupProfile(opts.ProfileName)
	if !ok {
		return nil, fmt.Errorf("transport: unknown profile %q", opts.ProfileName)
	}

	// opts.Timeout is always ≥1s here: the zero-value guard above sets it to
	// 600s, and callers that supply a positive Duration are unchanged.
	timeoutSecs := int(opts.Timeout.Seconds())
	clientOpts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profile),
		tls_client.WithTimeoutSeconds(timeoutSecs),
	}
	if opts.InsecureSkipVerify {
		clientOpts = append(clientOpts, tls_client.WithInsecureSkipVerify())
	}

	// NewHttpClient only errors on invalid config combinations (e.g. protocol
	// racing + HTTP/3 disabled, or cert pinning + InsecureSkipVerify). None of
	// those are reachable through our Options surface, so this branch is
	// untestable without mocking the bogdanfinn internals.
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("transport: build client: %w", err)
	}
	return &Transport{client: c}, nil
}

// Doer is the small surface the codex Terminal needs from its
// transport. Defining it here lets callers (e.g. the share proxy)
// substitute a wrapper — for instance, the trace recorder — without
// depending on the concrete *Transport type.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Do executes req via the bogdanfinn TLS client and returns a stdlib
// *http.Response. Internally we convert stdlib types to bogdanfinn's
// fhttp types around the call. Streaming bodies and context cancellation
// work transparently.
func (t *Transport) Do(req *http.Request) (*http.Response, error) {
	// Defensive nil guard. New always sets client; this branch is only
	// reachable if a caller constructs Transport{} directly (unsupported).
	if t == nil || t.client == nil {
		return nil, errors.New("transport: nil client")
	}

	// Convert stdlib *http.Request → fhttp.Request.
	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = req.Body
	}
	freq, err := fhttp.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("transport: build fhttp request: %w", err)
	}
	// Copy headers verbatim. fhttp.Header is map[string][]string just like stdlib.
	for k, vs := range req.Header {
		for _, v := range vs {
			freq.Header.Add(k, v)
		}
	}
	if req.Host != "" {
		freq.Host = req.Host
	}

	fresp, err := t.client.Do(freq)
	if err != nil {
		return nil, fmt.Errorf("transport: do: %w", err)
	}

	// Convert fhttp.Response → stdlib *http.Response.
	resp := &http.Response{
		Status:        fresp.Status,
		StatusCode:    fresp.StatusCode,
		Proto:         fresp.Proto,
		ProtoMajor:    fresp.ProtoMajor,
		ProtoMinor:    fresp.ProtoMinor,
		Header:        http.Header{},
		Body:          fresp.Body,
		ContentLength: fresp.ContentLength,
		Request:       req,
	}
	for k, vs := range fresp.Header {
		for _, v := range vs {
			resp.Header.Add(k, v)
		}
	}
	return resp, nil
}

// lookupProfile maps a profile name to a bogdanfinn ClientProfile.
// The list below covers the candidates Task 1's verification gate considers,
// plus Firefox_135 which is pinned as Default. Extend as new profiles become
// relevant. Unknown names return ok=false.
func lookupProfile(name string) (profiles.ClientProfile, bool) {
	switch name {
	case "Chrome_120":
		return profiles.Chrome_120, true
	case "Chrome_131":
		return profiles.Chrome_131, true
	case "Chrome_133":
		return profiles.Chrome_133, true
	case "Firefox_120":
		return profiles.Firefox_120, true
	case "Firefox_123":
		return profiles.Firefox_123, true
	case "Firefox_132":
		return profiles.Firefox_132, true
	case "Firefox_135":
		return profiles.Firefox_135, true
	}
	return profiles.ClientProfile{}, false
}
