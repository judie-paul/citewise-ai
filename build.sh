#!/usr/bin/env sh
# Builds CiteWise AI from ./code into ./build (macOS, Linux, Git Bash).
# Usage: ./build.sh          (builds build/citewise, or build/citewise.exe on Windows)
#        ./build.sh --test   (also runs gofmt check, go vet and go test first)
set -eu
root=$(cd "$(dirname "$0")" && pwd)
cd "$root/code"

if [ "${1:-}" = "--test" ]; then
  unformatted=$(gofmt -l .)
  if [ -n "$unformatted" ]; then
    echo "gofmt needed on: $unformatted" >&2
    exit 1
  fi
  go vet ./...
  go test ./...
fi

out="$root/build/citewise"
if [ "$(go env GOOS)" = "windows" ]; then
  out="$out.exe"
fi
mkdir -p "$root/build"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$out" .
echo "Built $out"
