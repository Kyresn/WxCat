#Requires -Version 5.1
<#
.SYNOPSIS
    Start AstrWeChat or WeFlow (local WeChat database API) and then WxCat,
    without using the WCF injection backend.

.DESCRIPTION
    WxCat's "weflow"/"astrwechat" backend consumes the same local HTTP/SSE API
    that AstrWeChat and WeFlow expose on 127.0.0.1:5031.

    This launcher:
      1. checks whether that API is already healthy
      2. otherwise starts AstrWeChat/WeFlow from a known install location
      3. waits for /health
      4. runs wxcat.exe with config\wxcat.json

    It never injects into WeChat and never touches an installed WeChat 4.x.

.PARAMETER ApiUrl
    AstrWeChat/WeFlow API base URL, default http://127.0.0.1:5031.

.PARAMETER AllowRemoteApi
    Allow a non-loopback ApiUrl. Off by default so a mistyped URL cannot leak
    requests to another host.

.PARAMETER AppExe
    Explicit AstrWeChat.exe / WeFlow.exe path when auto-detection fails.
#>
[CmdletBinding()]
param(
    [string]$Root,
    [string]$ApiUrl = "http://127.0.0.1:5031",
    [string]$AppExe = "",
    [switch]$AllowRemoteApi,
    [int]$WaitSeconds = 90
)

$ErrorActionPreference = "Stop"
if (-not $Root) {
    $Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$Root = (Resolve-Path -LiteralPath $Root).Path
$Config = Join-Path $Root "config\wxcat.json"
$WxCat = Join-Path $Root "wxcat.exe"

function Test-Api {
    try {
        # /health is unauthenticated; never send the API token here.
        $r = Invoke-WebRequest -Uri ($ApiUrl.TrimEnd('/') + "/health") `
            -UseBasicParsing -TimeoutSec 3 -MaximumRedirection 0
        return $r.StatusCode -eq 200
    } catch {
        return $false
    }
}

function Find-App {
    param([string]$Hint)
    $candidates = @()
    if ($Hint) { $candidates += $Hint }
    $candidates += @(
        (Join-Path $env:LOCALAPPDATA "Programs\AstrWeChat\AstrWeChat.exe"),
        (Join-Path $env:LOCALAPPDATA "Programs\astrwechat\AstrWeChat.exe"),
        (Join-Path $env:ProgramFiles "AstrWeChat\AstrWeChat.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "AstrWeChat\AstrWeChat.exe"),
        (Join-Path $env:LOCALAPPDATA "Programs\WeFlow\WeFlow.exe"),
        (Join-Path $env:ProgramFiles "WeFlow\WeFlow.exe")
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) { return (Resolve-Path -LiteralPath $p).Path }
    }
    return $null
}

$api = [Uri]$ApiUrl
if (-not $AllowRemoteApi -and -not $api.IsLoopback) {
    throw "ApiUrl must be loopback unless -AllowRemoteApi is set: $ApiUrl"
}

if (-not (Test-Path -LiteralPath $WxCat)) {
    throw "wxcat.exe not found in $Root"
}
if (-not (Test-Path -LiteralPath $Config)) {
    throw "config not found: $Config. Copy config.example.json and set backend.type to astrwechat/weflow."
}

$cfgText = Get-Content -LiteralPath $Config -Raw
if ($cfgText -notmatch '"type"\s*:\s*"(astrwechat|weflow)"') {
    Write-Warning "backend.type is not astrwechat/weflow; WxCat will use whatever the config says."
}

if (-not (Test-Api)) {
    Write-Host "[api] $ApiUrl is not responding; looking for AstrWeChat/WeFlow"
    $app = Find-App -Hint $AppExe
    if (-not $app) {
        throw @"
AstrWeChat/WeFlow API is not reachable at $ApiUrl and no app was found.

Install one of them, log in to WeChat, enable "API 服务" and "主动推送" in
its settings, then run this script again. Or pass -AppExe <path>.
"@
    }
    Write-Host "[api] starting $app"
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $app
    $psi.WorkingDirectory = Split-Path -Parent $app
    $psi.UseShellExecute = $true
    [void][System.Diagnostics.Process]::Start($psi)

    $deadline = (Get-Date).AddSeconds($WaitSeconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Api) { break }
        Start-Sleep -Seconds 2
    }
    if (-not (Test-Api)) {
        throw "AstrWeChat/WeFlow API did not become healthy at $ApiUrl. Finish its setup (database key + API service) first."
    }
}

Write-Host "[api] healthy: $ApiUrl"
Write-Host "[wxcat] starting with $Config"
$env:WXCAT_DATA = Join-Path $Root "data"
Push-Location -LiteralPath $Root
try {
    & $WxCat -c $Config
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
