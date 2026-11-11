<#
.SYNOPSIS
  Installs the ccqc binary for the current user (no administrator rights).

.DESCRIPTION
  From a cc-quant-chat checkout with Go installed it builds from source; otherwise it
  downloads the release binary and verifies its SHA-256. Installs into
  %LOCALAPPDATA%\Programs\ccqc and adds that folder to the user PATH.
  Chat data (%APPDATA%\ccqc) is never touched.

  Reinstalling over a running broker is safe: the locked ccqc.exe is renamed to
  ccqc.exe.old, the new one takes its place, and the running broker is restarted on the
  new version (connected sessions reconnect by themselves). -NoRestart leaves it running.

.PARAMETER Version
  Release tag to download (default: latest).
.PARAMETER Dir
  Install directory (default: %LOCALAPPDATA%\Programs\ccqc).
.PARAMETER FromSource
  Build from this checkout (needs Go).
.PARAMETER FromRelease
  Download the release binary even inside a checkout.
.PARAMETER NoPath
  Don't touch the user PATH.
.PARAMETER NoRestart
  Don't restart a running broker after the update.
.PARAMETER Uninstall
  Stop the broker, remove the binary and the PATH entry; chat data is kept.

.EXAMPLE
  .\install.ps1
  .\install.ps1 -Version v0.2.0
  .\install.ps1 -Uninstall
  & ([scriptblock]::Create((irm https://raw.githubusercontent.com/PHPCraftdream/cc-quant-chat/main/install.ps1)))
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$Dir = (Join-Path $env:LOCALAPPDATA 'Programs\ccqc'),
    [switch]$FromSource,
    [switch]$FromRelease,
    [switch]$NoPath,
    [switch]$NoRestart,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'PHPCraftdream/cc-quant-chat'
$Exe = Join-Path $Dir 'ccqc.exe'

function Say([string]$Message) { Write-Host "ccqc-install: $Message" }

# The user PATH is read and written raw, so entries like %USERPROFILE%\bin stay unexpanded.
function Get-UserPath {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    try {
        return [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    } finally { $key.Close() }
}

function Set-UserPath([string]$Value) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $key.SetValue('Path', $Value, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    } finally { $key.Close() }
    # Tell running programs (Explorer, new terminals) that the environment changed.
    if (-not ('Ccqc.Env' -as [type])) {
        Add-Type -Namespace Ccqc -Name Env -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint msg, UIntPtr wParam, string lParam,
    uint flags, uint timeout, out UIntPtr result);
'@
    }
    $result = [UIntPtr]::Zero
    [void][Ccqc.Env]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
}

function Test-PathEntry([string]$PathValue, [string]$Entry) {
    $want = $Entry.TrimEnd('\')
    return @($PathValue -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ieq $want }).Count -gt 0
}

if ($Uninstall) {
    if (Test-Path $Exe) {
        # Only binaries that know `broker stop` get it: an old one would start a broker instead.
        $help = & { $ErrorActionPreference = 'Continue'; & $Exe help 2>&1 | Out-String }
        if ($help -match 'broker stop') {
            & { $ErrorActionPreference = 'Continue'; & $Exe broker stop *> $null }
            Start-Sleep -Milliseconds 300
        }
        try {
            Remove-Item $Exe -Force
        } catch {
            # A running ccqc (session or broker) locks the exe; renaming still works.
            Move-Item $Exe "$Exe.old" -Force
            Say 'ccqc.exe is in use (a Claude Code session or the broker); renamed to ccqc.exe.old'
        }
        Say "removed $Exe"
    } else {
        Say "nothing installed at $Exe"
    }
    Remove-Item "$Exe.old" -Force -ErrorAction SilentlyContinue
    if (Test-Path "$Exe.old") {
        Say "$Exe.old is still used by a running ccqc (a Claude Code session); run -Uninstall again after those sessions end"
    } elseif ((Test-Path $Dir) -and -not (Get-ChildItem $Dir -Force)) {
        Remove-Item $Dir -Force
    }
    if (-not $NoPath) {
        $userPath = Get-UserPath
        $kept = @($userPath -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ine $Dir.TrimEnd('\') }) -join ';'
        if ($kept -ne $userPath) {
            Set-UserPath $kept
            Say "removed $Dir from the user PATH"
        }
    }
    Say "chat data is kept in $(Join-Path $env:APPDATA 'ccqc') (database, folder settings, nick keys)"
    return
}

$src = $PSScriptRoot
$goMod = if ($src) { Join-Path $src 'go.mod' } else { '' }
$isCheckout = [bool]($src -and (Test-Path (Join-Path $src 'cmd\ccqc')) -and (Test-Path $goMod) -and
    (Select-String -Path $goMod -Pattern "^module github.com/$Repo$" -Quiet))
$haveGo = [bool](Get-Command go -ErrorAction SilentlyContinue)

$mode = if ($FromSource) { 'source' }
elseif ($FromRelease) { 'release' }
elseif ($isCheckout -and $haveGo) { 'source' }
else { 'release' }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('ccqc-install-' + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
    $new = Join-Path $tmp 'ccqc.exe'
    if ($mode -eq 'source') {
        if (-not $isCheckout) { throw '-FromSource needs this script inside a cc-quant-chat checkout' }
        if (-not $haveGo) { throw '-FromSource needs Go (https://go.dev/dl/)' }
        $ver = & git -C $src describe --tags --always --dirty 2>$null
        if (-not $ver) { $ver = 'dev' }
        Say "building from source ($ver)"
        $env:CGO_ENABLED = '0'
        Push-Location $src
        try {
            & go build -trimpath -ldflags "-s -w -X github.com/$Repo/internal/version.Version=$ver" -o $new ./cmd/ccqc
            if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
        } finally { Pop-Location }
    } else {
        $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
            'AMD64' { 'amd64' }
            'ARM64' { 'arm64' }
            default { throw "unsupported CPU: $env:PROCESSOR_ARCHITECTURE" }
        }
        $asset = "ccqc_windows_$arch.exe"
        $base = if ($Version -eq 'latest') { "https://github.com/$Repo/releases/latest/download" }
        else { "https://github.com/$Repo/releases/download/$Version" }
        Say "downloading $asset ($Version)"
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $new
        $sums = Join-Path $tmp 'SHA256SUMS'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums
        $want = Get-Content $sums | ForEach-Object {
            $f = $_ -split '\s+'
            if ($f.Count -ge 2 -and $f[1].TrimStart('*') -eq $asset) { $f[0] }
        } | Select-Object -First 1
        if (-not $want) { throw "$asset is not listed in SHA256SUMS" }
        if ((Get-FileHash -Algorithm SHA256 $new).Hash -ine $want) { throw "checksum mismatch for $asset" }
    }

    New-Item -ItemType Directory -Force $Dir | Out-Null
    # A running ccqc (the broker) locks its exe, but Windows allows renaming it.
    if (Test-Path $Exe) {
        Remove-Item "$Exe.old" -Force -ErrorAction SilentlyContinue
        Move-Item $Exe "$Exe.old" -Force
    }
    Move-Item $new $Exe -Force
    Remove-Item "$Exe.old" -Force -ErrorAction SilentlyContinue # still running: removed next time
    Say "installed $(& $Exe version) to $Exe"

    # A running broker keeps the old code: restart it on the new binary.
    if (-not $NoRestart) {
        & { $ErrorActionPreference = 'Continue'; & $Exe broker status *> $null }
        if ($LASTEXITCODE -eq 0) {
            Say 'restarting the running broker on the new version (sessions reconnect by themselves)'
            & $Exe broker restart
            if ($LASTEXITCODE -ne 0) { Say "broker restart failed; run 'ccqc broker restart' later" }
            Start-Sleep -Milliseconds 500
            Remove-Item "$Exe.old" -Force -ErrorAction SilentlyContinue
        }
    }

    if (-not $NoPath) {
        $userPath = Get-UserPath
        if (-not (Test-PathEntry $userPath $Dir)) {
            Set-UserPath ((@($userPath.TrimEnd(';'), $Dir) | Where-Object { $_ }) -join ';')
            Say "added $Dir to the user PATH (new terminals will see it)"
        }
        if (-not (Test-PathEntry $env:Path $Dir)) { $env:Path = "$env:Path;$Dir" }
    }
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
