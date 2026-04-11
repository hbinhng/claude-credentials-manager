# ccm — Claude Credentials Manager

A single-binary Go CLI for managing multiple Claude OAuth sessions locally. Switch between accounts for Claude Code, share a credential with another machine over a Cloudflare Quick Tunnel without ever copying the token, or launch `claude` against a specific credential without touching the active one.

## Install

### npm

```bash
npm install -g ccm-go
```

### Homebrew (macOS / Linux)

```bash
brew install hbinhng/tap/ccm
```

Installs the `ccm` binary and the `man ccm` manual page.

### Download binary

Grab the latest release for your platform from [Releases](https://github.com/hbinhng/claude-credentials-manager/releases), then:

```bash
chmod +x ccm-*        # Linux / macOS
sudo mv ccm-* /usr/local/bin/ccm
```

### Build from source

```bash
git clone https://github.com/hbinhng/claude-credentials-manager.git
cd claude-credentials-manager
make
```

`make` bakes version/commit/build-date into the binary. A plain `go build` produces a `dev/unknown` build, which signals an untagged local build via `ccm version`.

### Shell completion

```bash
# bash
eval "$(ccm completion bash)"

# zsh
eval "$(ccm completion zsh)"

# fish
ccm completion fish | source

# powershell
ccm completion powershell | Out-String | Invoke-Expression
```

Credential `<id-or-name>` arguments are completed against the stored credentials.

## Commands

### `ccm login`

Authenticate a new Claude account via OAuth (PKCE copy-code flow). Opens your browser, you paste the code back. The new credential is auto-named after the account email (fetched from the profile endpoint); rename it later with `ccm rename` for a shorter handle.

### `ccm backup`

Import an existing unmanaged `~/.claude/.credentials.json` into the ccm store. Safe to run repeatedly — if the file is already managed by ccm, it no-ops with a warning. The imported credential is **not** activated; run `ccm use` to switch to it.

### `ccm status`

List all credentials with status, tier, expiry, and live quota.

```
$ ccm status
ID        NAME      TIER            STATUS  EXPIRES   ACTIVE
a1b2c3d4  personal  Claude Max 20x  valid   in 4 hrs  *
                                            5h: 75% (resets in 2h41m)
                                            7d: 60% (resets in 5d3h)
e5f6a7b8  work      Claude Pro      expired 2 hrs ago
```

Flags:

- `--no-quota` — skip the live quota API call (faster, offline-safe).
- `-o, --output table|json` — pick the output format. `-o json` emits a stable, versioned, minified envelope suitable for scripting.

```bash
# find the currently active credential from a script
ccm status -o json | jq '.credentials[] | select(.active)'
```

The two flags are orthogonal. See `man ccm` for the full JSON schema and field contracts.

### `ccm use <id-or-name>`

Activate a credential for Claude Code. On first use, the original `~/.claude/.credentials.json` is backed up to `~/.claude/bk.credentials.json`. If the token is expired, prompts to refresh before activation.

### `ccm refresh [id-or-name]`

Refresh the OAuth token for a credential. Pass `-a` / `--all` to refresh every stored credential concurrently. If the refreshed credential is currently active, the active credential file is updated as well.

### `ccm rename <id-or-name> <new-name>`

Give a credential a friendly name (1–32 characters, alphanumerics / hyphens / underscores, unique across the store).

### `ccm logout <id-or-name>`

Remove a credential. Pass `-f` / `--force` to skip confirmation when removing the currently active credential.

### `ccm restore`

Undo `ccm use` — remove the symlink (or managed copy on Windows) and restore the original `~/.claude/.credentials.json` from backup.

### `ccm share <id-or-name>`

Expose a credential over a Cloudflare Quick Tunnel so a remote Claude Code install can use it **without the credential ever leaving this machine**. The command runs a local reverse proxy, captures the local install's identity headers with a one-shot `claude -p`, and transitions into serving mode behind a random access token.

```
$ ccm share work
Share session for work (4300c4bc) is live.
  tunnel:  https://<random>.trycloudflare.com
Ticket (give this to the remote side):
  <base64 ticket>
```

On the remote machine, run `ccm launch --via <ticket>` (below). The proxy strips the remote side's keychain `Authorization` header and injects the target credential's real bearer on every forwarded request. Session stays alive until Ctrl-C.

For a LAN-reachable setup that skips the Cloudflare round-trip (typical case: a container on the same host):

```bash
ccm share work --bind-host host.docker.internal --bind-port 8787
```

The listener is bound to the TCP wildcard address and `--bind-host` is placed verbatim into the ticket as the dial target. No tunnel, no `cloudflared` download — but the ticket carries `http://`, so **only use this on a trusted LAN**.

Requires `claude` on `$PATH` for the capture step. If `cloudflared` isn't installed, a pinned version is downloaded to `~/.ccm/bin/` on first use (not needed with `--bind-host`).

### `ccm launch {<id-or-name> | --via <ticket>} [-- claude args...]`

Run Claude Code against a specific credential without mutating `~/.claude/.credentials.json`. Two modes.

**Local mode** — use a stored credential via a loopback passthrough proxy:

```bash
ccm launch work -- -p 'hi'
```

The proxy refreshes the token up front if it's expired or expiring soon, then execs `claude` with `ANTHROPIC_BASE_URL` pointing at itself. Lets you run multiple `claude` sessions against different ccm-managed credentials simultaneously without calling `ccm use`. Requires `ccm use` to have been run at least once previously (the child `claude` still reads its keychain bearer; the proxy overwrites it in flight).

**Remote mode** — decode a ticket from `ccm share` and launch against the remote proxy:

```bash
ccm launch --via <ticket> -- -p 'hi'
```

Does not require any ccm-managed credential on the local machine — the bearer comes from the ticket.

In both modes, any arguments after `--` are passed to `claude` verbatim.

### `ccm version`

Print the ccm version, short git commit, and build timestamp. Same as `ccm --version`. Release binaries built via `make dist` embed this metadata at link time; plain `go build` reports `dev/unknown` placeholders.

## Credential resolution

All commands accepting `<id-or-name>` resolve the argument in order:

1. Exact UUID match
2. Exact name match
3. UUID prefix match (minimum 4 characters); an ambiguous prefix errors out with the list of matches

## Environment

- **`CCM_PROXY`** — route all outbound ccm traffic through an HTTP(S) or SOCKS5 proxy (e.g. `socks5://user:pass@proxy.example:1080`). Applies to `ccm login`, `ccm refresh`, `ccm status`, `ccm backup`, and the reverse-proxy forwarding of `ccm share` and `ccm launch`. `ccm use` is deliberately excluded — activation is local, and the opportunistic token refresh it does on expired credentials runs direct. The stdlib `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` variables are **not** consulted; only `CCM_PROXY` is respected. Does not affect `cloudflared`'s own connections — set `HTTPS_PROXY` in the shell for those.
- **`CCM_SHARE_DEBUG=1`** — log captured identity headers and forwarded upstream requests in `ccm share` to stderr.
- **`CCM_LAUNCH_DEBUG=1`** — log forwarded upstream requests in `ccm launch <id-or-name>` local mode to stderr.

## How it works

- Credentials are stored in `~/.ccm/<uuid>.credentials.json` with permission `0600`. Each file holds the OAuth tokens, the cached subscription tier, and timestamps for a single Claude account.
- **Unix / macOS:** `~/.claude/.credentials.json` is an absolute symlink pointing directly at `~/.ccm/<uuid>.credentials.json`. No intermediate copy — Claude Code always reads whatever ccm has written through the symlink.
- **Windows:** symlinks are unreliable, so a wrapper JSON file carrying a `ccmSourceId` marker is copied to `~/.claude/.credentials.json` instead. `ccm` rewrites this copy automatically when the underlying credential changes (refresh, rename).
- The original `~/.claude/.credentials.json` is backed up to `~/.claude/bk.credentials.json` on the first `ccm use` and never overwritten; `ccm restore` puts it back.

For every command, flag, and environment variable in exhaustive detail, see `man ccm`.

## License

MIT
