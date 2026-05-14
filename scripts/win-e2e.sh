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

step "verify all installed PS profiles have the rc-block sentinel"
checked_count=0
for ps_bin in pwsh powershell.exe; do
	# Check existence via the calling shell, not the target binary.
	exists="$(ssh "$HOST" "powershell -NoProfile -Command \"if (Get-Command $ps_bin -ErrorAction SilentlyContinue) { 'yes' } else { 'no' }\"" | tr -d '\r')"
	if [[ "$exists" != "yes" ]]; then
		echo "  $ps_bin not installed; skipping"
		continue
	fi
	# Ask the target binary for its own $PROFILE.
	profile_path="$(ssh "$HOST" "$ps_bin -NoProfile -Command \"\$PROFILE\"" | tr -d '\r')"
	if [[ -z "$profile_path" ]]; then
		fail "$ps_bin returned empty \$PROFILE"
	fi
	echo "  checking $ps_bin profile: $profile_path"
	body="$(ssh "$HOST" "powershell -NoProfile -Command \"if (Test-Path '$profile_path') { Get-Content '$profile_path' -Raw } else { '' }\"")"
	if ! echo "$body" | grep -q "ccm-aliases:begin"; then
		fail "$ps_bin profile ($profile_path) missing rc-block sentinel"
	fi
	checked_count=$((checked_count + 1))
done
if [[ $checked_count -eq 0 ]]; then
	fail "no PS hosts checked — both pwsh and powershell.exe reported missing"
fi
echo "  all $checked_count installed PS profile(s) have the sentinel"

step "verify the function loads in every installed PS host"
for ps_bin in pwsh powershell.exe; do
	exists="$(ssh "$HOST" "powershell -NoProfile -Command \"if (Get-Command $ps_bin -ErrorAction SilentlyContinue) { 'yes' } else { 'no' }\"")"
	exists="$(echo "$exists" | tr -d '\r')"
	if [[ "$exists" != "yes" ]]; then
		echo "  $ps_bin not installed; skipping"
		continue
	fi
	defn="$(ssh "$HOST" "$ps_bin -Command \"(Get-Command $ALIAS_NAME -CommandType Function -ErrorAction Stop).Definition\"")"
	echo "  $ps_bin function definition: $defn"
	if ! echo "$defn" | grep -q "ccm launch"; then
		fail "$ps_bin: function body doesn't call ccm launch"
	fi
	if ! echo "$defn" | grep -q "$FAKE_CRED"; then
		fail "$ps_bin: function body doesn't carry captured cred"
	fi
done
echo "  all installed PS hosts define the function"

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
