GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X github.com/Neon-Dolls/neondoll/pkg/version.Version=$(VERSION)

BUILD_DIR ?= build

.PHONY: all build build-daemon build-client build-printspark clean test test-e2e vet lint fmt version acceptance acceptance-external acceptance-telegram

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

PROFILE ?= core5
ACCEPTANCE_REPORT ?= acceptance-report.json

acceptance:
	ACCEPTANCE_CORE_REVISION="$$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
		$(GO) run ./cmd/neondoll-acceptance -profile "$(PROFILE)" -report "$(ACCEPTANCE_REPORT)"

SHELLBODY_SOURCE ?=
SHELLBODY_BINARY ?=
SHELLBODY_REVISION ?= 10b2d510d9a5887800c8e6f72c2ef05530bf6c46

acceptance-external:
	ACCEPTANCE_CORE_REVISION="$$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
		$(GO) run ./cmd/neondoll-acceptance -profile "$(PROFILE)" -external \
		-shellbody-source "$(SHELLBODY_SOURCE)" -shellbody-binary "$(SHELLBODY_BINARY)" \
		-shellbody-revision "$(SHELLBODY_REVISION)" -report "$(ACCEPTANCE_REPORT)"

TELEGRAM_GATEWAY_SOURCE ?=
TELEGRAM_GATEWAY_WASM ?=
TELEGRAM_GATEWAY_REVISION ?= 962e09b396a389950ed2d926ba38efc4baf37dfd
TINYGO ?= tinygo

acceptance-telegram:
	ACCEPTANCE_CORE_REVISION="$$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
		$(GO) run ./cmd/neondoll-acceptance -profile "$(PROFILE)" -telegram \
		-telegram-source "$(TELEGRAM_GATEWAY_SOURCE)" -telegram-artifact "$(TELEGRAM_GATEWAY_WASM)" \
		-telegram-revision "$(TELEGRAM_GATEWAY_REVISION)" -tinygo "$(TINYGO)" -report "$(ACCEPTANCE_REPORT)"

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
.PHONY: vet fmt lint version clean release-dry-run acceptance acceptance-external
