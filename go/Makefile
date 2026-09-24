BINARY_NAME=code-puppy
BUILD_DIR=bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-s -w -X main.version=$(VERSION)
GOFLAGS_BUILD=-trimpath -buildvcs=false

.PHONY: all build test test-race vet check clean cross-compile tidy snapshot release-check

all: build

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/code-puppy
	@echo "✅ Built $(BUILD_DIR)/$(BINARY_NAME)"

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

check: vet test-race

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR)

cross-compile: clean
	@mkdir -p $(BUILD_DIR)
	@echo "Building for macOS (arm64)..."
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/code-puppy
	@echo "Building for macOS (amd64)..."
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/code-puppy
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/code-puppy
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/code-puppy
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/code-puppy
	@echo "✅ All cross-compiled universal binaries created in $(BUILD_DIR)/"
