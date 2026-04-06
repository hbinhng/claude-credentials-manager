# ccm — Claude Credentials Manager

Manage multiple Claude OAuth sessions locally with quick switching between accounts for [Claude Code](https://docs.anthropic.com/en/docs/claude-code).

## Install

```bash
npm install -g ccm-go
```

## Usage

```bash
# Authenticate a new Claude account
ccm login

# List all credentials with live usage quota
ccm status

# Switch active credential for Claude Code
ccm use <id-or-name>

# Refresh an expired token
ccm refresh <id-or-name>

# Give a credential a friendly name
ccm rename <id-or-name> <new-name>

# Remove a credential
ccm logout <id-or-name>

# Restore original Claude Code credentials
ccm restore
```

### Example

```
$ ccm status
ID        NAME      STATUS  EXPIRES      USAGE            ACTIVE
a1b2c3d4  personal  valid   in 47 min    5h:75% 7d:90%    *
e5f6a7b8  work      expired 2h ago       -
```

## How it works

- Credentials are stored in `~/.ccm/` as individual JSON files
- `ccm use` backs up your original `~/.claude/.credentials.json` and activates the selected account
- `ccm restore` undoes the switch and restores your original credentials
- All commands accept a full UUID, a 4+ char prefix, or a name

## Platforms

| OS | Arch |
|----|------|
| macOS | x64, ARM64 (Apple Silicon) |
| Linux | x64, ARM64 |
| Windows | x64, ARM64 (Qualcomm) |

## Also available via

- **Homebrew**: `brew install hbinhng/tap/ccm`
- **Go**: `go install github.com/hbinhng/claude-credentials-manager@latest`
- **Binary**: [GitHub Releases](https://github.com/hbinhng/claude-credentials-manager/releases)

## License

MIT — [GitHub](https://github.com/hbinhng/claude-credentials-manager)
