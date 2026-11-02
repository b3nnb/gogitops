# gogitops Makefile
# Targets: build, install, test, lint — focused on the recipe-lint tool.
# Usage:
#   make build       — compile recipe-lint to bin/recipe-lint
#   make install     — go install the recipe-lint binary
#   make test        — run Go tests
#   make lint        — run recipe-lint against recipes/
#   make clean       — remove built artifacts

BINARY_NAME  = recipe-lint
BIN_DIR      = bin
CMD_PATH     = ./cmd/recipe-lint
RECIPES_DIR  = recipes
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      = -X main.Version=$(VERSION)

GO           = go

.PHONY: build install test lint clean vet fmt help

## build: compile recipe-lint to bin/recipe-lint
build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

## install: install recipe-lint to GOBIN
install:
	$(GO) install -ldflags "$(LDFLAGS)" $(CMD_PATH)

## test: run Go tests
test:
	$(GO) test ./cmd/recipe-lint/... ./internal/...

## lint: run recipe-lint against the recipes directory
lint: build
	./$(BIN_DIR)/$(BINARY_NAME) -repo .

## vet: run go vet
vet:
	$(GO) vet ./cmd/recipe-lint/...

## fmt: format Go source
fmt:
	$(GO) fmt ./cmd/recipe-lint/...

## clean: remove build artifacts
clean:
	rm -f $(BIN_DIR)/$(BINARY_NAME)

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'