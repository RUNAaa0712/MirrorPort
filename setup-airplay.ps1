[CmdletBinding()]
param([string]$ArchivePath)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$url = 'https://github.com/hull-cai/AirBridge-Windows/releases/download/v0.3.0/AirBridge-Windows-Portable-x64.zip'
$sha256 = '29866d361d31eb3381ab4533b3d49de99b8ae0322330d86aad2f54500c7e6886'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'engines'))
$target = Join-Path $root 'airplay'

if ((Test-Path -LiteralPath (Join-Path $target 'backend\uxplay.exe') -PathType Leaf) -or
    (Test-Path -LiteralPath (Join-Path $target 'uxplay.exe') -PathType Leaf)) {
    Write-Host "AirPlay is already installed: $target"
    exit 0
}
if (Test-Path -LiteralPath $target) {
    throw "Incomplete AirPlay installation at $target. Move that folder aside and retry."
}

$null = New-Item -ItemType Directory -Path $root -Force
$stage = Join-Path $root ('.setup-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $stage
try {
    if ($ArchivePath) {
        $archive = (Resolve-Path -LiteralPath $ArchivePath).ProviderPath
    } else {
        $archive = Join-Path $stage 'AirBridge-Windows-Portable-x64.zip'
        Write-Host "Downloading the AirPlay runtime: $url"
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $archive -TimeoutSec 180
        } catch {
            throw "Download failed. Download the ZIP from the release page and pass its path to setup-airplay.ps1 -ArchivePath. $($_.Exception.Message)"
        }
    }
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $sha256) {
        throw 'The archive does not match the official SHA-256.'
    }

    $expanded = Join-Path $stage 'expanded'
    Expand-Archive -LiteralPath $archive -DestinationPath $expanded
    $engine = Get-ChildItem -LiteralPath $expanded -Filter 'uxplay.exe' -File -Recurse |
        Where-Object { $_.Directory.Name -eq 'backend' } | Select-Object -First 1
    if (!$engine) { throw 'The archive is missing the AirPlay backend.' }
    $package = $engine.Directory.Parent.FullName
    $licenses = Join-Path $package 'licenses'
    if (!(Test-Path -LiteralPath $licenses -PathType Container)) {
        throw 'The archive is missing the runtime license notices.'
    }

    $destination = Join-Path $stage 'runtime'
    $null = New-Item -ItemType Directory -Path $destination
    Copy-Item -LiteralPath $engine.Directory.FullName,$licenses -Destination $destination -Recurse
    [IO.Directory]::Move($destination, $target)
    Write-Host "AirPlay runtime is ready: $target"
} finally {
    $resolvedStage = [IO.Path]::GetFullPath($stage)
    if ($resolvedStage.StartsWith($root + '\.setup-', [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $resolvedStage -Recurse -Force
    }
}
