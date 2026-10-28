@echo off
rem Builds CiteWise AI from .\code into .\build (Windows Command Prompt).
rem Usage: build.bat          (builds build\citewise.exe)
rem        build.bat test     (also runs gofmt check, go vet and go test first)
setlocal
set "ROOT=%~dp0"
cd /d "%ROOT%code" || goto :fail

where go >nul 2>nul
if errorlevel 1 (
    echo Go was not found on PATH. Install Go 1.26+ from https://go.dev/dl/ and reopen this window.
    goto :fail
)

if /i "%~1"=="test" (
    for /f "delims=" %%f in ('gofmt -l .') do (
        echo gofmt needed on: %%f
        goto :fail
    )
    go vet ./... || goto :fail
    go test ./... || goto :fail
)

if not exist "%ROOT%build" mkdir "%ROOT%build"
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o "%ROOT%build\citewise.exe" . || goto :fail
echo Built build\citewise.exe
endlocal
exit /b 0

:fail
echo Build failed.
endlocal
exit /b 1
