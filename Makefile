SHELL := /bin/sh

INSTALL ?= install

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
NANA_BIN ?= $(BINDIR)/nana
REPO_ROOT := $(abspath .)
NANA_ENTRY := $(REPO_ROOT)/bin/nana

.PHONY: all help build lint typecheck test benchmark fmt vet static-analysis verify install install-local uninstall run setup doctor clean release-assets

all: build

help:
	@printf '%s\n' \
		'Targets:' \
		'  build       Build native binaries to bin/' \
		'  lint        Check Go formatting without modifying files' \
		'  typecheck   Compile Go packages/tests without running tests' \
		'  test        Run the Go test suite' \
		'  benchmark   Run Go benchmarks with allocation counts' \
		'  fmt         Run gofmt on Go sources' \
		'  vet         Run go vet ./...' \
		'  static-analysis  Run static-analysis checks' \
		'  verify      Run the repo verification profile' \
		'  release-assets  Build native release assets for the current platform' \
		'  install     Install the built nana binary to $(NANA_BIN)' \
		'  install-local  Alias for install (~/.local/bin/nana by default)' \
		'  uninstall   Remove $(NANA_BIN)' \
		'  run         Run the freshly built repo binary (pass ARGS="...")' \
		'  setup       Run nana setup from the built CLI' \
		'  doctor      Run nana doctor from the built CLI' \
		'  clean       Remove bin/'

build:
	go run ./cmd/nana-build build-go-cli

lint:
	@out="$$(gofmt -l cmd internal)"; \
	status="$$?"; \
	if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
	if [ -n "$$out" ]; then printf '%s\n' "$$out"; exit 1; fi

typecheck:
	GOFLAGS= go test -run '^$$' ./...

test:
	GOFLAGS= go test ./...

benchmark:
	GOFLAGS= go test -run=^$$ -bench=. -benchmem ./...

fmt:
	gofmt -w $$(find cmd internal -type f -name '*.go')

vet:
	GOFLAGS= go vet ./...

static-analysis: vet

verify:
	MAKEFLAGS= MFLAGS= GNUMAKEFLAGS= MAKEFILES= GOFLAGS= go run ./cmd/nana verify

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

install-local: install

uninstall:
	rm -f "$(NANA_BIN)"

run: build
	"$(NANA_ENTRY)" $(ARGS)

setup: build
	"$(NANA_ENTRY)" setup $(ARGS)

doctor: build
	"$(NANA_ENTRY)" doctor $(ARGS)

clean:
	rm -rf bin release-assets
