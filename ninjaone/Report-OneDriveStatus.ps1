<#
.SYNOPSIS
    Fallback: copies OneDrive Helper's status into the NinjaOne custom fields from a scheduled script.

.DESCRIPTION
    Normally the OneDrive Helper service writes the custom fields itself through ninjarmm-cli.exe
    and this script is not needed. Use it (hourly, as SYSTEM) only if that does not work in your
    tenant, after setting NinjaEnabled to 0 in the helper's settings so the two don't fight.

    It reads C:\ProgramData\OneDriveHelper\status.json (written by the service every 30 seconds)
    and the registry key HKLM\SOFTWARE\OneDriveHelper\Status, and sets the same four fields the
    service would. Field names can be changed with the script variables fieldStatus, fieldHealth,
    fieldStaleCount and fieldLastReport.

    Exit code 0 when the machine is Healthy or has no user, 1 otherwise, so the script result
    itself can drive a condition.
#>
param(
    [string]$FieldStatus = $env:fieldStatus,
    [string]$FieldHealth = $env:fieldHealth,
    [string]$FieldStaleCount = $env:fieldStaleCount,
    [string]$FieldLastReport = $env:fieldLastReport,
    [int]$MaxAgeHours = 2
)
$ErrorActionPreference = 'Continue'
if (-not $FieldStatus) { $FieldStatus = 'ODErrors' }
if (-not $FieldHealth) { $FieldHealth = 'ODHealth' }
if (-not $FieldStaleCount) { $FieldStaleCount = 'ODStaleFolders' }
if (-not $FieldLastReport) { $FieldLastReport = 'ODLastReport' }

$exe = 'C:\Program Files\OneDriveHelper\OneDriveHelper.exe'
$statusFile = Join-Path $env:ProgramData 'OneDriveHelper\status.json'

function Set-Field([string]$name, $value) {
    if (-not $name) { return }
    try { Ninja-Property-Set $name $value } catch { Write-Host "Ninja-Property-Set ${name}: $_" }
}

if (-not (Test-Path $exe) -or -not (Test-Path $statusFile)) {
    Write-Host 'OneDrive Helper is not installed or has not reported yet.'
    try { '<i>OneDrive Helper is not installed or has not reported yet.</i>' | Ninja-Property-Set-Piped $FieldStatus } catch { Write-Host "$_" }
    Set-Field $FieldHealth 'NotInstalled'
    Set-Field $FieldStaleCount 0
    exit 1
}

$status = Get-Content -LiteralPath $statusFile -Raw -Encoding UTF8 | ConvertFrom-Json
$generated = [datetime]::Parse($status.generatedAt, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
$health = "$($status.health)"
if ($generated -lt (Get-Date).AddHours(-$MaxAgeHours)) {
    Write-Host "status.json is from $generated; the service is not reporting."
    $health = 'Stale'
}

# The HTML table is rendered by the helper itself so this script and the service show the same thing.
$html = (& $exe status --html) -join "`n"
if ($LASTEXITCODE -or -not $html) { $html = "<i>Could not render the status table (exit $LASTEXITCODE).</i>" }
try { $html | Ninja-Property-Set-Piped $FieldStatus } catch { Write-Host "Ninja-Property-Set-Piped ${FieldStatus}: $_" }
Set-Field $FieldHealth $health
Set-Field $FieldStaleCount ([int]$status.staleFolderCount)
Set-Field $FieldLastReport ([DateTimeOffset]$generated).ToUnixTimeSeconds()

Write-Host "Health: $health  Folders not syncing: $($status.staleFolderCount)  Users: $($status.users.Count)  Reported: $generated"
foreach ($f in $status.staleFolders) { Write-Host "  $($f.user): $($f.path)" }
if ($health -in @('Healthy', 'NoUser')) { exit 0 } else { exit 1 }
