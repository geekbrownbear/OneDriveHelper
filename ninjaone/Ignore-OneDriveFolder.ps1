<#
.SYNOPSIS
    Keeps a folder that OneDrive no longer syncs: stops reporting it and keeps it out of every fix.
    Run from NinjaOne as SYSTEM after confirming with the user that they want the folder (for
    example a copy of an archived SharePoint site kept for reference).

.DESCRIPTION
    NinjaOne script variables:
      folder   Full path, e.g. C:\Users\jsmith\Contoso\Archive - Documents            (required)
      note     Why it is kept, e.g. "T12345 kept for reference, approved by J. Smith"   (recommended)
      remove   Checkbox: take the folder off the ignore list instead (report it again)

    The list is machine-wide, under HKLM\SOFTWARE\OneDriveHelper\IgnoredFolders, so only
    administrators can change it. Ignoring a folder also ignores everything inside it. The ignored
    folders are shown at the bottom of the ODErrors table so nobody forgets they exist.
#>
param(
    [string]$Folder = $env:folder,
    [string]$Note = $env:note,
    [string]$Remove = $env:remove
)
$ErrorActionPreference = 'Stop'
$exe = 'C:\Program Files\OneDriveHelper\OneDriveHelper.exe'
if (-not (Test-Path $exe)) { Write-Host 'OneDrive Helper is not installed.'; exit 1 }
if (-not $Folder) { Write-Host 'The "folder" script variable is required.'; exit 2 }

if ($Remove -in @('true', '1', 'yes')) {
    & $exe ignore remove $Folder
} else {
    if (-not $Note) { $Note = "ignored from NinjaOne on $(Get-Date -Format 'yyyy-MM-dd')" }
    & $exe ignore add $Folder --note $Note
}
$code = $LASTEXITCODE
Write-Host ''
& $exe ignore list
# Refresh the fields now rather than at the next change.
& $exe ninja push | Out-Null
exit $code
