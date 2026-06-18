BINARY   := freefsm
MODULE   := github.com/MartialM1nd/freefsm
GO       := go
CGO_ENABLED := 0

BUILD_DIR  := dist
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
VERSION   ?= $(shell git describe --tags 2>/dev/null || echo "0.1.0")
LDFLAGS   := -s -w -X $(MODULE)/internal/config.Version=$(VERSION) -X $(MODULE)/internal/config.Commit=$(COMMIT)

export CGO_ENABLED=0
_PATH_EXTRA := $(HOME)/go/bin

.PHONY: all build clean install generate ent templ sqlc

all: generate build

generate: ent templ

ENT_CMD := $(shell which ent 2>/dev/null || echo "$(GOPATH)/bin/ent")

ent:
	@echo "generating ent schema..."
	PATH="$(_PATH_EXTRA):$$PATH" $(ENT_CMD) generate ./internal/ent/schema

templ:
	@echo "generating templ templates..."
	PATH="$(_PATH_EXTRA):$$PATH" templ generate

sqlc:
	@echo "generating sqlc queries..."
	sqlc generate

build: generate
	@echo "building $(BINARY)..."
	mkdir -p $(BUILD_DIR)
	PATH="$(_PATH_EXTRA):$$PATH" $(GO) build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/freefsm/

install: build
	@echo "installing..."
	install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

install-freebsd: install
	install -m 755 deploy/freebsd/freefsm /etc/rc.d/freefsm

install-linux: install
	install -m 644 deploy/linux/freefsm.service /etc/systemd/system/freefsm.service
	systemctl daemon-reload

clean:
	rm -rf $(BUILD_DIR) internal/repository/

fmt:
	$(GO) fmt ./...

lint:
	$(GO) vet ./...

test:
	$(GO) test -v -race ./...

run: build
	./$(BUILD_DIR)/$(BINARY)
