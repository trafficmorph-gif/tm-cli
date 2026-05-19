# tm CLI build / test targets.
#
# Quickstart:
#   make build        compile the binary into bin/tm
#   make test         run all Go tests
#   make all          build + test (default CI target)

VERSION ?= dev
SPEC_VERSION = v1

# oapi-codegen version is pinned here, not vendored as a binary —
# `go run` resolves it from Go's module cache on first use and from
# cache afterwards. Bumping this is a one-line change tracked in git
# history, and works identically across darwin/linux/amd64/arm64
# without checking in platform-specific binaries.
OAPI_CODEGEN_VERSION = v2.7.0

BIN_DIR := bin
TM_BINARY := $(BIN_DIR)/tm

LDFLAGS := -X 'github.com/trafficmorph-gif/tm-cli/internal/cli.CLIVersion=$(VERSION)'

.PHONY: all build test lint clean regen-client

all: build test

# Phony build: defer all up-to-date checking to `go build` itself.
# Go's build cache keys on source content AND linker flags, so
# `make VERSION=vA build && make VERSION=vB build` correctly relinks
# with the new version string — whereas a file-timestamp dependency
# (the previous Makefile shape) would consider the binary already
# up-to-date because no .go file had changed. Re-running on no-op
# changes is sub-second thanks to Go's cache; the convenience of
# always-reproducible release builds outweighs the cost.
build:
	go build -ldflags "$(LDFLAGS)" -o $(TM_BINARY) ./cmd/tm

test:
	go test ./...

# Lint target — golangci-lint not installed by default; install via:
#   brew install golangci-lint  OR
#   go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
lint:
	@which golangci-lint >/dev/null 2>&1 || \
		(echo "golangci-lint not found; install via 'brew install golangci-lint'" && exit 1)
	golangci-lint run ./...

clean:
	rm -rf $(BIN_DIR) internal/api/client.gen.go

# Regenerate the typed Go HTTP client from the committed OpenAPI
# snapshot at openapi/v1.json. Uses `go run` so the tool version
# is pinned in this Makefile and resolved through Go's module
# cache — no per-platform binaries in bin/, no install step that
# needs to detect a wrong-platform binary already on disk.
regen-client:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
		-config openapi/codegen.yaml openapi/v1.json
