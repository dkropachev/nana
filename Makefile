SHELL := /bin/sh

NPM ?= npm
NODE ?= node
INSTALL ?= install

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
NANA_BIN ?= $(BINDIR)/nana
REPO_ROOT := $(abspath .)
NANA_ENTRY := $(REPO_ROOT)/bin/nana

.PHONY: help build build-go build-full test test-go test-node check lint verify install uninstall run setup doctor clean

help:
	@printf '%s\n' \
		'Targets:' \
		'  build       Build dist/cli/nana.js' \
		'  build-go    Build the Go CLI to bin/nana' \
		'  build-full  Build JS plus native explore/sparkshell assets' \
		'  test        Run the repo test suite' \
		'  test-go     Run the Go test suite' \
		'  test-node   Run node-based tests only' \
		'  check       Run unused-symbol/type checks' \
		'  lint        Run biome lint' \
		'  verify      Run build + targeted checks + lint' \
		'  install     Install a wrapper to $(NANA_BIN) that runs this checkout' \
		'  uninstall   Remove $(NANA_BIN)' \
		'  run         Run the built NANA CLI (pass ARGS="...")' \
		'  setup       Run nana setup from the built CLI' \
		'  doctor      Run nana doctor from the built CLI' \
		'  clean       Remove dist/'

build:
	$(NPM) run build

build-go:
	$(NPM) run build:go

build-full:
	$(NPM) run build:full

test:
	$(NPM) test

test-go:
	$(NPM) run test:go

test-node:
	$(NPM) run test:node

check:
	$(NPM) run check:no-unused

lint:
	$(NPM) run lint

verify: build
	$(NODE) --test dist/cli/__tests__/github.test.js dist/cli/__tests__/index.test.js
	$(NPM) run check:no-unused
	$(NPM) run lint

install: build-go
	$(INSTALL) -d "$(BINDIR)"
	$(INSTALL) "$(NANA_ENTRY)" "$(NANA_BIN)"

uninstall:
	rm -f "$(NANA_BIN)"

run: build-go
	"$(NANA_BIN)" $(ARGS)

setup: build-go
	"$(NANA_ENTRY)" setup $(ARGS)

doctor: build-go
	"$(NANA_ENTRY)" doctor $(ARGS)

clean:
	rm -rf dist
