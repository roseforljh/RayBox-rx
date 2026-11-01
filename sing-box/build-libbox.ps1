# Build Android AARs from this repository's sing-box and adjacent Xray-core.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$out = Join-Path $root 'artifacts/libbox'
$localGo = Join-Path $root 'artifacts/toolchain/go/bin'
if (Test-Path -LiteralPath $localGo -PathType Container) {
    $env:PATH = $localGo + [IO.Path]::PathSeparator + $env:PATH
}
$env:GOTOOLCHAIN = 'local'
$go = (& go version).Trim()
if ($LASTEXITCODE -ne 0 -or $go -notmatch '^go version go1\.27\.3 ') { throw "Go 1.27.3 required, found: $go" }
$java = (& java --version 2>&1 | Select-Object -First 1).ToString()
if ($java -notmatch '^(openjdk|java) 17[. ]') { throw "JDK 17 required, found: $java" }
$commit = (& git -C $root rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Cannot identify RayBox source commit' }

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
    Invoke-Clean go run ./cmd/internal/build_libbox -target android -platform 'android/arm,android/arm64'
    New-Item -ItemType Directory -Force -Path $out | Out-Null
    Move-Item -Force -LiteralPath libbox.aar, libbox-legacy.aar -Destination $out
    [ordered]@{
        schemaVersion = 1
        repository = 'roseforljh/RayBox-rx'
        commit = $commit
        architectures = @('arm64-v8a', 'armeabi-v7a')
        builtAt = (Get-Date).ToUniversalTime().ToString('o')
        go = $go
        java = $java
        outputs = @('libbox.aar', 'libbox-legacy.aar' | ForEach-Object {
            [ordered]@{ name = $_; sha256 = (Get-FileHash -LiteralPath (Join-Path $out $_)).Hash.ToLower() }
        })
    } | ConvertTo-Json -Depth 6 | Set-Content -Encoding utf8 -LiteralPath (Join-Path $out 'build.json')
    Write-Host "RayBox Android AARs written to $out (source $commit)"
} finally {
    Pop-Location
}
