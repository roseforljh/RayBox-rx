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
# The combined snapshot has no original Git objects; use source repos containing these baselines.
foreach ($source in @(
    @{ Path = $XrayRepository; Base = '0afec66085a85191e9dd2a4a6646c7c9a94688d1' },
    @{ Path = $SingBoxRepository; Base = '4e30f6b047b18f54ae757c2d2e67dc33627c74ef' }
)) {
    & git -C $source.Path cat-file -e ($source.Base + '^{commit}')
    if ($LASTEXITCODE -ne 0) { throw "Original baseline missing in $($source.Path)" }
}

# A temporary index includes untracked source without staging or changing either repository.
function Export-Patch([string]$Repo, [string]$Base, [string[]]$Paths, [string]$Output) {
    $previousIndex = $env:GIT_INDEX_FILE
    $env:GIT_INDEX_FILE = Join-Path ([IO.Path]::GetTempPath()) (([guid]::NewGuid().ToString('N')) + '.index')
    try {
        & git -C $Repo read-tree $Base
        if ($LASTEXITCODE -ne 0) { throw "Cannot read baseline $Base" }
        & git -C $Repo add --all -- @Paths
        if ($LASTEXITCODE -ne 0) { throw "Cannot collect source from $Repo" }
        & git -C $Repo diff --cached --binary --full-index "--output=$Output" $Base --
        if ($LASTEXITCODE -ne 0) { throw "Cannot export patch from $Repo" }
    } finally {
        Remove-Item -LiteralPath $env:GIT_INDEX_FILE -Force -ErrorAction SilentlyContinue
        $env:GIT_INDEX_FILE = $previousIndex
    }
}

if (-not (Test-Path -LiteralPath (Join-Path $SimpleNet 'third_party/xray.source.json'))) {
    throw 'SimpleNet source lock is missing'
}
$temp = Join-Path ([IO.Path]::GetTempPath()) ('xray-rx-export-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp | Out-Null
try {
    $xrayPatch = Join-Path $temp '0001-xray-rx.patch'
    Export-Patch $XrayRepository '0afec66085a85191e9dd2a4a6646c7c9a94688d1' @(
        'go.mod', 'go.sum', ':(glob)infra/conf/**/*.go',
        ':(glob)transport/internet/**/*.go', ':(glob)transport/internet/**/*.proto',
        ':(exclude,glob)**/*_test.go'
    ) $xrayPatch
    $singPatch = Join-Path $temp '0002-xray-rx.patch'
    Export-Patch $SingBoxRepository '4e30f6b' @('go.mod', 'go.sum', 'protocol/xray') $singPatch
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
    Remove-Item -LiteralPath $temp -Recurse -Force
}
