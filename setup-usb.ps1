[CmdletBinding()]
param([string]$ArchivePath)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$package = 'iPhoneMirror-v1.8.3-win-x64'
$release = "https://github.com/RayrenSX/iPhoneMirror/releases/download/v1.8.3/$package.zip"
$archiveHash = '97C5DED39A9437069CB24FD4EE59FE9775C1EBCF2700E3FD6305C9E424396BF0'
$coreHash = '12FCAC3AB0E2B86DFD46FBA76F1992916F55C5AE7E530E2A6B70E1EA68D6BD4F'
$helperHash = '3F23BCCAC19A1FBA47E971BF632529DCB39AEC1C4A06DC9F7783061786C2BCE8'
$helperName = 'iPhoneMirror.UsbConfigurationSwitch.exe'
$usbFolder = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'engines\usb'))
$target = Join-Path $usbFolder $package
$helperPath = Join-Path $PSScriptRoot $helperName

function Test-ExpectedFile([string]$Path, [string]$Hash) {
    return (Test-Path -LiteralPath $Path -PathType Leaf) -and
        ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -eq $Hash)
}

if ((Test-Path -LiteralPath $helperPath) -and !(Test-ExpectedFile $helperPath $helperHash)) {
    throw "A different USB helper already exists at $helperPath. Move it aside before running setup."
}

if (!(Test-Path -LiteralPath $target)) {
    $null = New-Item -ItemType Directory -Path $usbFolder -Force
    $stage = Join-Path $usbFolder ('.setup-' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $stage
    try {
        if ($ArchivePath) {
            $archive = (Resolve-Path -LiteralPath $ArchivePath).ProviderPath
        } else {
            $archive = Join-Path $stage "$package.zip"
            Write-Host "Downloading the USB runtime: $release"
            Invoke-WebRequest -UseBasicParsing -Uri $release -OutFile $archive -TimeoutSec 180
        }
        if (!(Test-ExpectedFile $archive $archiveHash)) {
            throw 'The archive does not match the official SHA-256.'
        }
        $expanded = Join-Path $stage 'expanded'
        Expand-Archive -LiteralPath $archive -DestinationPath $expanded
        [IO.Directory]::Move((Join-Path $expanded $package), $target)
    } finally {
        $resolvedStage = [IO.Path]::GetFullPath($stage)
        if ($resolvedStage.StartsWith($usbFolder + '\.setup-', [StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $resolvedStage -Recurse -Force
        }
    }
}

$sourceHelper = Join-Path $target $helperName
if (!(Test-ExpectedFile (Join-Path $target 'iPhoneMirror.Core.dll') $coreHash) -or
    !(Test-ExpectedFile $sourceHelper $helperHash)) {
    throw "The USB runtime at $target is incomplete or has a different version."
}
if (!(Test-Path -LiteralPath $helperPath)) {
    [IO.File]::Copy($sourceHelper, $helperPath)
}
Write-Host "USB runtime is ready: $target"
Write-Host "For device-specific driver setup, open $target\iPhoneMirror.Driver.exe with the iPhone connected and unlocked."
