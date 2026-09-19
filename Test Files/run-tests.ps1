$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempRoot = Join-Path $tempBase ("pokemon-violet-tests-" + [guid]::NewGuid().ToString('N'))
$pushed = $false

try {
    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    Get-ChildItem -LiteralPath $repoRoot -File -Filter '*.go' |
        Copy-Item -Destination $tempRoot
    Copy-Item -LiteralPath (Join-Path $repoRoot 'go.mod'), (Join-Path $repoRoot 'go.sum') -Destination $tempRoot
    Copy-Item -LiteralPath (Join-Path $repoRoot 'proto') -Destination $tempRoot -Recurse
    Get-ChildItem -LiteralPath $PSScriptRoot -File -Filter '*_test.go' |
        Copy-Item -Destination $tempRoot

    Push-Location $tempRoot
    $pushed = $true
    & go test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed with exit code $LASTEXITCODE" }
}
finally {
    if ($pushed) { Pop-Location }
    $resolved = [IO.Path]::GetFullPath($tempRoot)
    if ($resolved.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -and
        (Test-Path -LiteralPath $resolved)) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
