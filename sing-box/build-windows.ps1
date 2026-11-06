#requires -Version 7.0
# Build a native Windows sing-box shell with the local Xray module.
[CmdletBinding()]
param(
    [switch]$AllowDirty
)

$ErrorActionPreference = 'Stop'
$singBox = $PSScriptRoot
$root = Split-Path -Parent $singBox
$out = Join-Path $root 'artifacts/windows'
$localGo = Join-Path $root 'artifacts/toolchain/go/bin'
if (Test-Path -LiteralPath $localGo -PathType Container) {
    $env:PATH = $localGo + [IO.Path]::PathSeparator + $env:PATH
}
$env:GOTOOLCHAIN = 'local'
$env:GOWORK = 'off'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

function Invoke-Checked {
    param([string]$Command, [string[]]$Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Command failed ($LASTEXITCODE): $Command $($Arguments -join ' ')" }
}

function Git-Value {
    param([string[]]$Arguments)
    $value = (& git -C $root @Arguments).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Git command failed: $($Arguments -join ' ')" }
    return $value
}

function Get-PeInfo {
    param([string]$Path)
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 0x40 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw 'Built file is not a DOS executable'
    }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 0 -or $peOffset + 26 -gt $bytes.Length -or
        $bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45 -or
        $bytes[$peOffset + 2] -ne 0 -or $bytes[$peOffset + 3] -ne 0) {
        throw 'Built file is not a PE executable'
    }
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    $optionalMagic = [BitConverter]::ToUInt16($bytes, $peOffset + 24)
    if ($machine -ne 0x8664 -or $optionalMagic -ne 0x20b) {
        throw ('Unexpected PE architecture: machine 0x{0:X4}, optional magic 0x{1:X}' -f $machine, $optionalMagic)
    }
    return [ordered]@{ format = 'PE32+'; machine = 'amd64'; machineCode = '0x8664' }
}

$go = (& go version).Trim()
if ($LASTEXITCODE -ne 0 -or $go -notmatch '^go version go1\.27\.3 ') { throw "Go 1.27.3 required, found: $go" }
$head = Git-Value @('rev-parse', 'HEAD')
$repository = 'roseforljh/RayBox-rx'
$sourceCheck = Join-Path $root 'tools/check_android_artifacts.py'
if (-not (Test-Path -LiteralPath $sourceCheck -PathType Leaf)) { throw 'Source identity checker is required' }
$sourceInfo = (& python -I $sourceCheck source $root | ConvertFrom-Json)
if ($LASTEXITCODE -ne 0) { throw 'Cannot capture source identity before build' }
if ($sourceInfo.commit -ne $head -or ($sourceInfo.dirty -and -not $AllowDirty)) {
    throw 'Source identity mismatch or dirty source without -AllowDirty'
}
$outName = if ($sourceInfo.dirty) { 'windows-candidate' } else { 'windows' }
$out = Join-Path $root ('artifacts/' + $outName)
$staging = Join-Path $root ('artifacts/windows-build-' + [Guid]::NewGuid().ToString('N'))
$sourceDigest = $sourceInfo.sourceDigest
$sourceDigestAlgorithm = $sourceInfo.sourceDigestAlgorithm
Push-Location $singBox
try {
    $commitVersion = (& go run ./cmd/internal/read_tag).Trim()
} finally {
    Pop-Location
}
if ($LASTEXITCODE -ne 0 -or $commitVersion -notmatch '^\d+\.\d+\.\d+') { throw "Invalid sing-box version: $commitVersion" }
if (-not $commitVersion.Contains($head.Substring(0, 12))) {
    $commitVersion += '-' + $head.Substring(0, 12)
}
$tagsFile = Join-Path $singBox 'release/DEFAULT_BUILD_TAGS_WINDOWS'
$buildTags = ((Get-Content -LiteralPath $tagsFile -Raw).Trim() -split ',' | Where-Object { $_ -ne 'with_tailscale' }) -join ','
if ([string]::IsNullOrWhiteSpace($buildTags) -or $buildTags -match '(^|,)with_tailscale(,|$)') {
    throw 'Windows build tags must be non-Tailscale portable tags'
}

New-Item -ItemType Directory -Path $staging | Out-Null
$exe = Join-Path $staging 'sing-box-windows-amd64.exe'
$manifestPath = Join-Path $staging 'manifest.json'
$licensePath = Join-Path $staging 'LICENSE'
$xrayLicensePath = Join-Path $staging 'Xray-LICENSE'
$sourcePath = Join-Path $staging 'SOURCE.txt'

Push-Location $singBox
try {
    $ldflags = "-X github.com/sagernet/sing-box/constant.Version=$commitVersion -X runtime.godebugDefault=multipathtcp=0,tlssha1=1 -checklinkname=0 -s -w -buildid="
    Invoke-Checked 'go' @('build', '-trimpath', '-buildvcs=false', '-tags', $buildTags, '-ldflags', $ldflags, '-o', $exe, './cmd/sing-box')
} finally {
    Pop-Location
}

$pe = Get-PeInfo $exe
Invoke-Checked 'python' @('-I', (Join-Path $root 'tools/check_windows_runtime.py'), $exe)

$xrayCore = Join-Path $root 'Xray-core/core/core.go'
$xrayText = Get-Content -LiteralPath $xrayCore -Raw
$parts = foreach ($name in @('Version_x', 'Version_y', 'Version_z')) {
    $match = [regex]::Match($xrayText, "(?m)^\s*$name\s+byte\s*=\s*(\d+)")
    if (-not $match.Success) { throw "Cannot read Xray $name" }
    $match.Groups[1].Value
}
$xrayVersion = $parts -join '.'
$runtimeVersion = (& $exe version) -join "`n"
if ($LASTEXITCODE -ne 0 -or -not $runtimeVersion.Contains("sing-box version $commitVersion") -or
    -not $runtimeVersion.Contains("Xray version $xrayVersion")) {
    throw 'Windows executable does not report the expected dual-core versions'
}
[IO.File]::WriteAllText($sourcePath, "RayBox Windows core`nCommit: $head`nSource digest: $sourceDigest`n`n" +
    [IO.File]::ReadAllText((Join-Path $root 'SOURCE.md')))
$assetFiles = @($exe, $licensePath, $xrayLicensePath, $sourcePath)
Copy-Item -LiteralPath (Join-Path $singBox 'LICENSE') -Destination $licensePath
Copy-Item -LiteralPath (Join-Path $root 'Xray-core/LICENSE') -Destination $xrayLicensePath
$assets = [ordered]@{}
foreach ($path in $assetFiles) {
    $file = Split-Path -Leaf $path
    $assets[$file] = [ordered]@{ file = $file; size = (Get-Item -LiteralPath $path).Length; sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() }
}
$manifest = [ordered]@{
    schemaVersion = 1
    repository = $repository
    commit = $head
    fullHead = $head
    tag = $(if ($sourceInfo.dirty) { $null } else { "windows-$head" })
    dirty = [bool]$sourceInfo.dirty
    sourceDigest = $sourceDigest
    sourceDigestAlgorithm = $sourceDigestAlgorithm
    source = @{
        url = "https://github.com/$repository/tree/$head"
        singBoxLicense = 'sing-box/LICENSE'
        xrayLicense = 'Xray-core/LICENSE'
    }
    singBoxVersion = $commitVersion
    xrayVersion = $xrayVersion
    go = $go
    goVersion = $go
    goos = 'windows'
    goarch = 'amd64'
    buildTags = @($buildTags -split ',')
    rxDefault = $false
    oobDefault = $false
    features = [ordered]@{ rxDefault = $false; oobDefault = $false }
    pe = $pe
    assets = $assets
}
$sourceAfter = (& python -I $sourceCheck source $root | ConvertFrom-Json)
if ($LASTEXITCODE -ne 0 -or $sourceAfter.commit -ne $head -or $sourceAfter.dirty -ne $sourceInfo.dirty -or $sourceAfter.sourceDigest -ne $sourceInfo.sourceDigest) {
    throw 'Source changed during Windows build'
}
$manifest | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $manifestPath -Encoding utf8
$backup = $out + '.backup-' + [Guid]::NewGuid().ToString('N')
$hadOutput = Test-Path -LiteralPath $out
if ($hadOutput) { Move-Item -LiteralPath $out -Destination $backup }
try {
    Move-Item -LiteralPath $staging -Destination $out
} catch {
    if ($hadOutput) { Move-Item -LiteralPath $backup -Destination $out }
    throw
}
if ($hadOutput) { Remove-Item -Recurse -Force -LiteralPath $backup }
Write-Host "Windows artifact written to $out"
Write-Host "Version: $commitVersion; Xray: $xrayVersion; PE: $($pe.machine); SHA256: $($assets[$(Split-Path -Leaf $exe)].sha256)"
