#!/usr/bin/env sh
# Builds vault for every platform into dist/. Go standard library only.
set -e
cd "$(dirname "$0")"
VERSION="${VERSION:-0.1.3}"
LD="-s -w -X main.version=$VERSION"
rm -rf dist && mkdir -p dist
export CGO_ENABLED=0

GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LD" -o dist/vault.exe .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LD -H=windowsgui -X main.defaultCmd=gui" -o dist/vaultw.exe .
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$LD" -o dist/vault-macos-arm64 .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$LD" -o dist/vault-macos-amd64 .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$LD" -o dist/vault-linux-amd64 .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$LD" -o dist/vault-linux-arm64 .

ls -lh dist
