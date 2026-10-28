# Builds CiteWise AI from ./code into ./build (Windows PowerShell).
# Usage: .\build.ps1            (builds build\citewise.exe)
#        .\build.ps1 -Test      (also runs gofmt check, go vet and go test first)
param([switch]$Test)
$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Push-Location (Join-Path $root "code")
try {
    if ($Test) {
        $unformatted = gofmt -l .
        if ($unformatted) { throw "gofmt needed on: $unformatted" }
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed" }
    }
    New-Item -ItemType Directory -Force (Join-Path $root "build") | Out-Null
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags "-s -w" -o (Join-Path $root "build\citewise.exe") .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "Built build\citewise.exe"
}
finally {
    Pop-Location
}
