#requires -Version 7.0
# Publish the three verified platform artifacts as one immutable RayBox release.
$ErrorActionPreference = 'Stop'
$version = (Get-Content -Raw VERSION).Trim()
if ($version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { throw 'Invalid RayBox version' }
$tag = "v$version"
$sha = $env:GITHUB_SHA
$repository = $env:GITHUB_REPOSITORY
if ($sha -notmatch '^[0-9a-f]{40}$' -or $repository -ne 'roseforljh/RayBox-rx') { throw 'Invalid source identity' }
$platforms = [ordered]@{
    android = @{ root = 'artifacts/libbox'; manifest = 'build.json'; remote = 'android-build.json' }
    windows = @{ root = 'artifacts/windows'; manifest = 'manifest.json'; remote = 'windows-manifest.json' }
    server = @{ root = 'artifacts/server'; manifest = 'manifest.json'; remote = 'server-manifest.json' }
}

function Assert-Identity($manifest) {
    if ($manifest.repository -ne $repository -or $manifest.commit -ne $sha -or
        $manifest.version -ne $version -or $manifest.tag -ne $tag -or $manifest.dirty -ne $false) {
        throw 'Platform manifest identity mismatch'
    }
    if ([string]::IsNullOrWhiteSpace($manifest.sourceDigest) -or
        $manifest.sourceDigestAlgorithm -ne 'sha256-git-path-kind-content-v1' -or
        $manifest.source.url -ne "https://github.com/$repository/tree/$sha" -or
        $manifest.source.runtimeLicense -ne 'docs/reference/runtime/LICENSE' -or
        $manifest.source.protocolLicense -ne 'docs/reference/protocol/LICENSE' -or
        $manifest.rxDefault -ne $false -or $manifest.oobDefault -ne $false) {
        throw 'Platform manifest source provenance mismatch'
    }
}

function Assert-Asset($root, $file, $size, $hash) {
    if ([string]::IsNullOrWhiteSpace($file) -or $file -match '[/\\]' -or $file -in @('.', '..')) { throw 'Invalid asset filename' }
    $path = Join-Path $root $file
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Manifest asset missing: $file" }
    if ((Get-Item -LiteralPath $path).Length -ne $size -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $hash) {
        throw "Manifest asset mismatch: $file"
    }
}

function Assert-Platform($kind, $manifest, $root) {
    Assert-Identity $manifest
    if ($kind -eq 'android') {
        $expected = @("raybox-android-$version-standard.aar", "raybox-android-$version-legacy.aar")
        if (@($manifest.versionedOutputs).Count -ne 2 -or
            (@($manifest.versionedOutputs.name | Sort-Object) -join ',') -ne (@($expected | Sort-Object) -join ',') -or
            $manifest.bundleName -ne "raybox-android-$version-integrated.zip") { throw 'Invalid Android outputs' }
        foreach ($asset in $manifest.versionedOutputs) { Assert-Asset $root $asset.name $asset.size $asset.sha256 }
        if (-not (Test-Path -LiteralPath (Join-Path $root $manifest.bundleName) -PathType Leaf)) { throw 'Integrated bundle missing' }
    } else {
        $expected = if ($kind -eq 'windows') { @('raybox-windows-amd64.exe', 'LICENSE', 'RayBox-Protocol-LICENSE', 'SOURCE.txt') } else {
            @('raybox-linux-amd64', 'raybox-linux-arm64', 'raybox-linux-armv7')
        }
        $actual = @($manifest.assets.psobject.Properties | ForEach-Object { $_.Value.file })
        if ((($actual | Sort-Object) -join ',') -ne (($expected | Sort-Object) -join ',')) { throw "Invalid $kind assets" }
        foreach ($entry in $manifest.assets.psobject.Properties) {
            $asset = $entry.Value
            Assert-Asset $root $asset.file $asset.size $asset.sha256
        }
        if ($kind -eq 'windows' -and ($manifest.fullHead -ne $sha -or $manifest.goos -ne 'windows' -or
            $manifest.goarch -ne 'amd64' -or $manifest.pe.machine -ne 'amd64' -or
            $manifest.goVersion -notmatch '^go version go1\.27\.3 windows/amd64$')) { throw 'Invalid Windows architecture/toolchain' }
        if ($kind -eq 'server' -and ($manifest.source.entry -ne 'cmd/raybox-server' -or
            $manifest.goVersion -ne 'go version go1.27.3 linux/amd64' -or
            (@($manifest.architectures | Sort-Object) -join ',') -ne 'amd64,arm64,armv7')) { throw 'Invalid server architecture/toolchain' }
    }
}

function Invoke-GhJson([string[]]$Arguments, [switch]$AllowMissing) {
    $errorFile = New-TemporaryFile
    try {
        $output = & gh @Arguments 2>$errorFile | Out-String
        if ($LASTEXITCODE -eq 0) { return $(if ($output.Trim()) { $output | ConvertFrom-Json } else { $null }) }
        $message = Get-Content -Raw -LiteralPath $errorFile
        if ($AllowMissing -and $message -match 'HTTP 404') { return $null }
        throw "gh $($Arguments[0..1] -join ' ') failed: $message"
    } finally { Remove-Item -LiteralPath $errorFile -Force }
}

# Validate every platform before even creating a draft.
$mapping = [ordered]@{}
foreach ($kind in $platforms.Keys) {
    $platform = $platforms[$kind]
    $manifest = Get-Content -Raw -LiteralPath (Join-Path $platform.root $platform.manifest) | ConvertFrom-Json
    Assert-Platform $kind $manifest $platform.root
    $files = @(if ($kind -eq 'android') { @($manifest.versionedOutputs.name) + @($manifest.bundleName, 'SHA256SUMS') } elseif ($kind -eq 'windows') {
        @('raybox-windows-amd64.exe')
    } else { @('raybox-linux-amd64', 'raybox-linux-arm64', 'raybox-linux-armv7') })
    $files += @('SOURCE.txt', 'LICENSE', 'RayBox-Protocol-LICENSE', $platform.manifest)
    foreach ($file in $files) {
        $remote = if ($file -eq $platform.manifest) { $platform.remote } elseif ($file -in @('SOURCE.txt', 'LICENSE', 'RayBox-Protocol-LICENSE', 'SHA256SUMS')) { "$kind-$file" } else { $file }
        if ($mapping.Contains($remote)) { throw "Duplicate release asset: $remote" }
        $path = Join-Path $platform.root $file
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Built asset missing: $path" }
        $mapping[$remote] = $path
    }
}
$ref = Invoke-GhJson @('api', "repos/$repository/git/ref/tags/$tag") -AllowMissing
$tagCommit = $null
if ($ref) {
    if ($ref.object.type -eq 'commit') { $tagCommit = $ref.object.sha } elseif ($ref.object.type -eq 'tag') {
        $tagCommit = (Invoke-GhJson @('api', "repos/$repository/git/tags/$($ref.object.sha)")).object.sha
    } else { throw 'Unexpected tag object type' }
    if ($tagCommit -ne $sha) { throw "Tag $tag points to $tagCommit" }
}
$ids = @(gh api --paginate "repos/$repository/releases?per_page=100" --jq ".[] | select(.tag_name == ""$tag"") | .id")
if ($LASTEXITCODE -ne 0) { throw 'Cannot list releases' }
if ($ids.Count -gt 1) { throw "Multiple releases use tag $tag" }
$release = $null
$existing = @()
$published = 'artifacts/published'
$staging = 'artifacts/release'
foreach ($directory in @($published, $staging)) {
    if (Test-Path -LiteralPath $directory) { throw "Release staging must be empty: $directory" }
    New-Item -ItemType Directory -Path $directory | Out-Null
}
if ($ids.Count -eq 1) {
    $release = Invoke-GhJson @('api', "repos/$repository/releases/$($ids[0])")
    if ($release.draft -and $release.target_commitish -ne $sha) { throw "Draft $tag targets $($release.target_commitish)" }
    if (-not $release.draft -and $tagCommit -ne $sha) { throw 'Published release has no tag at this commit' }
    $existing = @($release.assets | ForEach-Object { $_.name })
    if ($existing.Count -gt 0) {
        gh release download $tag --dir $published
        if ($LASTEXITCODE -ne 0) { throw 'Cannot download existing release' }
    }
}
# Keep already uploaded platform manifests after semantic validation, including builtAt.
foreach ($kind in $platforms.Keys) {
    $platform = $platforms[$kind]
    if ($existing -contains $platform.remote) {
        $path = Join-Path $published $platform.remote
        Assert-Platform $kind (Get-Content -Raw -LiteralPath $path | ConvertFrom-Json) $platform.root
        $mapping[$platform.remote] = $path
    }
}
$assets = [ordered]@{}
foreach ($remote in $mapping.Keys) {
    $path = $mapping[$remote]
    if ($existing -contains $remote) {
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne
            (Get-FileHash -LiteralPath (Join-Path $published $remote) -Algorithm SHA256).Hash) { throw "Existing asset differs: $remote" }
    } elseif ($release -and -not $release.draft) { throw "Published release is missing asset: $remote" }
    Copy-Item -LiteralPath $path -Destination (Join-Path $staging $remote)
    $assets[$remote] = [ordered]@{ file = $remote; size = (Get-Item -LiteralPath $path).Length; sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() }
}
$combined = [ordered]@{ schemaVersion = 1; repository = $repository; commit = $sha; version = $version; tag = $tag; dirty = $false; assets = $assets }
$manifestPath = Join-Path $staging 'manifest.json'
if ($existing -contains 'manifest.json') {
    $kept = Get-Content -Raw -LiteralPath (Join-Path $published 'manifest.json') | ConvertFrom-Json
    if ($kept.repository -ne $repository -or $kept.commit -ne $sha -or $kept.version -ne $version -or
        $kept.tag -ne $tag -or $kept.dirty -ne $false -or $kept.schemaVersion -ne 1 -or
        @($kept.assets.psobject.Properties).Count -ne $assets.Count) { throw 'Existing manifest identity mismatch' }
    foreach ($asset in $kept.assets.psobject.Properties) {
        if (-not $assets.Contains($asset.Name) -or $asset.Value.file -ne $asset.Name) { throw 'Unexpected manifest asset' }
        Assert-Asset $staging $asset.Value.file $asset.Value.size $asset.Value.sha256
    }
    Copy-Item -LiteralPath (Join-Path $published 'manifest.json') -Destination $manifestPath
} elseif ($release -and -not $release.draft) { throw 'Existing release is missing manifest.json' } else {
    $combined | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $manifestPath -Encoding utf8
}
if ($release -and -not $release.draft) { Write-Host "Reusing published release $tag"; exit 0 }
$base = "https://github.com/$repository/releases/download/$tag"
$notes = @"
Built from $repository@$sha. All three platforms verified before publication.

## Android
- [Standard AAR]($base/raybox-android-$version-standard.aar)
- [Legacy AAR]($base/raybox-android-$version-legacy.aar)
- [Integrated bundle]($base/raybox-android-$version-integrated.zip)

## Windows
- [Windows amd64]($base/raybox-windows-amd64.exe)

## Linux server
- [amd64]($base/raybox-linux-amd64) / [arm64]($base/raybox-linux-arm64) / [armv7]($base/raybox-linux-armv7)

Verify manifest.json and the platform manifests before use.
"@
if (-not $release) {
    gh release create $tag --target $sha --draft --latest=false --title "RayBox v$version" --notes $notes
    if ($LASTEXITCODE -ne 0) { throw 'Cannot create release draft' }
}
# Upload the combined manifest last so an interrupted draft can retain platform metadata.
foreach ($file in @($mapping.Keys) + @('manifest.json')) {
    if ($existing -contains $file) { continue }
    gh release upload $tag (Join-Path $staging $file)
    if ($LASTEXITCODE -ne 0) { throw "Cannot upload $file" }
}
gh release edit $tag --target $sha --draft=false --latest=false
if ($LASTEXITCODE -ne 0) { throw 'Cannot publish release' }
