[CmdletBinding()]
param(
    [ValidatePattern('^(dev|v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9][a-zA-Z0-9.-]*)?)$')]
    [string]$Version = 'dev'
)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$dist = Join-Path $root 'dist'
$null = New-Item -ItemType Directory -Path $dist -Force
$stem = "MirrorPort-$Version-windows-x64"
$stage = Join-Path $dist ('.package-' + [guid]::NewGuid().ToString('N'))
$package = Join-Path $stage $stem
$null = New-Item -ItemType Directory -Path $package -Force
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

Push-Location -LiteralPath $root
try {
    $exe = Join-Path $package 'MirrorPort.exe'
    & go build -trimpath -buildvcs=false -o $exe .
    if ($LASTEXITCODE -ne 0) { throw 'MirrorPort build failed.' }

    Copy-Item -LiteralPath 'README.md','setup-airplay.cmd','setup-airplay.ps1' -Destination $package
    $archive = Join-Path $dist "$stem.zip"
    Compress-Archive -LiteralPath $package -DestinationPath $archive -Force
    $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $dist 'SHA256SUMS.txt') -Value "$hash  $stem.zip" -Encoding Ascii
    Write-Host $archive
} finally {
    Pop-Location
    $resolvedStage = [IO.Path]::GetFullPath($stage)
    if ($resolvedStage.StartsWith($dist + '\.package-', [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $resolvedStage -Recurse -Force
    }
}
