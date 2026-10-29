#Requires -Version 5.1
<#
.SYNOPSIS
    Prepare a native Windows WxCat deployment: download the WeChatFerry DLLs,
    create config/wxcat.json, optionally install WeChat 3.9.12.51 and open the
    OneBot ports in Windows Firewall.

.DESCRIPTION
    Run this once after extracting the release zip. It never starts WeChat or
    the gateway; use start.ps1 for that.

.PARAMETER InstallWeChat
    Run the bundled WeChatSetup-3.9.12.51.exe silently when WeChat is missing.

.PARAMETER Firewall
    Create inbound TCP rules for 3000 (HTTP API), 3001 (forward WebSocket) and
    6099 (admin panel). Requires an elevated PowerShell.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -InstallWeChat -Firewall
#>
[CmdletBinding()]
param(
    [string]$WcfVersion = "v39.5.2",
    [string]$WeChatVersion = "3.9.12.51",
    [string]$Root,
    [switch]$InstallWeChat,
    [switch]$Firewall
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

if (-not $Root) {
    $Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$Root = (Resolve-Path $Root).Path
$WcfDir = Join-Path $Root "wcf"
$ConfigDir = Join-Path $Root "config"
New-Item -ItemType Directory -Force -Path $WcfDir, $ConfigDir | Out-Null

function Get-WeChatExe {
    $candidates = @(
        (Join-Path ${env:ProgramFiles(x86)} "Tencent\WeChat\WeChat.exe"),
        (Join-Path $env:ProgramFiles "Tencent\WeChat\WeChat.exe")
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path $p)) { return $p }
    }
    return $null
}

$base = "https://github.com/lich0821/WeChatFerry/releases/download/$WcfVersion"
foreach ($name in @("sdk.dll", "spy.dll", "WeChatSetup-$WeChatVersion.exe")) {
    $dest = Join-Path $WcfDir $name
    if (Test-Path $dest) {
        Write-Host "[skip] $name"
        continue
    }
    Write-Host "[download] $name"
    Invoke-WebRequest -Uri "$base/$name" -OutFile $dest -UseBasicParsing
}

$config = Join-Path $ConfigDir "wxcat.json"
if (-not (Test-Path $config)) {
    Copy-Item (Join-Path $Root "config.example.json") $config
    Write-Host "[config] created $config"
} else {
    Write-Host "[config] kept existing $config"
}

if ($InstallWeChat) {
    $exe = Get-WeChatExe
    if ($exe) {
        Write-Host "[wechat] already installed: $exe"
    } else {
        $setup = Join-Path $WcfDir "WeChatSetup-$WeChatVersion.exe"
        Write-Host "[wechat] installing silently (this takes a minute)"
        Start-Process -FilePath $setup -ArgumentList "/S" -Wait
        $exe = Get-WeChatExe
        if (-not $exe) {
            throw "WeChat installation did not produce WeChat.exe"
        }
        Write-Host "[wechat] installed: $exe"
    }
}

# Stop WeChat 3.9 from silently upgrading to 4.x, which breaks the hook.
if (Test-Path "HKCU:\Software\Tencent\WeChat") {
    Set-ItemProperty -Path "HKCU:\Software\Tencent\WeChat" -Name "NeedUpdateType" -Value 0 -Type DWord -ErrorAction SilentlyContinue
}
$exe = Get-WeChatExe
if ($exe) {
    $updater = Join-Path (Split-Path -Parent $exe) "WeChatUpdate.exe"
    if (Test-Path $updater) {
        Rename-Item -Path $updater -NewName "WeChatUpdate.exe.bak" -ErrorAction SilentlyContinue
        Write-Host "[wechat] disabled WeChatUpdate.exe"
    }
}

if ($Firewall) {
    foreach ($port in 3000, 3001, 6099) {
        New-NetFirewallRule -DisplayName "WxCat $port" -Direction Inbound -Protocol TCP `
            -LocalPort $port -Action Allow -ErrorAction SilentlyContinue | Out-Null
    }
    Write-Host "[firewall] inbound TCP 3000/3001/6099 allowed"
}

Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Edit $(Join-Path $ConfigDir 'wxcat.json') and set network.*.token."
Write-Host "  2. Run as Administrator:"
Write-Host "     powershell -ExecutionPolicy Bypass -File scripts\windows\start.ps1"
Write-Host "  3. Scan the QR code in the WeChat window."
