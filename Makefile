SHELL := /bin/sh

INSTALL ?= install

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
NANA_BIN ?= $(BINDIR)/nana
REPO_ROOT := $(abspath .)
NANA_ENTRY := $(REPO_ROOT)/bin/nana

.PHONY: help build test fmt vet verify install uninstall run setup doctor clean release-assets

help:
	@printf '%s\n' \
		'Targets:' \
		'  build       Build native binaries to bin/' \
		'  test        Run the Go test suite' \
		'  fmt         Run gofmt on Go sources' \
		'  vet         Run go vet ./...' \
		'  verify      Run fmt + vet + test' \
		'  release-assets  Build native release assets for the current platform' \
		'  install     Install the built nana binary to $(NANA_BIN)' \
		'  uninstall   Remove $(NANA_BIN)' \
		'  run         Run the built NANA CLI (pass ARGS="...")' \
		'  setup       Run nana setup from the built CLI' \
		'  doctor      Run nana doctor from the built CLI' \
		'  clean       Remove bin/'

build:
	go run ./cmd/nana-build build-go-cli

test:
	go test ./...

fmt:
	gofmt -w $$(find cmd internal -type f -name '*.go')

vet:
	go vet ./...

verify: fmt vet test

release-assets:
	mkdir -p release-assets
	TARGET="$$( \
		case "$$(go env GOOS)/$$(go env GOARCH)" in \
			linux/amd64) echo x86_64-unknown-linux-gnu ;; \
			linux/arm64) echo aarch64-unknown-linux-gnu ;; \
			darwin/amd64) echo x86_64-apple-darwin ;; \
			darwin/arm64) echo aarch64-apple-darwin ;; \
			windows/amd64) echo x86_64-pc-windows-msvc ;; \
			*) echo unsupported ;; \
		esac \
	)"; \
	if [ "$$TARGET" = unsupported ]; then \
		echo "unsupported local release target: $$(go env GOOS)/$$(go env GOARCH)"; \
		exit 1; \
	fi; \
	go run ./cmd/nana-build build-go-release-asset --target "$$TARGET" --out-dir release-assets

install: build
	$(INSTALL) -d "$(BINDIR)"
	$(INSTALL) "$(NANA_ENTRY)" "$(NANA_BIN)"

uninstall:
	rm -f "$(NANA_BIN)"

run: build
	"$(NANA_BIN)" $(ARGS)

setup: build
	"$(NANA_ENTRY)" setup $(ARGS)

doctor: build
	"$(NANA_ENTRY)" doctor $(ARGS)

clean:
	rm -rf bin release-assets
