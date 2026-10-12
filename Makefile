BINARY   := freefsm
MODULE   := github.com/freefsm-project/freefsm
GO       := go

BUILD_DIR := dist
# Staging and service ownership for platform installers.
DESTDIR ?=
SERVICE_USER ?= freefsm
SERVICE_GROUP ?= freefsm

# PATH extension for Go binaries
_PATH_EXTRA := $(HOME)/go/bin

.PHONY: all build compile clean install generate vocabulary ent templ sqlc fmt lint test run checksum

all: generate build

generate: vocabulary ent templ

vocabulary:
	@$(GO) generate ./internal/vocabulary

ent:
	@echo "generating ent schema..."
	@PATH="$(_PATH_EXTRA):$$PATH" CGO_ENABLED=0 ent generate ./internal/ent/schema

templ:
	@echo "generating templ templates..."
	@PATH="$(_PATH_EXTRA):$$PATH" CGO_ENABLED=0 templ generate

sqlc:
	@echo "generating sqlc queries..."
	@sqlc generate

build: generate
	@echo "building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	@COMMIT=$$(git rev-parse HEAD 2>/dev/null || echo "none"); \
	 VERSION=$$(git describe --tags --exact-match HEAD 2>/dev/null || echo "dev"); \
	 EXACT=false; \
	 if [ "$$VERSION" != dev ] && [ -z "$$(git status --porcelain --untracked-files=normal)" ]; then EXACT=true; else VERSION=dev; fi; \
	 PATH="$(_PATH_EXTRA):$$PATH" CGO_ENABLED=0 $(GO) build -buildvcs=true \
	 -ldflags "-s -w -X $(MODULE)/internal/config.Version=$$VERSION -X $(MODULE)/internal/config.Commit=$$COMMIT -X $(MODULE)/internal/config.ExactRelease=$$EXACT -X $(MODULE)/internal/config.BuildKind=$$(if [ $$EXACT = true ]; then echo release; else echo development; fi)" \
	 -o $(BUILD_DIR)/$(BINARY) ./cmd/freefsm/

compile:
	@echo "building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	@COMMIT=$$(git rev-parse HEAD 2>/dev/null || echo "none"); \
	 VERSION=$$(git describe --tags --exact-match HEAD 2>/dev/null || echo "dev"); \
	 EXACT=false; \
	 if [ "$$VERSION" != dev ] && [ -z "$$(git status --porcelain --untracked-files=normal)" ]; then EXACT=true; else VERSION=dev; fi; \
	 PATH="$(_PATH_EXTRA):$$PATH" CGO_ENABLED=0 $(GO) build -buildvcs=true \
	 -ldflags "-s -w -X $(MODULE)/internal/config.Version=$$VERSION -X $(MODULE)/internal/config.Commit=$$COMMIT -X $(MODULE)/internal/config.ExactRelease=$$EXACT -X $(MODULE)/internal/config.BuildKind=$$(if [ $$EXACT = true ]; then echo release; else echo development; fi)" \
	 -o $(BUILD_DIR)/$(BINARY) ./cmd/freefsm/

install: compile
	@echo "installing..."
	@install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

install-freebsd: compile
	@install -d "$(DESTDIR)/usr/local/bin"
	@install -m 755 $(BUILD_DIR)/$(BINARY) "$(DESTDIR)/usr/local/bin/$(BINARY)"
	@sh deploy/freebsd/install.sh "$(if $(DESTDIR),$(DESTDIR),/)" "$(SERVICE_USER)" "$(SERVICE_GROUP)"

install-linux: compile
	@install -d "$(DESTDIR)/usr/local/bin"
	@install -m 755 $(BUILD_DIR)/$(BINARY) "$(DESTDIR)/usr/local/bin/$(BINARY)"
	@sh deploy/linux/install.sh "$(if $(DESTDIR),$(DESTDIR),/)" "$(SERVICE_USER)" "$(SERVICE_GROUP)"
	@if [ -z "$(DESTDIR)" ]; then systemctl daemon-reload; fi

clean:
	@rm -rf $(BUILD_DIR) internal/repository/

fmt:
	@$(GO) fmt ./...

lint:
	@$(GO) vet ./...

test:
	@$(GO) test -v -race ./...

run: build
	@./$(BUILD_DIR)/$(BINARY)

checksum:
	@echo "SHA256 of $(BUILD_DIR)/$(BINARY):"
	@sha256sum $(BUILD_DIR)/$(BINARY) 2>/dev/null || shasum -a 256 $(BUILD_DIR)/$(BINARY)
