#Requires -Version 5.1
<#
.SYNOPSIS
    Start WeChat 3.9, inject the WeChatFerry hook and run the WxCat gateway.

.DESCRIPTION
    Native Windows launcher. It copies sdk.dll/spy.dll next to WeChat.exe,
    starts WeChat if needed, injects with the 32-bit wxinject.exe until the
    WeChatFerry RPC port 10086 is open, then runs wxcat.exe in the foreground.

    Run this in an elevated PowerShell if WeChat itself runs elevated.

.PARAMETER NoStartWeChat
    Do not launch WeChat; require it to be already running.

.PARAMETER InjectTimeoutSec
    How long to keep retrying injection before giving up.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\windows\start.ps1
#>
[CmdletBinding()]
param(
    [string]$Root,
    [string]$Config,
    [string]$WeChatExe,
    [switch]$NoStartWeChat,
    [int]$InjectTimeoutSec = 90
)

$ErrorActionPreference = "Stop"

try { chcp 65001 | Out-Null } catch { }

if (-not $Root) {
    $Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$Root = (Resolve-Path -LiteralPath $Root).Path
if (-not $Config) {
    $Config = Join-Path $Root "config\wxcat.json"
}

function Get-WeChatExe {
    param([string]$Hint, [string]$Root)
    $candidates = @()
    if ($Hint) { $candidates += $Hint }
    $runtime = Join-Path $Root "runtime\wechat"
    if (Test-Path -LiteralPath $runtime) {
        $hits = Get-ChildItem -LiteralPath $runtime -Filter "WeChat.exe" -Recurse -File -ErrorAction SilentlyContinue
        foreach ($hit in $hits) {
            if (Test-Path -LiteralPath (Join-Path $hit.DirectoryName "WeChatWin.dll")) {
                $candidates += $hit.FullName
                break
            }
        }
        if ($hits -and $candidates.Count -eq 0) { $candidates += $hits[0].FullName }
    }
    $candidates += @(
        (Join-Path ${env:ProgramFiles(x86)} "Tencent\WeChat\WeChat.exe"),
        (Join-Path $env:ProgramFiles "Tencent\WeChat\WeChat.exe")
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) { return (Resolve-Path -LiteralPath $p).Path }
    }
    return $null
}

function Test-LocalPort {
    param([int]$Port)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect("127.0.0.1", $Port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne(500)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Assert-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Write-Warning "Not running as Administrator. Injection fails if WeChat runs elevated."
    }
}

Assert-Admin

foreach ($file in @("wxcat.exe", "wxinject.exe")) {
    if (-not (Test-Path -LiteralPath (Join-Path $Root $file))) {
        throw "$file not found in $Root. Extract the release zip first."
    }
}
foreach ($file in @("sdk.dll", "spy.dll")) {
    if (-not (Test-Path -LiteralPath (Join-Path $Root "wcf\$file"))) {
        throw "wcf\$file not found. Run scripts\windows\setup.ps1 first."
    }
}

$exe = Get-WeChatExe -Hint $WeChatExe -Root $Root
if (-not $exe) {
    throw "WeChat.exe not found. Run scripts\windows\setup.ps1 or pass -WeChatExe."
}
Write-Host "[wechat] $exe"

$wechatDir = Split-Path -Parent $exe
[IO.File]::Copy((Join-Path $Root "wcf\sdk.dll"), (Join-Path $wechatDir "sdk.dll"), $true)
[IO.File]::Copy((Join-Path $Root "wcf\spy.dll"), (Join-Path $wechatDir "spy.dll"), $true)
Write-Host "[wechat] copied sdk.dll and spy.dll"

$proc = Get-Process -Name "WeChat" -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $proc) {
    if ($NoStartWeChat) {
        throw "WeChat is not running and -NoStartWeChat was set."
    }
    Write-Host "[wechat] starting"
    # Start-Process treats [version] in the path as a wildcard; use the .NET
    # API with literal strings instead.
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    $psi.WorkingDirectory = (Split-Path -Parent $exe)
    $psi.UseShellExecute = $true
    [void][System.Diagnostics.Process]::Start($psi)
    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Seconds 1
        $proc = Get-Process -Name "WeChat" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($proc) { break }
    }
    if (-not $proc) {
        throw "WeChat did not start."
    }
}
Write-Host "[wechat] pid $($proc.Id)"

Start-Sleep -Seconds 4

$dll = Join-Path $wechatDir "sdk.dll"
$injector = Join-Path $Root "wxinject.exe"
$deadline = (Get-Date).AddSeconds($InjectTimeoutSec)
$injected = $false

while ((Get-Date) -lt $deadline) {
    if (Test-LocalPort 10086) {
        $injected = $true
        break
    }
    Write-Host "[inject] running $injector"
    & $injector $dll
    for ($i = 0; $i -lt 10; $i++) {
        if (Test-LocalPort 10086) {
            $injected = $true
            break
        }
        Start-Sleep -Milliseconds 500
    }
    if ($injected) { break }
    Write-Host "[inject] rpc port not open yet, retrying"
    Start-Sleep -Seconds 2
}

if (-not $injected) {
    throw @"
WeChatFerry RPC port 10086 did not open.

Check:
  1. WeChat is version 3.9.12.51 (not 4.x).
  2. wxinject.exe is the 32-bit build from the release zip.
  3. PowerShell and WeChat run at the same privilege level.
  4. sdk.dll and spy.dll are in $wechatDir.
"@
}

Write-Host "[inject] rpc port is up"
Write-Host "[wxcat] starting gateway, config: $Config"
$logDir = Join-Path $Root "data"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$env:WXCAT_DATA = $logDir
$logFile = Join-Path $logDir "wxcat.log"
Push-Location -LiteralPath $Root
try {
    & (Join-Path $Root "wxcat.exe") -c $Config 2>&1 | Tee-Object -LiteralPath $logFile -Append
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
