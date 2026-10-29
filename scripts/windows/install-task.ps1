#Requires -Version 5.1
<#
.SYNOPSIS
    Register WxCat to start at user logon via Task Scheduler.

.DESCRIPTION
    WeChat needs an interactive desktop session, so a Windows service is not
    suitable. This task starts start.ps1 at logon with the highest privileges
    available to the user.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\windows\install-task.ps1
#>
[CmdletBinding()]
param(
    [string]$TaskName = "WxCat",
    [string]$Root
)

$ErrorActionPreference = "Stop"

if (-not $Root) {
    $Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$Root = (Resolve-Path $Root).Path
$script = Join-Path $PSScriptRoot "start.ps1"
if (-not (Test-Path $script)) {
    throw "start.ps1 not found next to this script."
}

$action = New-ScheduledTaskAction -Execute "powershell.exe" `
    -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$script`" -Root `"$Root`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" `
    -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings -Force | Out-Null

Write-Host "[task] registered '$TaskName' for $env:USERNAME at logon"
Write-Host "Start now:  Start-ScheduledTask -TaskName $TaskName"
Write-Host "Logs:       Get-Content (Join-Path '$Root' 'data\wxcat.log') -Tail 50"
