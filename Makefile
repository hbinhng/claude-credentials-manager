# ccm — Claude Credentials Manager
#
# Quick reference:
#   make              build ccm for the host platform (default)
#   make test         run all tests
#   make dist         cross-compile all six release binaries into dist/
#   make clean        remove ccm and dist/
#   make help         show this help
#
# All targets build with CGO_ENABLED=0 and -trimpath -ldflags="-s -w", so
# every binary is fully static (no libc dependency) and stripped. Linux
# builds therefore run on glibc 2.23+ (Ubuntu 16+), Alpine/musl, and
# anything else with a modern kernel.

BINARY  := ccm

# Version metadata baked into the binary via -ldflags -X. VERSION is read
# from npm/package.json (the canonical version source — see CLAUDE.md), so
# bumping the release version is a single-file edit. COMMIT is the short
# git SHA; BUILD_DATE is the current UTC time in ISO-8601. A plain
# `go build .` (bypassing this Makefile) leaves the defaults in
# cmd/version.go at "dev"/"unknown", which signals an untagged local build.
VERSION    := $(shell sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' npm/package.json | head -1)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := github.com/hbinhng/claude-credentials-manager/cmd
LDFLAGS := -s -w \
  -X $(VERSION_PKG).Version=$(VERSION) \
  -X $(VERSION_PKG).Commit=$(COMMIT) \
  -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

GOFLAGS := -trimpath -ldflags="$(LDFLAGS)"

export CGO_ENABLED := 0

GO_SOURCES := $(shell find . -type f -name '*.go' -not -path './dist/*')

DIST_BINARIES := \
  dist/ccm-darwin-amd64 \
  dist/ccm-darwin-arm64 \
  dist/ccm-linux-amd64 \
  dist/ccm-linux-arm64 \
  dist/ccm-windows-amd64.exe \
  dist/ccm-windows-arm64.exe

.PHONY: all build test dist clean help

all: build

help:
	@echo 'Targets:'
	@echo '  build    build ccm for the host platform (default)'
	@echo '  test     run all tests'
	@echo '  dist     cross-compile all six release binaries into dist/'
	@echo '  clean    remove ccm and dist/'
	@echo '  help     show this help'

build: $(BINARY)

$(BINARY): $(GO_SOURCES)
	go build $(GOFLAGS) -o $@ .

test:
	go test ./...

dist: $(DIST_BINARIES)

dist/ccm-darwin-%: $(GO_SOURCES)
	@mkdir -p $(@D)
	GOOS=darwin GOARCH=$* go build $(GOFLAGS) -o $@ .

dist/ccm-linux-%: $(GO_SOURCES)
	@mkdir -p $(@D)
	GOOS=linux GOARCH=$* go build $(GOFLAGS) -o $@ .

dist/ccm-windows-%.exe: $(GO_SOURCES)
	@mkdir -p $(@D)
	GOOS=windows GOARCH=$* go build $(GOFLAGS) -o $@ .

clean:
	rm -f $(BINARY)
	rm -rf dist/
