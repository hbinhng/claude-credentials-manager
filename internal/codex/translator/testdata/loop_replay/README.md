# Loop replay fixture

Trimmed/redacted excerpt from a real ccm trace that exhibited the
TaskUpdate loop documented in
`docs/superpowers/specs/2026-05-10-codex-loop-comprehensive-fix-design.md`.

- 25 inbound `/v1/messages` requests + their paired `upstream.resp.event` lines.
- Loop entry point lands inside the window (turn 16 → onward calls only TaskUpdate).
- Redacted: trace already strips Authorization/Cookie at write-time.

The Phase 10 regression test (`loopreg_test.go`) replays this fixture
through `TranslateRequest` and asserts that post-fix translator output
preserves the four invariants Phases 1, 2, 3, and 9 ship.
