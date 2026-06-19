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

.PHONY: all build compile clean install generate ent templ sqlc

all: generate build

generate: ent templ

ent:
	@echo "generating ent schema..."
	PATH="$(_PATH_EXTRA):$$PATH" ent generate ./internal/ent/schema

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

compile:
	@echo "building $(BINARY)..."
	mkdir -p $(BUILD_DIR)
	PATH="$(_PATH_EXTRA):$$PATH" $(GO) build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/freefsm/

install: compile
	@echo "installing..."
	install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

install-freebsd: install
	install -m 755 deploy/freebsd/freefsm /etc/rc.d/freefsm
	install -m 644 deploy/freebsd/freefsm.conf.sample /usr/local/etc/freefsm.conf.sample
	@if [ ! -f /usr/local/etc/freefsm.conf ]; then \
		cp /usr/local/etc/freefsm.conf.sample /usr/local/etc/freefsm.conf; \
		echo "Created /usr/local/etc/freefsm.conf — edit it with your secrets"; \
	fi
	install -d -o freefsm -g freefsm -m 755 /var/log/freefsm
	install -d -o freefsm -g freefsm -m 755 /var/run/freefsm

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
