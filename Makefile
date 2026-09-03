SHELL := /bin/sh

BINARY := symbrowse
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo 0.1.1)
GO ?= go
# Match the toolchain CI formats with, so gofmt output cannot differ by host.
GO_VERSION := $(shell awk '$$1 == "go" { print $$2; exit }' go.mod)
CGO_ENABLED ?= 0
GOFLAGS ?=
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: build test test-race lint fmt-check clean

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/symbrowse

test:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test $(GOFLAGS) -count=1 ./...

test-race:
	CGO_ENABLED=1 $(GO) test $(GOFLAGS) -race -count=1 ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		printf '%s\n' 'golangci-lint not found; falling back to go vet'; \
		CGO_ENABLED=$(CGO_ENABLED) $(GO) vet ./...; \
	fi

fmt-check:
	@files="$$(git ls-files '*.go' ':!:vendor/*')"; \
	if [ -n "$$files" ]; then \
		gofmt_bin="$$(GOTOOLCHAIN=go$(GO_VERSION) $(GO) env GOROOT 2>/dev/null)/bin/gofmt"; \
		if [ ! -x "$$gofmt_bin" ]; then \
			printf '%s\n' 'gofmt for Go $(GO_VERSION) unavailable; falling back to the gofmt in PATH'; \
			gofmt_bin=gofmt; \
		fi; \
		unformatted="$$("$$gofmt_bin" -l $$files)"; \
		if [ -n "$$unformatted" ]; then \
			printf '%s\n' 'The following Go files are not formatted:'; \
			printf '%s\n' "$$unformatted"; \
			exit 1; \
		fi; \
	fi

clean:
	rm -rf $(BINARY) dist coverage.out
