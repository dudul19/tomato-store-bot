#!/bin/bash
# Build release files into dist/:
#   tomato-store-linux-amd64, tomato-store-linux-arm64, install.sh, version, SHA256SUMS
# Upload everything in dist/ to a GitHub release.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
VERSION=$(tr -d '[:space:]' <VERSION)
rm -rf dist && mkdir -p dist

cd src
unformatted=$(gofmt -l .)
[ -z "$unformatted" ] || { echo "gofmt: $unformatted"; exit 1; }
go vet ./...
go test ./...
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o "$ROOT/dist/tomato-store-linux-$arch" .
done
cd "$ROOT"
cp install.sh dist/install.sh
echo "$VERSION" >dist/version
(cd dist && sha256sum tomato-store-linux-amd64 tomato-store-linux-arm64 install.sh >SHA256SUMS)
ls -la dist
