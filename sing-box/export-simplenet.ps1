[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$SimpleNet,
    [Parameter(Mandatory)][string]$XrayRepository,
    [Parameter(Mandatory)][string]$SingBoxRepository
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$SimpleNet = (Resolve-Path -LiteralPath $SimpleNet).Path
$XrayRepository = (Resolve-Path -LiteralPath $XrayRepository).Path
$SingBoxRepository = (Resolve-Path -LiteralPath $SingBoxRepository).Path
$sourceRoot = Split-Path -Parent $PSScriptRoot
# The combined snapshot has no original Git objects; use source repos containing these baselines.
foreach ($source in @(
    @{ Path = $XrayRepository; Base = 'a4a3dff5b20c2faae567b742c172c7b95e75c083' },
    @{ Path = $SingBoxRepository; Base = '4e30f6b047b18f54ae757c2d2e67dc33627c74ef' }
)) {
    & git -C $source.Path cat-file -e ($source.Base + '^{commit}')
    if ($LASTEXITCODE -ne 0) { throw "Original baseline missing in $($source.Path)" }
}

# Read baseline objects from the archive, but export the current combined source.
# All new Git objects and the index belong to an isolated temporary clone.
function Export-Patch([string]$Repo, [string]$CurrentSource, [string]$Base, [string[]]$Paths, [string]$Output) {
    $isolated = Join-Path $temp ([guid]::NewGuid().ToString('N'))
    & git clone --shared --no-checkout --quiet -- $Repo $isolated
    if ($LASTEXITCODE -ne 0) { throw "Cannot read baseline from $Repo" }
    $gitDirectory = Join-Path $isolated '.git'
    $gitOptions = @('-C', $CurrentSource, "--git-dir=$gitDirectory", "--work-tree=$CurrentSource")
    & git @gitOptions read-tree $Base
        if ($LASTEXITCODE -ne 0) { throw "Cannot read baseline $Base" }
        & git @gitOptions add --all -- @Paths
        if ($LASTEXITCODE -ne 0) { throw "Cannot collect source from $Repo" }
        & git @gitOptions diff --cached --binary --full-index "--output=$Output" $Base -- @Paths
        if ($LASTEXITCODE -ne 0) { throw "Cannot export patch from $Repo" }
}

if (-not (Test-Path -LiteralPath (Join-Path $SimpleNet 'third_party/xray.source.json'))) {
    throw 'SimpleNet source lock is missing'
}
$temp = Join-Path ([IO.Path]::GetTempPath()) ('xray-rx-export-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp | Out-Null
try {
    $xrayPatch = Join-Path $temp '0001-xray-rx.patch'
    Export-Patch $XrayRepository (Join-Path $sourceRoot 'Xray-core') 'a4a3dff5b20c2faae567b742c172c7b95e75c083' @(
        'go.mod', 'go.sum', ':(glob)infra/conf/**/*.go',
        ':(glob)transport/internet/**/*.go', ':(glob)transport/internet/**/*.proto',
        ':(exclude,glob)**/*_test.go'
    ) $xrayPatch
    $singPatch = Join-Path $temp '0002-xray-rx.patch'
    Export-Patch $SingBoxRepository $PSScriptRoot '4e30f6b' @(
        'go.mod', 'go.sum', 'protocol/xray', 'common/dialer/default.go', 'option/outbound.go',
        ':(exclude,glob)**/*_test.go'
    ) $singPatch
    $content = [IO.File]::ReadAllText($singPatch)
    $localReplace = '+replace github.com/xtls/xray-core => ../Xray-core'
    if (-not $content.Contains($localReplace)) { throw 'Local Xray replace directive is missing from patch' }
    [IO.File]::WriteAllText($singPatch, $content.Replace($localReplace,
        '+replace github.com/xtls/xray-core => ../sing-box-build/xray-core'), [Text.UTF8Encoding]::new($false))
    foreach ($patch in @($xrayPatch, $singPatch)) {
        if ((Get-Item -LiteralPath $patch).Length -eq 0) { throw "Empty patch: $patch" }
    }
    $xrayDestination = Join-Path $SimpleNet 'patches/xray-core'
    New-Item -ItemType Directory -Force -Path $xrayDestination | Out-Null
    Copy-Item -LiteralPath $xrayPatch -Destination $xrayDestination -Force
    Copy-Item -LiteralPath $singPatch -Destination (Join-Path $SimpleNet 'patches/sing-box') -Force
    $oldPatch = Join-Path $SimpleNet 'patches/sing-box/0002-xray-websocket-source.patch'
    if (Test-Path -LiteralPath $oldPatch) { Remove-Item -LiteralPath $oldPatch }
    Write-Host 'Exported build source only; Xray tests, docs, scripts and artifacts are excluded.'
} finally {
    $resolvedTemp = [IO.Path]::GetFullPath($temp)
    $allowedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedTemp.StartsWith($allowedTemp, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing cleanup outside the temporary directory'
    }
    Remove-Item -LiteralPath $temp -Recurse -Force
}
