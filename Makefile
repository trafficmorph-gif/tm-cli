# tm CLI build / regen / test targets.
#
# Quickstart:
#   make build        compile the binary into bin/tm
#   make test         run all Go tests
#   make regen-spec   re-fetch the OpenAPI snapshot from the running server
#   make regen-client re-generate Go client types from the snapshot
#   make all          build + test (default CI target)
#
# Build / regen targets are split because regenerating the spec
# requires booting the Spring Boot app (~5s), whereas regenerating
# the client from an existing snapshot is sub-second. Keep them
# decoupled so fast inner-loop iteration (tweak codegen.yaml, regen,
# build) doesn't pay the Spring boot cost.

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

LDFLAGS := -X 'github.com/trafficmorph/tm-cli/internal/cli.CLIVersion=$(VERSION)'

.PHONY: all build test lint clean regen-spec regen-client

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

# regen-spec re-fetches the OpenAPI snapshot from the Spring Boot
# server. Runs the OpenApiSpecSnapshotTest in the parent Maven
# project with -Dtm.snapshot=true so the test actually writes the
# snapshot files (it's a no-op otherwise).
regen-spec:
	cd .. && mvn -B -q -Dskip.frontend=true -Dtm.snapshot=true \
		-Dtest=OpenApiSpecSnapshotTest test

# regen-client re-generates Go types from the committed JSON snapshot.
# Uses `go run` so the tool version is pinned in this Makefile and
# resolved through Go's module cache — no per-platform binaries in
# bin/, no install step that needs to detect a wrong-platform binary
# already on disk. Source of truth is openapi/v1.json (NOT YAML —
# see OpenApiSpecSnapshotTest Javadoc for why oapi-codegen prefers
# JSON for this codebase).
regen-client:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
		-config openapi/codegen.yaml openapi/v1.json
