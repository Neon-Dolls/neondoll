GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X github.com/Neon-Dolls/neondoll/pkg/version.Version=$(VERSION)

BUILD_DIR ?= build

.PHONY: all build build-daemon build-client build-printspark clean test test-e2e vet lint fmt version

all: build test vet

# ── Build ──────────────────────────────────────────────────────────

build: build-daemon build-client build-printspark

build-daemon:
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/neondolld ./cmd/neondolld

build-client:
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/neondoll-client ./cmd/neondoll-client

build-printspark:
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/printspark ./cmd/printspark

# ── Test ───────────────────────────────────────────────────────────

test:
	$(GO) test -count=1 -race ./...

test-e2e:
	$(GO) test -tags e2e -count=1 -v ./test/...

test-e2e-short:
	$(GO) test -tags e2e -count=1 -v -run 'TestExportImportBoundary|TestRestoredSparkContinues' ./test/...

test-e2e-destroy:
	$(GO) test -tags e2e -count=1 -v -run TestSparkSurvivesDestruction ./test/...

# ── Quality ────────────────────────────────────────────────────────

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint:
	golangci-lint run ./...

# ── Version ─────────────────────────────────────────────────────────

version:
	@echo $(VERSION)

# ── Clean ──────────────────────────────────────────────────────────

clean:
	rm -rf $(BUILD_DIR) dist/

# ── Release (goreleaser dry-run) ──────────────────────────────────

release-dry-run:
	goreleaser release --snapshot --clean

.PHONY: build build-daemon build-client build-printspark
.PHONY: test test-e2e test-e2e-short test-e2e-destroy
.PHONY: vet fmt lint version clean release-dry-run