SHELL := /bin/sh

BINARY := symbrowse
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo 0.1.1)
GO ?= go
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
	@files="$$(find . -type f -name '*.go' -not -path './vendor/*' -print)"; \
	if [ -n "$$files" ]; then \
		unformatted="$$(gofmt -l $$files)"; \
		if [ -n "$$unformatted" ]; then \
			printf '%s\n' 'The following Go files are not formatted:'; \
			printf '%s\n' "$$unformatted"; \
			exit 1; \
		fi; \
	fi

clean:
	rm -rf $(BINARY) dist coverage.out
