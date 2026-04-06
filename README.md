# ccm — Claude Credentials Manager

A single-binary Go CLI for managing multiple Claude OAuth sessions locally, with quick switching between accounts for Claude Code.

## Install

### npm

```bash
npm install -g claude-credentials-manager
```

### Homebrew (macOS / Linux)

```bash
brew install hbinhng/tap/ccm
```

This installs the `ccm` binary and the `man ccm` manual page.

### Download binary

Grab the latest release for your platform from [Releases](https://github.com/hbinhng/claude-credentials-manager/releases), then:

```bash
chmod +x ccm-*        # Linux / macOS
sudo mv ccm-* /usr/local/bin/ccm
```

### Go install

```bash
go install github.com/hbinhng/claude-credentials-manager@latest
```

### Build from source

```bash
git clone https://github.com/hbinhng/claude-credentials-manager.git
cd claude-credentials-manager
go build -o ccm .
```

### Shell completion

```bash
# zsh (add to ~/.zshrc)
eval "$(ccm completion zsh)"

# bash (add to ~/.bashrc)
eval "$(ccm completion bash)"

# fish
ccm completion fish | source
```

## Commands

### `ccm login`

Authenticate a new Claude account via OAuth (PKCE copy-code flow). Opens your browser, you paste the code back.

```
$ ccm login
Open this URL in your browser to authenticate:

  https://claude.ai/oauth/authorize?client_id=...

Paste the code here: xxxxxxxx

Logged in as a1b2c3d4-...
Use `ccm rename a1b2c3d4 <name>` to set a friendly name.
```

### `ccm status`

List all credentials with token status and usage quota.

```
$ ccm status
ID        NAME      STATUS  EXPIRES      USAGE            ACTIVE
a1b2c3d4  personal  valid   in 47 min    5h:75% 7d:90%    *
e5f6a7b8  work      expired 2h ago       -
```

### `ccm use <id-or-name>`

Activate a credential for Claude Code. Backs up original `~/.claude/.credentials.json` on first use, then symlinks in the selected account.

```
$ ccm use personal
Now using 'personal' (a1b2c3d4)
```

### `ccm refresh <id-or-name>`

Refresh the OAuth token for a credential. Automatically updates the active credential if applicable.

### `ccm rename <id-or-name> <new-name>`

Give a credential a friendly name (used in `status`, `use`, `refresh`, etc.).

```
$ ccm rename a1b2c3d4 personal
Renamed a1b2c3d4-... -> personal (a1b2c3d4)
```

### `ccm logout <id-or-name>`

Remove a credential. Use `--force` to skip confirmation if the credential is active.

### `ccm restore`

Undo `ccm use` — removes the symlink and restores the original `~/.claude/.credentials.json` from backup.

## How it works

- Credentials are stored in `~/.ccm/<uuid>.credentials.json`
- `ccm use` writes the selected credential to `~/.claude/ccm.credentials.json` and symlinks `~/.claude/.credentials.json` to it
- The original credentials are backed up to `~/.claude/bk.credentials.json` (once, never overwritten)
- `ccm restore` reverses the symlink and restores the backup
- All commands accept a full UUID, a 4+ character UUID prefix, or a name

## Usage quota

`ccm status` fetches live quota from the Anthropic OAuth usage API, showing remaining percentage for the 5-hour and 7-day windows (including per-model breakdowns when available).

## License

MIT
