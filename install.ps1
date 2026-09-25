# Pygo installer for Windows.
#
#   irm https://raw.githubusercontent.com/marcodc74/pygo/master/install.ps1 | iex
#
# Downloads the pygo binary for this machine (x64 or ARM64) from the GitHub
# release, verifies it against SHA256SUMS, installs it in
# %LOCALAPPDATA%\Programs\pygo and adds that folder to the user PATH.
# No administrator rights are needed.
#
# Options (environment variables, because `irm | iex` cannot take parameters):
#   $env:PYGO_VERSION = "v0.1.0"   # a specific release (default: latest)
#   $env:PYGO_DIR     = "C:\tools\pygo"   # install folder
#   $env:PYGO_BASE_URL = "https://mirror.example/pygo/v0.1.0"   # mirror with the release files

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # Invoke-WebRequest is much faster without the progress bar

# `throw`, not `exit`: under `irm | iex`, exit would close the user's terminal.
function Fail($msg) {
    throw "pygo install: $msg"
}

# Windows PowerShell 5.1 may default to TLS 1.0, which GitHub rejects.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$repo = "marcodc74/pygo"
$version = if ($env:PYGO_VERSION) { $env:PYGO_VERSION } else { "latest" }
$installDir = if ($env:PYGO_DIR) { $env:PYGO_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\pygo" }

# Architecture of the OS (not of the PowerShell process: x64 PowerShell can run emulated on ARM64).
$arch = $null
try {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
} catch {
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
}
switch -Regex ($arch) {
    "^(X64|AMD64)$" { $asset = "pygo-windows-amd64.exe" }
    "^(Arm64|ARM64)$" { $asset = "pygo-windows-arm64.exe" }
    default { Fail "unsupported architecture '$arch' (supported: x64, ARM64)" }
}

$base = if ($env:PYGO_BASE_URL) {
    $env:PYGO_BASE_URL.TrimEnd("/")
} elseif ($version -eq "latest") {
    "https://github.com/$repo/releases/latest/download"
} else {
    "https://github.com/$repo/releases/download/$version"
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("pygo-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading $asset ($version)..."
    $exe = Join-Path $tmp $asset
    $sums = Join-Path $tmp "SHA256SUMS"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $exe
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums
    } catch {
        Fail "download failed from $base ($($_.Exception.Message))"
    }

    # Verify the checksum published with the release.
    $line = Get-Content $sums | Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { Fail "no checksum for $asset in SHA256SUMS" }
    $expected = ($line -split "\s+")[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -Path $exe).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { Fail "checksum mismatch for $asset (expected $expected, got $actual)" }
    Write-Host "Checksum OK ($actual)"

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $target = Join-Path $installDir "pygo.exe"
    Copy-Item -Path $exe -Destination $target -Force
    # Remove the "downloaded from the Internet" mark, if any, so SmartScreen does not prompt.
    if ($env:OS -eq "Windows_NT") {
        try { Unblock-File -Path $target } catch { }
    }
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# Add the install folder to the user PATH (persistent) and to this session.
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$entries = @($userPath -split ";" | Where-Object { $_ })
if ($entries -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable("Path", (($entries + $installDir) -join ";"), "User")
    Write-Host "Added $installDir to the user PATH (open a new terminal to use it everywhere)."
}
if (($env:Path -split ";") -notcontains $installDir) {
    $env:Path = "$env:Path;$installDir"
}

Write-Host ""
& $target version
Write-Host "Installed: $target" -ForegroundColor Green
Write-Host "Try:  pygo run -e 'print(6 * 7)'"
