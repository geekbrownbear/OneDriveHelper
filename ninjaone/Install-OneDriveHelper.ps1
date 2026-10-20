<#
.SYNOPSIS
    Installs or upgrades OneDrive Helper from a GitHub release. Run from NinjaOne as SYSTEM.

.DESCRIPTION
    Downloads the release zip for this PC's architecture, checks its SHA-256 hash, and runs
    "OneDriveHelper.exe install". The service then starts, reads its settings from the
    ODHelperConfig custom field (if you set one up) and begins reporting to the custom fields.

    NinjaOne script variables (they arrive as environment variables):
      version      Release tag, e.g. v0.9.0                                   (required)
      hashX64      SHA-256 of OneDriveHelper-<version>-x64.zip from SHA256SUMS.txt
      hashArm64    SHA-256 of OneDriveHelper-<version>-arm64.zip
      downloadBase Where the zips are (default: this project's GitHub releases).
                   Set it to your own file share or web server if you prefer.
      settingsJson Optional settings JSON applied at install (ODHelperConfig overrides it later)
      allowUnverified  Checkbox: install even without a hash (not recommended)

    The same script works from a plain PowerShell prompt: pass the values as parameters.

.NOTES
    Project: https://github.com/geekbrownbear/OneDriveHelper
#>
param(
    [string]$Version = $env:version,
    [string]$HashX64 = $env:hashX64,
    [string]$HashArm64 = $env:hashArm64,
    [string]$DownloadBase = $env:downloadBase,
    [string]$SettingsJson = $env:settingsJson,
    [string]$AllowUnverified = $env:allowUnverified
)
$ErrorActionPreference = 'Stop'

if (-not $Version) { Write-Host 'The "version" script variable is required (e.g. v0.9.0).'; exit 2 }
if ($Version -notmatch '^v') { $Version = "v$Version" }
if (-not $DownloadBase) { $DownloadBase = "https://github.com/geekbrownbear/OneDriveHelper/releases/download/$Version" }
$unverified = $AllowUnverified -in @('true', '1', 'yes')

$arch = if ($env:PROCESSOR_ARCHITEW6432 -eq 'ARM64' -or $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
$hash = if ($arch -eq 'arm64') { $HashArm64 } else { $HashX64 }
$hash = "$hash".Trim().ToUpperInvariant()
if (-not $hash -and -not $unverified) {
    Write-Host "No SHA-256 hash given for the $arch package. Paste it from SHA256SUMS.txt on the release page, or tick allowUnverified."
    exit 2
}

$installed = 'C:\Program Files\OneDriveHelper\OneDriveHelper.exe'
if (Test-Path $installed) {
    $have = (& $installed version 2>$null | Select-Object -First 1)
    if ("v$have" -eq $Version) {
        Write-Host "OneDrive Helper $Version is already installed."
        & $installed ninja check
        exit 0
    }
    Write-Host "Upgrading OneDrive Helper $have -> $Version"
}

$url = "$($DownloadBase.TrimEnd('/'))/OneDriveHelper-$Version-$arch.zip"

# Work in a randomly named folder that only SYSTEM and Administrators can touch,
# so nobody can swap the package between the hash check and the install.
$work = Join-Path $env:TEMP ("OneDriveHelper-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
& icacls.exe $work /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE) { throw "Could not secure $work" }
$zip = Join-Path $work 'package.zip'
$pkg = Join-Path $work 'package'

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Write-Host "Downloading $url"
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
    $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToUpperInvariant()
    if ($hash) {
        if ($got -ne $hash) { throw "Checksum mismatch for $url`n  expected $hash`n  got      $got" }
        Write-Host 'Checksum OK'
    } else {
        Write-Host "WARNING: installing without a checksum (SHA-256 of the download: $got)"
    }
    Expand-Archive $zip -DestinationPath $pkg
    $args = @('install')
    if ($SettingsJson -and $SettingsJson.Trim()) {
        $settings = Join-Path $pkg 'settings.json'
        [IO.File]::WriteAllText($settings, $SettingsJson, (New-Object Text.UTF8Encoding $false))
        & (Join-Path $pkg 'OneDriveHelper.exe') config validate $settings
        if ($LASTEXITCODE) { throw 'The settingsJson variable is not a valid settings file (see above).' }
        $args += @('--config', $settings)
    }
    & (Join-Path $pkg 'OneDriveHelper.exe') @args
    $code = $LASTEXITCODE
    if ($code) { throw "install exited with code $code" }
    Write-Host ''
    & $installed ninja check
} finally {
    Remove-Item $work -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Host "OneDrive Helper $Version installed. The service reports to the NinjaOne custom fields within a minute."
exit 0
