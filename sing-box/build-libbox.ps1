# Builds RayBox-rx libbox.aar and libbox-legacy.aar using the embedded ../Xray-core.
# Output: ../artifacts/libbox/ with build.json recording both source states and the AAR hashes.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$xray = Join-Path $root 'Xray-core'
$out = Join-Path $root 'artifacts/libbox'
$env:PATH = (Join-Path $root 'artifacts/toolchain/go/bin') + ';' + $env:PATH
$env:GOTOOLCHAIN = 'local'
$java = (& java -version 2>&1 | Select-Object -First 1).ToString()
if ($java -notmatch '"17\.') { throw "JDK 17 required, found: $java" }

# gomobile child processes fail on Windows' hidden "=C:" variables (same as SimpleNet scripts/run_clean_env.py).
function Invoke-Clean {
    & python -I -c "import os,subprocess,sys;sys.exit(subprocess.run(sys.argv[1:],env={k:v for k,v in os.environ.items() if not k.startswith('=')}).returncode)" @args
    if ($LASTEXITCODE) { throw "failed: $args" }
}

function Get-SourceState([string]$repo) {
    $files = @(git -C $repo -c status.relativePaths=true status --short --untracked-files=all -- . | ForEach-Object { $_.Substring(3) } | Sort-Object)
    [ordered]@{
        commit = (git -C $repo rev-parse HEAD).Trim()
        uncommitted = @($files | ForEach-Object {
            $path = Join-Path $repo $_
            [ordered]@{ path = $_; sha256 = if (Test-Path -LiteralPath $path -PathType Leaf) { (Get-FileHash -LiteralPath $path).Hash.ToLower() } else { 'deleted' } }
        })
    }
}

Push-Location $PSScriptRoot
try {
    Invoke-Clean go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
    Invoke-Clean go install github.com/sagernet/gomobile/cmd/gobind@v0.1.13
    $env:PATH = (Join-Path (go env GOPATH).Trim() 'bin') + ';' + $env:PATH
    Invoke-Clean gomobile init
    Invoke-Clean go run ./cmd/internal/build_libbox -target android
    New-Item -ItemType Directory -Force -Path $out | Out-Null
    Move-Item -Force -LiteralPath libbox.aar, libbox-legacy.aar -Destination $out
    [ordered]@{
        builtAt = (Get-Date).ToUniversalTime().ToString('o')
        go = (go version).Trim()
        java = $java
        singBox = Get-SourceState $PSScriptRoot
        xrayCore = Get-SourceState $xray
        outputs = @(Get-ChildItem -LiteralPath $out -Filter 'libbox*.aar' | Sort-Object Name | ForEach-Object {
            [ordered]@{ name = $_.Name; sha256 = (Get-FileHash -LiteralPath $_.FullName).Hash.ToLower() }
        })
    } | ConvertTo-Json -Depth 6 | Set-Content -Encoding utf8 -LiteralPath (Join-Path $out 'build.json')
    Write-Host "libbox written to $out"
} finally {
    Pop-Location
}
