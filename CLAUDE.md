# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

The `Makefile` is the canonical build entrypoint — always prefer it over raw `go build` so every binary is built with the same flags.

```bash
make              # build ccm for the host platform
make test         # run all tests
make dist         # cross-compile all six release binaries into dist/
make clean        # remove ccm and dist/
```

For running a subset of tests, fall back to `go test` directly:
```bash
go test ./internal/claude/...           # single package
go test ./internal/store/ -run TestSave  # single test by name
```

All Make targets build with `CGO_ENABLED=0` and `-trimpath -ldflags="-s -w"`. Every binary is fully static (no libc dependency) and stripped, so Linux builds run on glibc 2.23+ (Ubuntu 16+), Alpine/musl, and anything else with a modern kernel. If you ever need to add a dependency that pulls in cgo, think twice — it will break the Ubuntu 16+ guarantee.

## Release Process

Three distribution channels, all manual:

1. **GitHub Release** — tag `vX.Y.Z`, attach the six binaries + `ccm.1` man page
2. **npm** — bump `npm/package.json` version, `cd npm && npm publish` (the postinstall script downloads binaries from the GitHub release)
3. **Homebrew** — update `Formula/ccm.rb` in `hbinhng/homebrew-tap` with new version, URLs, and SHA256 hashes

Version is tracked only in `npm/package.json`. There is no Go-level version constant.

## Architecture

Single-binary Go CLI (Cobra) for managing multiple Claude OAuth credentials locally.

```
main.go → cmd.Execute()
cmd/           Cobra commands (login, use, status, refresh, rename, logout, restore, completion)
internal/
  store/       Credential CRUD — each credential is ~/.ccm/{uuid}.credentials.json
  oauth/       OAuth 2.0 PKCE flow (copy-code, no local server), token refresh, usage quota API
  claude/      Activates a credential for Claude Code by managing ~/.claude/.credentials.json
```

**Credential activation (Unix):** `.credentials.json` is an absolute symlink pointing directly into the store (`~/.ccm/{id}.credentials.json`). No intermediate copy — `store.Save()` updates the file Claude Code reads through the symlink. Backup of original credentials goes to `~/.claude/bk.credentials.json`.

**Credential activation (Windows):** No symlinks. A wrapper JSON with `ccmSourceId` marker is copied to `.credentials.json`. `WriteActive()` must be called after store updates to sync the copy.

**Credential resolution** (`store.Resolve`): accepts full UUID, UUID prefix (min 4 chars), or credential name.

## Test Patterns

Tests override `HOME`/`USERPROFILE` env vars to use `t.TempDir()` as a fake home directory. The `setupFakeHome` helper creates both `~/.claude/` and `~/.ccm/` directories. OAuth tests use `httptest` servers.
