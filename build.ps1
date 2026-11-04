# build.ps1 - one-step build for dsh-tray.
#
# Usage:
#   .\build.ps1                 # build with the version already in internal/tray/main.go
#   .\build.ps1 -Version 1.3.0  # bump version first, then build
#   .\build.ps1 -Version 1.2.1 -Note "fix tray icon flicker"
#
# Versioning rule:
#   new feature -> bump minor   (1.2.0 -> 1.3.0)
#   bug fix     -> bump patch   (1.2.0 -> 1.2.1)
#
# The version lives in TWO places and must stay in sync:
#   internal/tray/main.go   var appVersion = "x.y.z"
#   configs/versioninfo.json   FileVersion / ProductVersion = "x.y.z.0"
#
# Layout:
#   cmd/dsh-tray/     entrypoint (+ generated versioninfo.syso, must sit here)
#   internal/tray/    implementation
#   resources/        app.manifest
#   configs/          versioninfo.json
#   bin/              build output
#
# NOTE: keep this file ASCII-only. Windows PowerShell 5.1 decodes .ps1 as ANSI.

param(
    [string]$Version = "",
    [string]$Note = ""
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Definition
Set-Location $root

$utf8 = New-Object System.Text.UTF8Encoding($false)

function Read-Utf8([string]$path) {
    return [System.IO.File]::ReadAllText($path, [System.Text.Encoding]::UTF8)
}
function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, $utf8)
}

$mainGo   = Join-Path $root 'internal\tray\main.go'
$verJson  = Join-Path $root 'configs\versioninfo.json'
$manifest = Join-Path $root 'resources\app.manifest'
$appIco   = Join-Path $root 'resources\app.ico'
$syso     = Join-Path $root 'cmd\dsh-tray\versioninfo.syso'
$outExe   = Join-Path $root 'bin\dsh-tray.exe'

foreach ($p in @($mainGo, $verJson, $manifest)) {
    if (-not (Test-Path $p)) { throw ("missing required file: " + $p) }
}

$src = Read-Utf8 $mainGo
$m = [regex]::Match($src, 'var appVersion = "([^"]+)"')
if (-not $m.Success) { throw 'cannot find appVersion in internal/tray/main.go' }
$current = $m.Groups[1].Value

if ($Version -ne '') {
    if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "bad version: $Version (expect x.y.z)" }
    $parts = $Version.Split('.')
    $src = $src -replace 'var appVersion = "[^"]+"', ('var appVersion = "' + $Version + '"')
    Write-Utf8 $mainGo $src

    $json = Read-Utf8 $verJson
    $json = $json -replace '"Major": \d+', ('"Major": ' + $parts[0])
    $json = $json -replace '"Minor": \d+', ('"Minor": ' + $parts[1])
    $json = $json -replace '"Patch": \d+', ('"Patch": ' + $parts[2])
    $json = $json -replace '"Build": \d+', '"Build": 0'
    $json = $json -replace '"(FileVersion|ProductVersion)": "[^"]+"', ('"$1": "' + $Version + '.0"')
    Write-Utf8 $verJson $json
    $current = $Version
    Write-Host ("version -> " + $current)
}

# regenerate the application icon (sprite frame -> multi-size .ico)
& go run ./tools/genicon $appIco
if ($LASTEXITCODE -ne 0) { throw 'genicon failed' }

# regenerate resource: manifest + version info + app icon in ONE .rsrc section
$gvi = Join-Path $env:GOPATH 'bin\goversioninfo.exe'
if (-not (Test-Path $gvi)) {
    $gvi = Join-Path (Split-Path (Split-Path (& go env GOPATH))) 'go\bin\goversioninfo.exe'
}
if (-not (Test-Path $gvi)) {
    throw 'goversioninfo not found, run: go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest'
}
& $gvi -o $syso -manifest $manifest -icon $appIco $verJson
if ($LASTEXITCODE -ne 0) { throw 'goversioninfo failed' }

New-Item -ItemType Directory -Path (Join-Path $root 'bin') -Force | Out-Null
& go build -ldflags '-H windowsgui -s -w' -o $outExe ./cmd/dsh-tray
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

$vi = (Get-Item $outExe).VersionInfo
$size = [math]::Round((Get-Item $outExe).Length / 1MB, 2)
Write-Host ("built: bin\dsh-tray.exe  appVersion=" + $current + "  fileVersion=" + $vi.FileVersion + "  size=" + $size + "MB")
if ($Note -ne '') { Write-Host ("note: " + $Note) }
