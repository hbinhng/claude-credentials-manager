#Requires -Version 5.1
<#
.SYNOPSIS
    Install ccm (Claude Credentials Manager) for the current Windows user.

.DESCRIPTION
    Downloads the latest ccm release binary for this CPU architecture,
    places it at %LOCALAPPDATA%\Programs\ccm\ccm.exe, and idempotently
    adds that directory to the user PATH so ccm is on $env:Path in new
    shells. No admin / elevation required - every action is per-user.

.PARAMETER Version
    Optional release tag to pin (e.g. v1.21.1). Defaults to the latest
    release published on GitHub.

.PARAMETER InstallDir
    Override the install directory. Defaults to %LOCALAPPDATA%\Programs\ccm.

.EXAMPLE
    iwr https://raw.githubusercontent.com/hbinhng/claude-credentials-manager/main/scripts/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Version v1.21.1
#>

[CmdletBinding()]
param(
    [string]$Version,
    [string]$InstallDir
)

$ErrorActionPreference = 'Stop'

# GitHub releases require TLS 1.2; Windows PowerShell 5.1 defaults to
# TLS 1.0 on older Windows builds. OR the existing value so we do not
# downgrade something stricter the user already set.
[Net.ServicePointManager]::SecurityProtocol = `
    [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo = 'hbinhng/claude-credentials-manager'

if (-not $InstallDir -or $InstallDir.Trim() -eq '') {
    if (-not $env:LOCALAPPDATA) {
        throw 'LOCALAPPDATA is not set; pass -InstallDir <path> explicitly.'
    }
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\ccm'
}

function Get-CcmArch {
    # When the script is hosted in a 32-bit PowerShell on a 64-bit OS,
    # PROCESSOR_ARCHITECTURE reports x86 and the real host arch lives in
    # PROCESSOR_ARCHITEW6432. Prefer the latter when set.
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
    switch ($arch) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "Unsupported CPU architecture: $arch (need AMD64 or ARM64)" }
    }
}

function Get-LatestVersion {
    $headers = @{ 'User-Agent' = 'ccm-installer' }
    $resp = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
        -Headers $headers
    if (-not $resp.tag_name) {
        throw "GitHub API returned no tag_name for $Repo/releases/latest"
    }
    return [string]$resp.tag_name
}

function Add-UserPathEntry([string]$entry) {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $current) { $current = '' }
    $parts = $current -split ';' | Where-Object { $_ -ne '' }

    $entryFull = [IO.Path]::GetFullPath($entry)
    foreach ($p in $parts) {
        try {
            if ([IO.Path]::GetFullPath($p) -ieq $entryFull) { return $false }
        } catch {
            # Malformed existing entry - skip the equality check rather
            # than die on someone else's broken PATH.
        }
    }
    $parts += $entry
    [Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')

    # Best-effort: broadcast WM_SETTINGCHANGE so any process listening
    # (Explorer, new Terminal tabs) re-reads Environment. Failure is
    # non-fatal - new shells inherit the registry edit either way.
    try {
        if (-not ('Win32.NativeMethods' -as [type])) {
            Add-Type -Namespace Win32 -Name NativeMethods -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError=true, CharSet=System.Runtime.InteropServices.CharSet.Auto)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
        }
        $HWND_BROADCAST = [IntPtr]0xffff
        $WM_SETTINGCHANGE = 0x1A
        [UIntPtr]$result = [UIntPtr]::Zero
        [void][Win32.NativeMethods]::SendMessageTimeout(
            $HWND_BROADCAST, $WM_SETTINGCHANGE, [UIntPtr]::Zero, 'Environment',
            2, 1000, [ref]$result)
    } catch { }

    return $true
}

$arch = Get-CcmArch
if (-not $Version -or $Version.Trim() -eq '') {
    Write-Host 'Looking up latest ccm release...'
    $Version = Get-LatestVersion
}

$asset = "ccm-windows-$arch.exe"
$url = "https://github.com/$Repo/releases/download/$Version/$asset"
$target = Join-Path $InstallDir 'ccm.exe'

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

Write-Host "Downloading $asset ($Version) -> $target"
$tmp = [IO.Path]::GetTempFileName()
try {
    Invoke-WebRequest -Uri $url -OutFile $tmp `
        -Headers @{ 'User-Agent' = 'ccm-installer' }
    Move-Item -Force -Path $tmp -Destination $target
} catch {
    if (Test-Path $tmp) { Remove-Item -Force $tmp -ErrorAction SilentlyContinue }
    if ($_.Exception.Message -match 'used by another process') {
        throw "Cannot replace $target - another process (likely 'ccm serve') has it open. Stop it and re-run."
    }
    throw
}

$added = Add-UserPathEntry $InstallDir
Write-Host ''
Write-Host "ccm installed: $target"
if ($added) {
    Write-Host "Added $InstallDir to your user PATH."
    Write-Host 'Open a new terminal so the PATH update takes effect.'
} else {
    Write-Host "$InstallDir already on your user PATH."
}

# Try printing the version from the absolute path. The CURRENT shell's
# $env:Path is a snapshot taken at launch - even after we register the
# new directory, ccm.exe is not reachable by bare name here until the
# user opens a new shell. Calling by absolute path sidesteps that.
Write-Host ''
try {
    & $target version
} catch {
    Write-Host '(could not run ccm version; open a new shell and try `ccm version`)'
}
