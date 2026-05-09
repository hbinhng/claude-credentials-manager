// Package identity assembles per-request outbound headers for codex
// requests. Identity is fully synthesized from compile-time constants
// (mirroring OmniRoute's approach) plus credential-derived bearer +
// chatgpt-account-id. No spawn-codex-CLI capture needed.
package identity

// Static identity values shipped with ccm. Mirrors what codex CLI 0.129
// emits in its outbound requests; review on codex CLI major version
// bumps. Empirical evidence (OmniRoute production) shows chatgpt.com's
// /backend-api/codex/responses doesn't gate on these, but they're set
// for defensive plausibility.
//
// StaticUserAgent's platform/arch portion is intentionally hardcoded
// (mirrors OmniRoute's "(Windows 10.0.26100; x64)" approach: every
// request looks like the same host). Do NOT derive from runtime.GOOS
// or runtime.GOARCH: doing so would fingerprint each ccm install
// differently and create a unique signal chatgpt.com could
// potentially track.
const (
	StaticVersion    = "0.129.0"
	StaticOpenaiBeta = "responses=experimental"
	StaticUserAgent  = "codex-cli/0.129.0 (Linux; x86_64)"
)
