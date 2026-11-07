.PHONY: build build-all test test-race test-cover test-scripts cover-gate lint vet fmt golangci install clean hooks

MODULE    := github.com/samestrin/llm-extract-axi
BUILD_DIR := build
DIST_DIR  := dist

# The fallback is not "dev", because "dev" is the unstamped default in
# commands.Version: a build that failed to stamp must not look like one that did.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-unknown")

# The CLI version lives on commands.Version, not on main: the cobra root reads
# it from there, so stamping main would leave `llm-extract --version` reporting
# the default. main.serverVersion is for the MCP server; -X on a main package
# that lacks the variable is a no-op.
LDFLAGS := -ldflags "-s -w -X $(MODULE)/internal/extract/commands.Version=$(VERSION) -X main.serverVersion=$(VERSION)"

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

# Every main package under ./cmd lands in ./build under its directory name, so
# a new binary needs no edit here.
build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/ ./cmd/...

build-all:
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		for dir in cmd/*/; do \
			name=$$(basename $$dir); \
			echo "Building $$name $$os/$$arch..."; \
			GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $(DIST_DIR)/$$name-$$os-$$arch$$ext ./$$dir || exit 1; \
		done; \
	done

# The end-to-end tests exec the binaries in ./build, so every test target
# builds first.
test: build
	go test ./...

test-race: build
	go test -race ./...

test-cover: build
	go test -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -func=coverage.txt | tail -1

# Shell tests for the build/CI scripts (the coverage gate's failure branches).
# Self-contained: it builds a throwaway module in a temp dir and makes no
# network call. Wired into CI as its own step.
test-scripts:
	./scripts/coverage-gate_test.sh

# The same 80% gate CI enforces. Measured the way CI measures it and the way
# AGENTS.md prescribes: cold (`go clean -testcache`) and with the LLM
# environment stripped, so a cached run cannot write a partial profile that
# reads low, and an exported API key cannot push a test down a path that the
# gated number then counts. It builds first because the end-to-end tests exec
# ./build. It does NOT reuse test-cover, which stays a warm interactive run.
cover-gate: build
	go clean -testcache
	env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u OPENAI_BASE_URL -u OPENAI_MODEL \
		go test -coverprofile=coverage.txt -covermode=atomic ./...
	./scripts/coverage-gate.sh coverage.txt 80

lint: vet fmt golangci

vet:
	go vet ./...

# Fails when any file needs gofmt -s, rather than only listing it.
fmt:
	@unformatted=$$(gofmt -s -l .); \
	if [ -n "$$unformatted" ]; then echo "These files need gofmt -s:"; echo "$$unformatted"; exit 1; fi

golangci:
	golangci-lint run ./...

install: build
	@echo "Installing to /usr/local/bin (may require sudo)..."
	@for f in $(BUILD_DIR)/*; do install -m 755 $$f /usr/local/bin/$$(basename $$f); done

hooks:
	git config core.hooksPath .githooks

clean:
	rm -rf $(BUILD_DIR) $(DIST_DIR) coverage.txt coverage.html
