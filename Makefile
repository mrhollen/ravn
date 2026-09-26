# crowban — Makefile (slice 1: Go skeleton only).
# The docker-compose targets below are stubs until the Dockerfile and
# docker-compose.yml land in a later slice.

GO      ?= go
BIN_DIR := bin

.PHONY: help fmt vet build test up down logs smoke

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-8s %s\n", $$1, $$2}'

fmt: ## gofmt all Go code
	gofmt -l -w .

vet: ## go vet + gofmt check (gofmt -l must print nothing)
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi
	go vet ./...

build: ## compile binaries into bin/ (server now; mcp lands in a later slice)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/crowban-server ./cmd/crowban-server
	@if [ -d cmd/crowban-mcp ]; then \
		echo "building crowban-mcp"; \
		CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/crowban-mcp ./cmd/crowban-mcp; \
	else \
		echo "cmd/crowban-mcp not present yet — skipping (later slice)"; \
	fi

test: ## run all Go tests
	CGO_ENABLED=0 $(GO) test ./... -count=1

up: ## docker compose up --build -d [STUB]
	@echo "docker wiring lands in a later slice (no Dockerfile / docker-compose.yml yet)"

down: ## docker compose down [STUB]
	@echo "docker wiring lands in a later slice (no Dockerfile / docker-compose.yml yet)"

logs: ## docker compose logs -f [STUB]
	@echo "docker wiring lands in a later slice (no Dockerfile / docker-compose.yml yet)"

smoke: ## end-to-end curl smoke test [STUB]
	@echo "smoke test lands with the api layer in a later slice"
