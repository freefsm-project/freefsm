BINARY   := freefsm
MODULE   := github.com/freefsm-project/freefsm
GO       := go

BUILD_DIR := dist

# PATH extension for Go binaries
_PATH_EXTRA := $(HOME)/go/bin

.PHONY: all build compile clean install generate ent templ sqlc fmt lint test run checksum

all: generate build

generate: ent templ

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

install-freebsd: install
	@install -m 755 deploy/freebsd/freefsm /etc/rc.d/freefsm
	@install -m 644 deploy/freebsd/freefsm.conf.sample /usr/local/etc/freefsm.conf.sample
	@if [ ! -f /usr/local/etc/freefsm.conf ]; then \
		cp /usr/local/etc/freefsm.conf.sample /usr/local/etc/freefsm.conf; \
		echo "Created /usr/local/etc/freefsm.conf — edit it with your secrets"; \
	fi
	@install -d -o freefsm -g freefsm -m 755 /var/log/freefsm
	@install -d -o freefsm -g freefsm -m 755 /var/db/freefsm/uploads

install-linux: install
	@install -m 644 deploy/linux/freefsm.service /etc/systemd/system/freefsm.service
	@if [ ! -f /etc/freefsm.conf ]; then \
		install -m 600 deploy/linux/freefsm.conf.sample /etc/freefsm.conf; \
		echo "Created /etc/freefsm.conf — edit it with your secrets"; \
	fi
	@install -d -m 755 /var/log/freefsm
	@install -d -o freefsm -g freefsm -m 755 /var/lib/freefsm/uploads
	@systemctl daemon-reload

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
