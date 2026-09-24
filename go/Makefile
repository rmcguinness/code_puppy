BINARY_NAME=code-puppy
BUILD_DIR=bin
VERSION=2.0.0-go
LDFLAGS=-s -w -X main.version=$(VERSION)

.PHONY: all build test clean cross-compile tidy

all: build

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/code-puppy
	@echo "✅ Built $(BUILD_DIR)/$(BINARY_NAME)"

test:
	CGO_ENABLED=0 go test -v ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR)

cross-compile: clean
	@mkdir -p $(BUILD_DIR)
	@echo "Building for macOS (arm64)..."
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/code-puppy
	@echo "Building for macOS (amd64)..."
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/code-puppy
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/code-puppy
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/code-puppy
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/code-puppy
	@echo "✅ All cross-compiled universal binaries created in $(BUILD_DIR)/"
