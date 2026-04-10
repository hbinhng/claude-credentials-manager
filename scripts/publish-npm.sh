#!/usr/bin/env bash
# Stage and publish ccm-go and its six per-platform sub-packages.
#
# Usage:
#   scripts/publish-npm.sh VERSION [--dry-run]
#
# Expects prebuilt binaries in dist/:
#   dist/ccm-darwin-amd64
#   dist/ccm-darwin-arm64
#   dist/ccm-linux-amd64
#   dist/ccm-linux-arm64
#   dist/ccm-windows-amd64.exe
#   dist/ccm-windows-arm64.exe
#
# Requires an authenticated npm client (either `npm login` or an
# NPM_AUTH_TOKEN passed inline by the caller).
#
# Sub-packages are published BEFORE the main package — the main package's
# optionalDependencies reference exact sub-package versions that must exist
# on the registry first.

set -euo pipefail

VERSION="${1:-}"
DRY_RUN="${2:-}"

if [ -z "$VERSION" ]; then
  echo "usage: $0 VERSION [--dry-run]" >&2
  exit 1
fi

if [ -n "$DRY_RUN" ] && [ "$DRY_RUN" != "--dry-run" ]; then
  echo "second argument must be --dry-run (got: $DRY_RUN)" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NPM_DIR="$ROOT/npm"
PLATFORMS_DIR="$NPM_DIR/platforms"

main_pkg="$NPM_DIR/package.json"
current_version="$(node -p "require('$main_pkg').version")"
if [ "$current_version" != "$VERSION" ]; then
  echo "error: $main_pkg version is $current_version, expected $VERSION" >&2
  echo "       bump npm/package.json (including optionalDependencies) before publishing" >&2
  exit 1
fi

stage() {
  local key="$1" goos="$2" goarch="$3" nodeos="$4" nodearch="$5" ext="${6:-}"
  local src="$ROOT/dist/ccm-${goos}-${goarch}${ext}"
  if [ ! -f "$src" ]; then
    echo "error: missing $src — run the cross-compile block in CLAUDE.md first" >&2
    exit 1
  fi

  local dest="$PLATFORMS_DIR/$key"
  mkdir -p "$dest/bin"
  cp "$src" "$dest/bin/ccm${ext}"
  chmod +x "$dest/bin/ccm${ext}"

  cat > "$dest/package.json" <<JSON
{
  "name": "ccm-go.${key}",
  "version": "${VERSION}",
  "description": "${key} prebuilt binary for ccm-go (Claude Credentials Manager)",
  "os": ["${nodeos}"],
  "cpu": ["${nodearch}"],
  "license": "MIT",
  "repository": {
    "type": "git",
    "url": "git+https://github.com/hbinhng/claude-credentials-manager.git"
  },
  "homepage": "https://github.com/hbinhng/claude-credentials-manager"
}
JSON

  echo "    staged ccm-go.${key}"
}

publish_dir() {
  local dir="$1"
  if [ -n "$DRY_RUN" ]; then
    (cd "$dir" && npm publish --access public --dry-run)
  else
    (cd "$dir" && npm publish --access public)
  fi
}

echo "==> Staging sub-packages in $PLATFORMS_DIR"
rm -rf "$PLATFORMS_DIR"
mkdir -p "$PLATFORMS_DIR"
stage darwin-x64    darwin  amd64  darwin  x64
stage darwin-arm64  darwin  arm64  darwin  arm64
stage linux-x64     linux   amd64  linux   x64
stage linux-arm64   linux   arm64  linux   arm64
stage win32-x64     windows amd64  win32   x64    .exe
stage win32-arm64   windows arm64  win32   arm64  .exe

echo "==> Publishing sub-packages"
for key in darwin-x64 darwin-arm64 linux-x64 linux-arm64 win32-x64 win32-arm64; do
  publish_dir "$PLATFORMS_DIR/$key"
done

echo "==> Publishing main package"
publish_dir "$NPM_DIR"

echo "==> Done — ccm-go@$VERSION"
