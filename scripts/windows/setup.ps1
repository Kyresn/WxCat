#Requires -Version 5.1
<#
.SYNOPSIS
    Prepare a native Windows WxCat deployment with a private, portable copy of
    WeChat 3.9.12.51. Nothing is installed into Program Files, so an existing
    WeChat 4.x installation is left untouched.

.DESCRIPTION
    Run this once after extracting the release zip. It:
      1. downloads the WeChatFerry DLLs and the WeChat 3.9.12.51 installer
      2. extracts WeChat into runtime\wechat (or installs it into that private
         directory when 7-Zip is unavailable)
      3. creates config\wxcat.json
      4. optionally opens the OneBot ports in Windows Firewall

    It never starts WeChat or the gateway; use start.ps1 for that.

.PARAMETER WeChatDir
    Private directory for WeChat. Defaults to <root>\runtime\wechat.

.PARAMETER SkipWeChat
    Do not provision WeChat; use an existing -WeChatExe at start time.

.PARAMETER OfflineSetup
    Path to a WeChatSetup-3.9.12.51.exe to use instead of downloading it.

.PARAMETER Firewall
    Create inbound TCP rules for 3000 (HTTP API), 3001 (forward WebSocket) and
    6099 (admin panel). Requires an elevated PowerShell.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -Firewall
#>
[CmdletBinding()]
param(
    [string]$WcfVersion = "v39.5.2",
    [string]$WeChatVersion = "3.9.12.51",
    [string]$Root,
    [string]$WeChatDir,
    [string]$OfflineSetup,
    [switch]$SkipWeChat,
    [switch]$Firewall
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

if (-not $Root) {
    $Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$Root = (Resolve-Path $Root).Path
if (-not $WeChatDir) {
    $WeChatDir = Join-Path $Root "runtime\wechat"
}
$WcfDir = Join-Path $Root "wcf"
$ConfigDir = Join-Path $Root "config"
New-Item -ItemType Directory -Force -Path $WcfDir, $ConfigDir, $WeChatDir | Out-Null

function Get-RemoteFile {
    param(
        [string]$Url,
        [string]$Dest,
        [long]$MinBytes = 1024
    )
    $mirrors = @(
        $Url,
        ("https://ghfast.top/" + $Url),
        ("https://gh.llkk.cc/" + $Url),
        ("https://ghproxy.net/" + $Url),
        ("https://gh-proxy.com/" + $Url)
    )
    foreach ($u in $mirrors) {
        try {
            Write-Host "[download] $u"
            Invoke-WebRequest -Uri $u -OutFile $Dest -UseBasicParsing -TimeoutSec 900
            if ((Get-Item -LiteralPath $Dest).Length -ge $MinBytes) { return }
            Write-Warning "[download] file too small, retrying: $u"
        } catch {
            Write-Warning "[download] failed: $u ($($_.Exception.Message))"
        }
    }
    throw "Could not download $Url. Download it manually and pass -OfflineSetup."
}

function Find-WeChatExe {
    param([string]$Dir)
    if (-not (Test-Path -LiteralPath $Dir)) { return $null }
    $hits = Get-ChildItem -LiteralPath $Dir -Filter "WeChat.exe" -Recurse -File -ErrorAction SilentlyContinue
    # Prefer the payload directory that also contains WeChatWin.dll. The
    # installer archive has a launcher stub at the root plus the real client
    # under [<version>]\.
    foreach ($hit in $hits) {
        if (Test-Path -LiteralPath (Join-Path $hit.DirectoryName "WeChatWin.dll")) { return $hit.FullName }
    }
    if ($hits) { return $hits[0].FullName }
    return $null
}

function Get-SevenZip {
    $candidates = @(
        (Join-Path $env:ProgramFiles "7-Zip\7z.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "7-Zip\7z.exe")
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) { return $p }
    }
    $cmd = Get-Command "7z.exe" -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Test-WeChatPayload {
    param([string]$Dir)
    foreach ($name in @("WeChat.exe", "WeChatWin.dll", "WeChatResource.dll")) {
        $path = Join-Path $Dir $name
        if (-not (Test-Path -LiteralPath $path)) { return $false }
        if ((Get-Item -LiteralPath $path).Length -lt 100000) { return $false }
    }
    return $true
}

function Install-WeChatPrivate {
    param(
        [string]$Setup,
        [string]$Dir
    )

    New-Item -ItemType Directory -Force -Path $Dir | Out-Null

    $sevenZip = Get-SevenZip
    if ($sevenZip) {
        Write-Host "[wechat] verifying installer with $sevenZip"
        & $sevenZip t $Setup -y | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "The WeChat installer is corrupt (7-Zip test failed). Delete $Setup and re-run setup.ps1."
        }

        Write-Host "[wechat] extracting with $sevenZip into $Dir"
        $tmp = Join-Path $env:TEMP ("wxcat-wechat-" + [Guid]::NewGuid().ToString("N"))
        New-Item -ItemType Directory -Force -Path $tmp | Out-Null
        try {
            & $sevenZip x $Setup "-o$tmp" -y | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "[wechat] 7-Zip extraction failed with exit code $LASTEXITCODE"
            } else {
                # Large files can appear a moment after 7-Zip exits (antivirus
                # or filesystem flush). Poll before deciding the payload is bad.
                $exe = $null
                for ($i = 0; $i -lt 60; $i++) {
                    $exe = Find-WeChatExe $tmp
                    if ($exe -and (Test-WeChatPayload (Split-Path -Parent $exe))) { break }
                    Start-Sleep -Milliseconds 500
                }
                if ($exe -and (Test-WeChatPayload (Split-Path -Parent $exe))) {
                    Get-ChildItem -LiteralPath $tmp -Force | Copy-Item -Destination $Dir -Recurse -Force
                    $found = Find-WeChatExe $Dir
                    if ($found -and (Test-WeChatPayload (Split-Path -Parent $found))) { return $found }
                }
                Write-Warning "[wechat] extracted files are incomplete"
            }
        } finally {
            Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
        Write-Warning "[wechat] falling back to a private install"
    }

    Write-Host "[wechat] installing into the private directory $Dir"
    # NSIS /D= needs the last argument, unquoted. Use the 8.3 path so spaces
    # in $Root cannot break the argument.
    $fso = New-Object -ComObject Scripting.FileSystemObject
    $shortDir = $fso.GetFolder($Dir).ShortPath
    Start-Process -FilePath $Setup -ArgumentList "/S", "/D=$shortDir" -Wait
    for ($i = 0; $i -lt 60; $i++) {
        $found = Find-WeChatExe $Dir
        if ($found -and (Test-WeChatPayload (Split-Path -Parent $found))) { return $found }
        Start-Sleep -Seconds 2
    }
    return $null
}

$base = "https://github.com/lich0821/WeChatFerry/releases/download/$WcfVersion"
foreach ($name in @("sdk.dll", "spy.dll")) {
    $dest = Join-Path $WcfDir $name
    if (Test-Path -LiteralPath $dest) {
        Write-Host "[skip] $name"
        continue
    }
    Write-Host "[download] $name"
    Get-RemoteFile -Url "$base/$name" -Dest $dest
}

$setupName = "WeChatSetup-$WeChatVersion.exe"
$setupPath = Join-Path $WcfDir $setupName

if ($SkipWeChat) {
    Write-Host "[wechat] skipped; pass -WeChatExe to start.ps1"
} else {
    if ($OfflineSetup) {
        $setupPath = (Resolve-Path $OfflineSetup).Path
        Write-Host "[wechat] using offline installer $setupPath"
    } elseif (-not (Test-Path -LiteralPath $setupPath)) {
        Write-Host "[download] $setupName (about 285 MB)"
        Get-RemoteFile -Url "$base/$setupName" -Dest $setupPath -MinBytes 200MB
    }
    $exe = Find-WeChatExe $WeChatDir
    if ($exe) {
        Write-Host "[wechat] already provisioned: $exe"
    } else {
        $exe = Install-WeChatPrivate -Setup $setupPath -Dir $WeChatDir
        if (-not $exe) {
            throw "Could not provision WeChat into $WeChatDir. Install 7-Zip and retry, or pass -OfflineSetup."
        }
        Write-Host "[wechat] provisioned: $exe"
    }

    $payloadDir = Split-Path -Parent $exe
    [IO.File]::Copy((Join-Path $WcfDir "sdk.dll"), (Join-Path $payloadDir "sdk.dll"), $true)
    [IO.File]::Copy((Join-Path $WcfDir "spy.dll"), (Join-Path $payloadDir "spy.dll"), $true)

    # Keep this private copy from silently upgrading itself to 4.x.
    $updater = Join-Path (Split-Path -Parent $exe) "WeChatUpdate.exe"
    if (Test-Path -LiteralPath $updater) {
        Rename-Item -LiteralPath $updater -NewName "WeChatUpdate.exe.bak" -ErrorAction SilentlyContinue
        Write-Host "[wechat] disabled WeChatUpdate.exe in the private copy"
    }
}

$config = Join-Path $ConfigDir "wxcat.json"
if (-not (Test-Path -LiteralPath $config)) {
    [IO.File]::Copy((Join-Path $Root "config.example.json"), $config, $false)
    Write-Host "[config] created $config"
} else {
    Write-Host "[config] kept existing $config"
}

if ($Firewall) {
    foreach ($port in 3000, 3001, 6099) {
        New-NetFirewallRule -DisplayName "WxCat $port" -Direction Inbound -Protocol TCP `
            -LocalPort $port -Action Allow -ErrorAction SilentlyContinue | Out-Null
    }
    Write-Host "[firewall] inbound TCP 3000/3001/6099 allowed"
}

# Remove the Mark-of-the-Web from files extracted from the release zip so
# SmartScreen does not block wxcat.exe / wxinject.exe.
Get-ChildItem -Path $Root -Recurse -File -ErrorAction SilentlyContinue |
    Unblock-File -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Edit $(Join-Path $ConfigDir 'wxcat.json') and set network.*.token."
Write-Host "  2. Run as Administrator:"
Write-Host "     powershell -ExecutionPolicy Bypass -File scripts\windows\start.ps1"
Write-Host "  3. Scan the QR code in the WeChat window."
