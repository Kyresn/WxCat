#Requires -Version 5.1
<#
.SYNOPSIS
    Send a text message or a file to a WeChat chat through Windows UI
    Automation. Used by WxCat's WeFlow backend (sendMode "uia").

.DESCRIPTION
    WeFlow has no send API, so this script drives the WeChat desktop client:
    Ctrl+F search, paste the target name, open the chat, paste the payload and
    press Enter.

    Run it with -Sta (WxCat does). WeChat must be logged in and its window must
    be visible; the script brings it to the foreground and steals focus.

.PARAMETER TargetName
    Contact or group display name exactly as shown in WeChat search.

.PARAMETER TextFile
    UTF-8 file containing the message text.

.PARAMETER FilePath
    Image or file to attach.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$TargetName,
    [string]$TextFile,
    [string]$FilePath
)

$ErrorActionPreference = "Stop"

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes

Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class WxWin {
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hWnd);
}
"@

function Get-WeChatWindow {
    $procs = Get-Process -Name Weixin, WeChat -ErrorAction SilentlyContinue |
        Where-Object { $_.MainWindowHandle -ne [IntPtr]::Zero }
    if (-not $procs) {
        throw "WeChat main window not found. Start and log in to WeChat first."
    }
    return ($procs | Select-Object -First 1)
}

function Send-Keys {
    param([string]$Keys)
    [System.Windows.Forms.SendKeys]::SendWait($Keys)
    Start-Sleep -Milliseconds 250
}

$proc = Get-WeChatWindow
if ([WxWin]::IsIconic($proc.MainWindowHandle)) {
    [void][WxWin]::ShowWindow($proc.MainWindowHandle, 9)
}
[void][WxWin]::ShowWindow($proc.MainWindowHandle, 5)
[void][WxWin]::SetForegroundWindow($proc.MainWindowHandle)
Start-Sleep -Milliseconds 500

# Search for the chat and open it.
Send-Keys "^f"
Start-Sleep -Milliseconds 400
[System.Windows.Forms.Clipboard]::SetText($TargetName)
Start-Sleep -Milliseconds 100
Send-Keys "^v"
Start-Sleep -Milliseconds 800
Send-Keys "{ENTER}"
Start-Sleep -Milliseconds 1000

if ($TextFile) {
    if (-not (Test-Path -LiteralPath $TextFile)) {
        throw "text file not found: $TextFile"
    }
    $text = Get-Content -LiteralPath $TextFile -Raw -Encoding UTF8
    [System.Windows.Forms.Clipboard]::SetText($text)
    Start-Sleep -Milliseconds 100
    Send-Keys "^v"
    Start-Sleep -Milliseconds 400
    Send-Keys "{ENTER}"
}

if ($FilePath) {
    if (-not (Test-Path -LiteralPath $FilePath)) {
        throw "file not found: $FilePath"
    }
    $ext = [IO.Path]::GetExtension($FilePath).ToLowerInvariant()
    if ($ext -in @(".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp")) {
        $img = [System.Drawing.Image]::FromFile($FilePath)
        try {
            [System.Windows.Forms.Clipboard]::SetImage($img)
        } finally {
            $img.Dispose()
        }
    } else {
        $col = New-Object System.Collections.Specialized.StringCollection
        [void]$col.Add($FilePath)
        [System.Windows.Forms.Clipboard]::SetFileDropList($col)
    }
    Start-Sleep -Milliseconds 100
    Send-Keys "^v"
    Start-Sleep -Milliseconds 800
    Send-Keys "{ENTER}"
}

Write-Output "sent to $TargetName"
