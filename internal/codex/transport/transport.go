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

// Default is the bogdanfinn/tls-client profile name used by the codex
// transport when no explicit override is supplied. See package doc for the
// verification rationale.
const Default = "Firefox_135"
