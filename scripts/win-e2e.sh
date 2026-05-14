#!/usr/bin/env bash
# Windows e2e for `ccm alias`.
#
# Validates the PowerShell-specific mechanics that Linux tests cannot cover:
#   - cross-compiled Windows binary runs
#   - `ccm alias --shells pwsh` writes aliases.ps1 and inserts the
#     sentinel block into the PowerShell profile
#   - pwshResolver in detect_windows.go resolves $PROFILE correctly
#   - a fresh PowerShell session (with profile loaded) defines the
#     alias function
#   - `ccm alias --remove` cleans up
#
# Does NOT verify the alias actually launches claude — that requires
# a credential the test box doesn't have. The launch flow is covered
# by Linux integration tests under internal/shellalias/.
#
# Requires:
#   - passwordless SSH to 192.168.1.11 (PowerShell 5.1; ccm pre-installed
#     via npm; see reference-windows-e2e-box memory).
#
# Usage:
#   make win-e2e
#   # or:
#   bash scripts/win-e2e.sh

set -euo pipefail

HOST="${HOST:-192.168.1.11}"
ALIAS_NAME="${ALIAS_NAME:-ccm-test-cld}"
# Remote install path of the ccm.exe shipped by the npm package.
# The Node shim (ccm.ps1 → bin/ccm) resolves the platform binary from the
# ccm-go.win32-x64 optional-dependency sub-package.
REMOTE_BIN_DEFAULT='C:\Users\hbinhng\AppData\Roaming\npm\node_modules\ccm-go\node_modules\ccm-go.win32-x64\bin\ccm.exe'
REMOTE_BIN="${REMOTE_BIN:-$REMOTE_BIN_DEFAULT}"

# Use a unique fake-cred name in the captured payload. ccm alias doesn't
# validate that the cred exists — it just captures the arg verbatim —
# so this works without a real credential. We only care that the alias
# function expands to the right ccm launch invocation.
FAKE_CRED="${FAKE_CRED:-ccm-test-fake-cred}"

step() { echo; echo "== $* =="; }
fail() { echo "FAIL: $*" >&2; exit 1; }

step "build windows/amd64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o /tmp/ccm.exe .

step "ship to $HOST"
# On Windows SSH hosts, scp requires the Windows drive-letter path form.
# $env:TEMP expands to C:\Users\hbinhng\AppData\Local\Temp on this box.
WIN_TEMP='C:/Users/hbinhng/AppData/Local/Temp'
scp -q /tmp/ccm.exe "$HOST:$WIN_TEMP/ccm.exe"
# Kill any running ccm process first to avoid file-lock on the binary.
ssh "$HOST" "powershell -NoProfile -Command \"Get-Process ccm -ErrorAction SilentlyContinue | Stop-Process -Force\"" >/dev/null 2>&1 || true
# Overwrite the npm-shipped binary so `ccm` on the remote resolves to
# our test build for the duration of the run.
ssh "$HOST" "powershell -NoProfile -Command \"Move-Item -Force \$env:TEMP\\ccm.exe '$REMOTE_BIN'\""

step "verify remote ccm runs"
ver="$(ssh "$HOST" 'powershell -NoProfile -Command "ccm version"' 2>&1 || true)"
echo "  $ver"
if [[ -z "$ver" ]]; then
	fail "ccm version returned empty"
fi

step "clean any leftover alias from prior runs"
ssh "$HOST" "powershell -NoProfile -Command \"\$r = (ccm alias --remove $ALIAS_NAME 2>&1); if (\$LASTEXITCODE -ne 0) { exit 0 }\"" >/dev/null 2>&1 || true

# Detect which PowerShell binary resolvePwshProfile (detect_windows.go) prefers:
# it tries "pwsh" first (PS 7+), then falls back to "powershell.exe" (PS 5.1).
# We need the same binary so we read the same $PROFILE path and load the same rc.
step "detect PowerShell binary used by pwshResolver"
psh_bin="$(ssh "$HOST" "powershell -NoProfile -Command \"if (Get-Command pwsh -ErrorAction SilentlyContinue) { 'pwsh' } else { 'powershell' }\"")"
psh_bin="${psh_bin%%$'\r'}"  # strip CR on Windows output
echo "  pwshResolver will use: $psh_bin"

# Resolve $PROFILE from the perspective of the chosen binary — this is what
# ccm's pwshResolver does on the remote host.
profile_path="$(ssh "$HOST" "powershell -NoProfile -Command \"$psh_bin -NoProfile -Command '\$PROFILE'\"")"
profile_path="${profile_path%%$'\r'}"
echo "  profile path: $profile_path"

step "install alias --shells pwsh"
ssh "$HOST" "powershell -NoProfile -Command \"ccm alias --as $ALIAS_NAME --shells pwsh --load-balance $FAKE_CRED\""

step "verify aliases.ps1 contains the per-alias sentinel block"
alias_file_content="$(ssh "$HOST" "powershell -NoProfile -Command \"Get-Content \$env:USERPROFILE\\.ccm\\aliases.ps1 -Raw\"")"
if ! echo "$alias_file_content" | grep -q "ccm-alias:begin:$ALIAS_NAME"; then
	fail "aliases.ps1 missing per-alias begin sentinel"
fi
if ! echo "$alias_file_content" | grep -q "ccm-alias:end:$ALIAS_NAME"; then
	fail "aliases.ps1 missing per-alias end sentinel"
fi
if ! echo "$alias_file_content" | grep -q "$FAKE_CRED"; then
	fail "aliases.ps1 doesn't carry the captured payload"
fi
echo "  alias file ok"

step "verify PowerShell profile has rc-block sentinel"
# Read the profile that pwshResolver actually wrote to (may be PS7 profile
# if pwsh is installed — different from the PS5.1 $PROFILE path).
profile_content="$(ssh "$HOST" "powershell -NoProfile -Command \"Get-Content '$profile_path' -Raw\"")"
if ! echo "$profile_content" | grep -q "ccm-aliases:begin"; then
	fail "profile missing rc-block begin sentinel"
fi
echo "  profile sentinel ok"

step "verify fresh PowerShell session (profile loaded) defines the function"
# NOTE: -NoProfile is OMITTED so the ccm-aliases block in the profile runs.
# We use the same binary that wrote the profile so it loads the right rc file.
# .Definition returns the function body (contents inside the braces).
defn="$(ssh "$HOST" "$psh_bin -Command \"(Get-Command $ALIAS_NAME -CommandType Function -ErrorAction Stop).Definition\"")"
echo "  function definition: $defn"
if ! echo "$defn" | grep -q "ccm launch"; then
	fail "function body doesn't call ccm launch"
fi
if ! echo "$defn" | grep -q -- "--load-balance"; then
	fail "function body doesn't carry --load-balance"
fi
if ! echo "$defn" | grep -q "$FAKE_CRED"; then
	fail "function body doesn't carry captured cred"
fi
if ! echo "$defn" | grep -q "@args"; then
	fail "function body doesn't append @args"
fi
echo "  function body ok"

step "verify ccm alias --list reports the alias"
list_out="$(ssh "$HOST" "powershell -NoProfile -Command \"ccm alias --list\"")"
echo "$list_out" | sed 's/^/  /'
if ! echo "$list_out" | grep -q "$ALIAS_NAME"; then
	fail "ccm alias --list didn't show $ALIAS_NAME"
fi

step "remove alias"
ssh "$HOST" "powershell -NoProfile -Command \"ccm alias --remove $ALIAS_NAME\""

step "verify cleanup: aliases.ps1 no longer has the sentinel"
after_remove="$(ssh "$HOST" "powershell -NoProfile -Command \"if (Test-Path \$env:USERPROFILE\\.ccm\\aliases.ps1) { Get-Content \$env:USERPROFILE\\.ccm\\aliases.ps1 -Raw } else { '' }\"")"
if echo "$after_remove" | grep -q "ccm-alias:begin:$ALIAS_NAME"; then
	fail "alias block still present after --remove"
fi

step "verify ccm alias --list no longer reports the alias"
list_after="$(ssh "$HOST" "powershell -NoProfile -Command \"ccm alias --list\"")"
if echo "$list_after" | grep -q "$ALIAS_NAME"; then
	fail "ccm alias --list still shows $ALIAS_NAME after remove"
fi

echo
echo "== PASS =="
