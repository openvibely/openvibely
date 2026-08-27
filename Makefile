.PHONY: dev build build-desktop package-desktop-macos run migrate templ css clean install-tools test test-short test-cover test-build-dependencies docker-build docker-check-tools

DOCKER ?= docker
IMAGE ?= openvibely/openvibely:local
VERSION ?= dev-unknown

TEMPL_VERSION := $(shell go list -m -f '{{.Version}}' github.com/a-h/templ)
SWAG_VERSION := $(shell go list -m -f '{{.Version}}' github.com/swaggo/swag)
GOOSE_VERSION := $(shell go list -m -f '{{.Version}}' github.com/pressly/goose/v3)
AIR_VERSION := v1.65.3
GO_BIN := $(shell go env GOBIN)
ifeq ($(GO_BIN),)
GO_BIN := $(shell go env GOPATH)/bin
endif

# Generated outputs are tracked file targets so normal builds only regenerate
# them when a generator input changes. Each generator writes all of its outputs;
# the selected output is therefore used as its freshness marker.
TEMPL_GENERATED_TARGET := web/templates/layout/base_templ.go
TEMPL_SOURCES := $(shell find web/templates -type f -name '*.templ' -print)
TEMPL_DIRECTORIES := $(shell find web/templates -type d -print)
TEMPL_GENERATED_OUTPUTS := $(patsubst %.templ,%_templ.go,$(TEMPL_SOURCES))
TEMPL_GENERATED_OTHER_OUTPUTS := $(filter-out $(TEMPL_GENERATED_TARGET),$(TEMPL_GENERATED_OUTPUTS))
TEMPL_GENERATE := go run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION) generate

SWAGGER_GENERATED_TARGET := docs/docs.go
SWAGGER_ANNOTATION_SOURCES := $(shell find cmd internal pkg web -type d -name '.*' -prune -o -type f -name '*.go' -exec grep -lE '^[[:space:]]*//[[:space:]]+@[[:alpha:]]' {} +)
SWAGGER_SCHEMA_SOURCES := $(filter-out %_test.go,$(wildcard internal/models/*.go internal/viewmodels/*.go)) internal/repository/execution_repo.go internal/update/coordinator.go
SWAGGER_GENERATED_OUTPUTS := docs/docs.go docs/swagger.json docs/swagger.yaml
SWAGGER_GENERATED_OTHER_OUTPUTS := $(filter-out $(SWAGGER_GENERATED_TARGET),$(SWAGGER_GENERATED_OUTPUTS))
SWAGGER_INPUTS := $(SWAGGER_ANNOTATION_SOURCES) $(SWAGGER_SCHEMA_SOURCES) go.mod go.sum Makefile $(wildcard .swaggo)
SWAGGER_GENERATE := go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init -g cmd/server/main.go -o docs

# Install development tools
install-tools:
	go install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/air-verse/air@$(AIR_VERSION)
	go install github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION)

# Development with live reload
dev:
	OPENVIBELY_ENABLE_LOCAL_REPO_PATH="$${OPENVIBELY_ENABLE_LOCAL_REPO_PATH:-true}" \
	PATH="$(GO_BIN):$(PATH)" \
	$(GO_BIN)/air -c .air.toml

# Generate templ files (no global binary required)
# This target intentionally forces regeneration; builds use the tracked output target below.
templ:
	$(TEMPL_GENERATE)
	@touch $(TEMPL_GENERATED_TARGET)
	@touch $(TEMPL_GENERATED_OTHER_OUTPUTS)

$(TEMPL_GENERATED_TARGET): $(TEMPL_SOURCES) $(TEMPL_DIRECTORIES) go.mod go.sum Makefile
	$(TEMPL_GENERATE)
	@touch $(TEMPL_GENERATED_TARGET)
	@touch $(TEMPL_GENERATED_OTHER_OUTPUTS)

$(TEMPL_GENERATED_OTHER_OUTPUTS): | $(TEMPL_GENERATED_TARGET)
	@if test -f "$@"; then :; else rm -f "$(TEMPL_GENERATED_TARGET)" && $(MAKE) --no-print-directory "$(TEMPL_GENERATED_TARGET)" && test -f "$@"; fi

# Generate Swagger documentation (no global binary required)
# This target intentionally forces regeneration; builds use the tracked output target below.
swagger:
	$(SWAGGER_GENERATE)
	@sed -i.bak '/LeftDelim:/d' docs/docs.go && sed -i.bak '/RightDelim:/d' docs/docs.go && rm docs/docs.go.bak || true
	@touch $(SWAGGER_GENERATED_TARGET)
	@touch $(SWAGGER_GENERATED_OTHER_OUTPUTS)

$(SWAGGER_GENERATED_TARGET): $(SWAGGER_INPUTS)
	$(SWAGGER_GENERATE)
	@sed -i.bak '/LeftDelim:/d' docs/docs.go && sed -i.bak '/RightDelim:/d' docs/docs.go && rm docs/docs.go.bak || true
	@touch $(SWAGGER_GENERATED_TARGET)
	@touch $(SWAGGER_GENERATED_OTHER_OUTPUTS)

$(SWAGGER_GENERATED_OTHER_OUTPUTS): | $(SWAGGER_GENERATED_TARGET)
	@if test -f "$@"; then :; else rm -f "$(SWAGGER_GENERATED_TARGET)" && $(MAKE) --no-print-directory "$(SWAGGER_GENERATED_TARGET)" && test -f "$@"; fi

# Build production server binary
build: $(TEMPL_GENERATED_OUTPUTS) $(SWAGGER_GENERATED_OUTPUTS)
	go build -ldflags="-s -w" -o bin/openvibely ./cmd/server

# Build desktop binary (Wails integration - see cmd/desktop)
build-desktop: $(TEMPL_GENERATED_OUTPUTS) $(SWAGGER_GENERATED_OUTPUTS)
	go build -ldflags="-s -w" -o bin/openvibely-desktop ./cmd/desktop

# Package desktop app bundle for macOS Finder/Dock launch (no Terminal)
package-desktop-macos: build-desktop
	@rm -rf bin/OpenVibely.app
	@mkdir -p bin/OpenVibely.app/Contents/MacOS
	@mkdir -p bin/OpenVibely.app/Contents/Resources
	@cp bin/openvibely-desktop bin/OpenVibely.app/Contents/MacOS/OpenVibely
	@chmod +x bin/OpenVibely.app/Contents/MacOS/OpenVibely
	@printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>' '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' '<plist version="1.0">' '<dict>' '<key>CFBundleName</key><string>OpenVibely</string>' '<key>CFBundleDisplayName</key><string>OpenVibely</string>' '<key>CFBundleIdentifier</key><string>com.openvibely.desktop</string>' '<key>CFBundleVersion</key><string>$(VERSION)</string>' '<key>CFBundleShortVersionString</key><string>$(VERSION)</string>' '<key>CFBundlePackageType</key><string>APPL</string>' '<key>CFBundleExecutable</key><string>OpenVibely</string>' '<key>LSMinimumSystemVersion</key><string>12.0</string>' '</dict>' '</plist>' > bin/OpenVibely.app/Contents/Info.plist
	@echo 'Created bin/OpenVibely.app (launch this from Finder for no Terminal window)'

# Run the server
run: build
	./bin/openvibely

# Run database migrations (standalone)
migrate:
	go run ./cmd/server migrate

# Run all tests
test:
	go test ./... -count=1 -timeout 120s

# Verify build generator freshness rules
test-build-dependencies:
	./scripts/test-make-build-dependencies.sh

# Run all tests, skipping slow/timing-sensitive tests (fast CI feedback loop)
test-short:
	go test ./... -count=1 -timeout 60s -short

# Run all tests with coverage, excluding generated, fixture, and legacy workflow code.
# _templ.go files are auto-generated by templ and account for ~36% of tracked
# statements at low coverage — including them would misrepresent real coverage.
test-cover:
	go test ./... -count=1 -timeout 120s -coverpkg=./... -coverprofile=coverage.out
	@grep -Ev '(_templ\.go:|^github.com/openvibely/openvibely/cmd/|^github.com/openvibely/openvibely/docs/|^github.com/openvibely/openvibely/internal/database/migrations/|^github.com/openvibely/openvibely/internal/update/testfixture/|^github.com/openvibely/openvibely/internal/service/workflow_service\.go:)' coverage.out > coverage.filtered.out
	@go tool cover -func=coverage.filtered.out | tail -1
	@echo "Full HTML report: go tool cover -html=coverage.filtered.out"

# Build the published OpenVibely image with coding toolchains.
docker-build:
	$(DOCKER) build -t $(IMAGE) -f Dockerfile .

# Verify the image is configured non-root and its coding toolchain works as that user.
docker-check-tools:
	@test "$$($(DOCKER) image inspect --format '{{.Config.User}}' $(IMAGE))" = "10001:10001"
	@test "$$($(DOCKER) run --rm --entrypoint /usr/bin/sh $(IMAGE) -c '. /etc/os-release; printf %s "$$VERSION_ID"')" = "44"
	@test -z "$$($(DOCKER) run --rm --entrypoint /usr/bin/sh $(IMAGE) -c 'find / -xdev -perm /6000 -type f 2>/dev/null')"
	@$(DOCKER) run --rm --entrypoint /usr/bin/sh $(IMAGE) -c '! command -v sudo'
	$(DOCKER) run --rm $(IMAGE) bash -lc 'set -euo pipefail; test "$$(id -u)" = 10001; test "$$(id -g)" = 10001; test -w /data; go version; node --version; npm --version; corepack --version; tsc --version; python3 --version; python3 -m pip --version; venv="$$(mktemp -d)"; python3 -m venv "$$venv"; "$$venv/bin/python" --version; rm -rf "$$venv"; rustc --version; cargo --version; java -version; javac -version; ruby --version; git --version; rg --version | head -n 1; make --version | head -n 1; gcc --version | head -n 1; g++ --version | head -n 1; pkg-config --version'

# Clean build artifacts
clean:
	rm -rf bin/
