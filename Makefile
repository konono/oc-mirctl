BINARY    := oc-mirctl
MODULE    := github.com/nict-ocpai/oc-mirctl
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
  -X '$(MODULE)/internal/cli.version=$(VERSION)' \
  -X '$(MODULE)/internal/cli.commit=$(COMMIT)' \
  -X '$(MODULE)/internal/cli.buildDate=$(BUILD_DATE)'

GOFLAGS  ?=
INSTALL_DIR ?= $(shell go env GOPATH)/bin

.PHONY: build install clean test lint fmt tidy cross help

## build: Build the binary for the current platform
build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/oc-mirctl

## install: Build and install to INSTALL_DIR
install: build
	install -m 755 bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)

## cross: Build for linux/amd64 and linux/arm64
cross:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 ./cmd/oc-mirctl
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 ./cmd/oc-mirctl
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64 ./cmd/oc-mirctl

## test: Run tests
test:
	go test ./... -v

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt: Format code
fmt:
	go fmt ./...
	goimports -w .

## tidy: Tidy go modules
tidy:
	go mod tidy

## clean: Remove build artifacts
clean:
	rm -rf bin/

## help: Show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'
