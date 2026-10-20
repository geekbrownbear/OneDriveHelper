<#
.SYNOPSIS
    Removes OneDrive Helper and clears its NinjaOne custom fields. Run from NinjaOne as SYSTEM.

.DESCRIPTION
    NinjaOne script variables:
      purge   Checkbox: also delete the settings in HKLM\SOFTWARE\OneDriveHelper
      fieldStatus, fieldHealth, fieldStaleCount   Custom field names if you changed the defaults
#>
param(
    [string]$Purge = $env:purge,
    [string]$FieldStatus = $env:fieldStatus,
    [string]$FieldHealth = $env:fieldHealth,
    [string]$FieldStaleCount = $env:fieldStaleCount
)
$ErrorActionPreference = 'Continue'
if (-not $FieldStatus) { $FieldStatus = 'ODErrors' }
if (-not $FieldHealth) { $FieldHealth = 'ODHealth' }
if (-not $FieldStaleCount) { $FieldStaleCount = 'ODStaleFolders' }

$exe = 'C:\Program Files\OneDriveHelper\OneDriveHelper.exe'
if (Test-Path $exe) {
    $args = @('uninstall')
    if ($Purge -in @('true', '1', 'yes')) { $args += '--purge' }
    & $exe @args
    Write-Host "uninstall exited with code $LASTEXITCODE"
} else {
    Write-Host 'OneDrive Helper is not installed.'
}

# Leave the fields in a state a condition won't trip over.
try {
    '<i>OneDrive Helper is not installed.</i>' | Ninja-Property-Set-Piped $FieldStatus
    Ninja-Property-Set $FieldHealth 'NotInstalled'
    Ninja-Property-Set $FieldStaleCount 0
} catch {
    Write-Host "Could not clear the custom fields: $_"
}

# The program folder is kept while any user agent is still closing; remove it next time if it's empty of running processes.
$dir = 'C:\Program Files\OneDriveHelper'
if (Test-Path $dir) {
    Start-Sleep -Seconds 5
    Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path $dir) { Write-Host "$dir left in place (an agent is still exiting); delete it later." }
}
exit 0
