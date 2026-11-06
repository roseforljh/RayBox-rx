#requires -Version 7.0
# Build Android AARs from this repository's sing-box and adjacent Xray-core.
param([switch]$AllowDirty)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$out = Join-Path $root 'artifacts/libbox'
$checker = Join-Path $root 'tools/check_android_artifacts.py'
$localGo = Join-Path $root 'artifacts/toolchain/go/bin'
if (Test-Path -LiteralPath $localGo -PathType Container) {
    $env:PATH = $localGo + [IO.Path]::PathSeparator + $env:PATH
}
$env:GOTOOLCHAIN = 'local'
$go = (& go version).Trim()
if ($LASTEXITCODE -ne 0 -or $go -notmatch '^go version go1\.27\.3 ') { throw "Go 1.27.3 required, found: $go" }
$java = (& java --version 2>&1 | Select-Object -First 1).ToString()
if ($java -notmatch '^(openjdk|java) 17[. ]') { throw "JDK 17 required, found: $java" }
$source = & python -I $checker source $root | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Cannot identify RayBox source snapshot' }
if ($source.dirty -and -not $AllowDirty) { throw 'Release builds require clean source; use -AllowDirty for local candidates only' }
if ($source.dirty) { $out = Join-Path $root 'artifacts/libbox-candidate' }
$sdk = @($env:ANDROID_HOME, $env:ANDROID_SDK_ROOT, $(if ($env:LOCALAPPDATA) {
    Join-Path $env:LOCALAPPDATA 'Android/Sdk'
})) | Where-Object { $_ -and (Test-Path -LiteralPath (Join-Path $_ 'licenses/android-sdk-license')) } |
    Select-Object -First 1
if (-not $sdk) { throw 'Android SDK not found; set ANDROID_HOME' }
$ndkVersion = '29.0.14206865'
$ndk = Join-Path $sdk "ndk/$ndkVersion"
if (-not (Test-Path -LiteralPath (Join-Path $ndk 'source.properties'))) { throw "Android NDK $ndkVersion required" }
$ndkProperties = Get-Content -Raw -LiteralPath (Join-Path $ndk 'source.properties')
if ($ndkProperties -notmatch '(?m)^Pkg\.Revision\s*=\s*29\.0\.14206865\s*$') {
    throw "Android NDK metadata does not match $ndkVersion"
}
$env:ANDROID_HOME = $sdk
$env:ANDROID_NDK_HOME = $ndk

# Windows may supply hidden drive variables which gomobile cannot pass to Go.
function Invoke-Clean {
    & python -I -c "import os,subprocess,sys;sys.exit(subprocess.run(sys.argv[1:],env={k:v for k,v in os.environ.items() if not k.startswith('=')}).returncode)" @args
    if ($LASTEXITCODE -ne 0) { throw "failed: $args" }
}

Push-Location $PSScriptRoot
try {
    Invoke-Clean go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
    Invoke-Clean go install github.com/sagernet/gomobile/cmd/gobind@v0.1.13
    $env:PATH = (Join-Path (go env GOPATH).Trim() 'bin') + [IO.Path]::PathSeparator + $env:PATH
    Invoke-Clean gomobile init
    Invoke-Clean go run ./cmd/internal/build_libbox -target android -platform 'android/arm,android/arm64,android/386,android/amd64'
    $aarReport = & python -I $checker aar libbox.aar libbox-legacy.aar --require-safety
    if ($LASTEXITCODE -ne 0) { throw 'AAR ZIP/ABI/ELF/API/safety validation failed; candidate has not been promoted' }
    $outputs = ConvertFrom-Json -InputObject ($aarReport -join "`n") -NoEnumerate
    if ($outputs.Count -ne 2 -or $outputs[0].name -ne 'libbox.aar' -or
        $outputs[1].name -ne 'libbox-legacy.aar') { throw 'Invalid AAR report shape' }
    $after = & python -I $checker source $root | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or $after.commit -ne $source.commit -or
        $after.sourceDigest -ne $source.sourceDigest -or $after.dirty -ne $source.dirty) {
        throw 'Source changed during build; candidate has not been promoted'
    }
    $buildEntry = Get-Content -Raw -LiteralPath 'cmd/internal/build_libbox/main.go'
    $tagLine = ($buildEntry -split "`r?`n" | Where-Object { $_.Contains('sharedTags = append(sharedTags,') } | Select-Object -First 1)
    $tags = @([regex]::Matches($tagLine, '"([^"]+)"') | ForEach-Object { $_.Groups[1].Value })
    if (-not $tags -or $tags -contains 'with_tailscale') { throw 'Unexpected Android build tags' }
    $versionSource = Get-Content -Raw -LiteralPath (Join-Path $root 'Xray-core/core/core.go')
    $xrayVersionParts = @('x', 'y', 'z') | ForEach-Object {
        $match = [regex]::Match($versionSource, "Version_$_ byte = (\d+)")
        if (-not $match.Success) { throw "Cannot read Xray version component $_" }
        $match.Groups[1].Value
    }
    $xrayVersion = $xrayVersionParts -join '.'
    $version = (& go run ./cmd/internal/read_tag).Trim()
    if ($LASTEXITCODE -ne 0 -or $version -notmatch '^\d+\.\d+\.\d+') {
        throw "Invalid sing-box version: $version"
    }
    $staging = Join-Path $root ('artifacts/libbox-build-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $staging | Out-Null
    Move-Item -LiteralPath libbox.aar, libbox-legacy.aar -Destination $staging
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'LICENSE') -Destination (Join-Path $staging 'LICENSE')
    Copy-Item -LiteralPath (Join-Path $root 'Xray-core/LICENSE') -Destination (Join-Path $staging 'Xray-LICENSE')
    [IO.File]::WriteAllText((Join-Path $staging 'SOURCE.txt'),
        "RayBox Android core`nCommit: $($source.commit)`nSource digest: $($source.sourceDigest)`n`n" +
        [IO.File]::ReadAllText((Join-Path $root 'SOURCE.md')))
    [ordered]@{
        schemaVersion = 1
        repository = 'roseforljh/RayBox-rx'
        commit = $source.commit
        tag = $(if ($source.dirty) { $null } else { "android-$($source.commit)" })
        dirty = $source.dirty
        sourceDigest = $source.sourceDigest
        sourceDigestAlgorithm = $source.sourceDigestAlgorithm
        architectures = @('arm64-v8a', 'armeabi-v7a', 'x86', 'x86_64')
        singBoxVersion = $version
        xrayVersion = $xrayVersion
        builtAt = (Get-Date).ToUniversalTime().ToString('o')
        go = $go
        java = $java
        gomobile = 'v0.1.13'
        ndk = $ndkVersion
        androidApi = @{ 'libbox.aar' = 24; 'libbox-legacy.aar' = 21 }
        buildTags = @{ 'libbox.aar' = $tags; 'libbox-legacy.aar' = @($tags | Where-Object { $_ -ne 'with_naive_outbound' }) }
        rxDefault = $false
        oobDefault = $false
        source = @{
            url = "https://github.com/roseforljh/RayBox-rx/tree/$($source.commit)"
            singBoxLicense = 'sing-box/LICENSE'
            xrayLicense = 'Xray-core/LICENSE'
        }
        outputs = $outputs
    } | ConvertTo-Json -Depth 10 | Set-Content -Encoding utf8 -LiteralPath (Join-Path $staging 'build.json')
    $finalSource = & python -I $checker source $root | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or $finalSource.commit -ne $source.commit -or
        $finalSource.sourceDigest -ne $source.sourceDigest -or $finalSource.dirty -ne $source.dirty) {
        throw 'Source changed before candidate promotion'
    }
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
    Write-Host "RayBox Android AARs verified at $out (source $($source.commit), dirty=$($source.dirty))"
} finally {
    Pop-Location
}
