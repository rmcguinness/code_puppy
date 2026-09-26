BINARY_NAME=blitz
BUILD_DIR=bin
VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-s -w -X main.version=$(VERSION)
GOFLAGS_BUILD=-trimpath -buildvcs=false

.PHONY: all build install test test-race vet lint vulncheck check clean cross-compile tidy snapshot release-check proto proto-check desktop desktop-check

all: build

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/blitz
	@ln -sf $(BINARY_NAME) $(BUILD_DIR)/blz
	@echo "✅ Built $(BUILD_DIR)/$(BINARY_NAME) (and $(BUILD_DIR)/blz)"

# Installs blitz, and blz beside it, into $$GOBIN (or $$GOPATH/bin).
install: build
	@dir="$$(go env GOBIN)"; [ -n "$$dir" ] || dir="$$(go env GOPATH)/bin"; \
	mkdir -p "$$dir" && cp $(BUILD_DIR)/$(BINARY_NAME) "$$dir/" && ln -sf $(BINARY_NAME) "$$dir/blz" && \
	echo "✅ Installed $$dir/$(BINARY_NAME) and $$dir/blz"

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

# staticcheck and govulncheck are pinned in tools/go.mod. Every package's
# tests also fail on a leaked goroutine (goleak, in each leak_test.go).
lint:
	go tool -modfile=tools/go.mod staticcheck ./...

vulncheck:
	go tool -modfile=tools/go.mod govulncheck ./...

check: vet lint vulncheck test-race

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check

tidy:
	go mod tidy

# The service API: api/blitz/v1/*.proto -> internal/gen. The tools are
# pinned in tools/go.mod.
BUF=go tool -modfile=tools/go.mod buf

# protoc-gen-es, for the desktop app's TypeScript, comes from web/desktop.
web/desktop/node_modules: web/desktop/pnpm-lock.yaml
	cd web/desktop && pnpm install --frozen-lockfile
	touch $@

proto: web/desktop/node_modules
	$(BUF) lint
	$(BUF) format -w
	$(BUF) generate

# Fails when the protos aren't formatted or internal/gen is out of date.
proto-check: web/desktop/node_modules
	$(BUF) lint
	$(BUF) format --exit-code -d
	$(BUF) generate
	git diff --exit-code -- internal/gen web/desktop/src/gen
	test -z "$$(git ls-files --others --exclude-standard -- internal/gen web/desktop/src/gen)"

clean:
	rm -rf $(BUILD_DIR)

cross-compile: clean
	@mkdir -p $(BUILD_DIR)
	@echo "Building for macOS (arm64)..."
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/blitz
	@echo "Building for macOS (amd64)..."
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/blitz
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/blitz
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/blitz
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/blitz
	@echo "✅ All cross-compiled universal binaries created in $(BUILD_DIR)/"

# The desktop app (cmd/blitz-desktop, its own module; the page is
# web/desktop): build/desktop/bin. Needs cgo, pnpm, and on Linux
# webkit2gtk. The Wails CLI is pinned in tools/go.mod.
WAILS=go tool -modfile=../../tools/go.mod wails

DESKTOP_APP=build/desktop/bin/blitz-desktop.app

# The CLI is bundled next to the app's binary, where the app looks for it
# to install the service; on macOS the app is then signed again (ad hoc),
# since adding a file breaks Wails's signature.
desktop: web/desktop/node_modules
	cd cmd/blitz-desktop && CGO_CFLAGS=-mmacosx-version-min=13.0 CGO_LDFLAGS=-mmacosx-version-min=13.0 $(WAILS) build -clean
	@if [ -d "$(DESKTOP_APP)" ]; then \
		CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o "$(DESKTOP_APP)/Contents/MacOS/blitz" ./cmd/blitz && \
		codesign --force --deep --sign - "$(DESKTOP_APP)"; \
	else \
		CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -ldflags="$(LDFLAGS)" -o build/desktop/bin/blitz ./cmd/blitz; \
	fi
	@echo "✅ Built build/desktop/bin (with the blitz CLI bundled)"

desktop-check: web/desktop/node_modules
	cd web/desktop && pnpm test && pnpm run build
	cd cmd/blitz-desktop && go vet . && go test -race ./...
