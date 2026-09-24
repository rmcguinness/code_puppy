#!/bin/bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

BUILD_DIR="bin"
BINARY_NAME="code-puppy"
VERSION="2.0.0-go"
LDFLAGS="-s -w -X main.version=${VERSION}"

mkdir -p "$BUILD_DIR"

echo "Building local binary..."
CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}" ./cmd/code-puppy
echo "✅ Built ${BUILD_DIR}/${BINARY_NAME}"

if [ "$1" == "--cross-compile" ] || [ "$1" == "all" ]; then
    echo "Cross-compiling universal binaries..."
    
    echo "• macOS (arm64)..."
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}-darwin-arm64" ./cmd/code-puppy

    echo "• macOS (amd64)..."
    GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}-darwin-amd64" ./cmd/code-puppy

    echo "• Linux (amd64)..."
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}-linux-amd64" ./cmd/code-puppy

    echo "• Linux (arm64)..."
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}-linux-arm64" ./cmd/code-puppy

    echo "• Windows (amd64)..."
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/${BINARY_NAME}-windows-amd64.exe" ./cmd/code-puppy

    echo "✅ All universal static binaries built successfully in ${BUILD_DIR}/"
fi
