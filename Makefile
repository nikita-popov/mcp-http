REPO := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

GO = go

VERSION := $(shell printf '%s-dev' "$$(git describe --tags --always --dirty 2>/dev/null || echo unknown)")
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build mcp-http clean

all: deps build

build: mcp-http

deps:
	$(GO) mod tidy
	$(GO) mod download

get:
	$(GO) get -v ./...

mcp-http:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mcp-http

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out
	$(GO) tool cover -html=coverage.out -o coverage.html

check: fmt vet test

clean:
	$(GO) clean -v
	rm -rf $(REPO)/bin
