# ravn — Makefile. Slice 6: docker-first deployment (Dockerfile with two
# build targets + docker-compose.yml). Host-side ports are overridable via
# RAVN_SERVER_PORT / RAVN_MCP_PORT (.env or environment); see .env.example.

GO      ?= go
BIN_DIR := bin
IMAGE   ?= ravn:latest

.PHONY: help fmt vet build test ui e2e up build-up down logs smoke

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-8s %s\n", $$1, $$2}'

fmt: ## gofmt all Go code
	gofmt -l -w .

vet: ## go vet + gofmt check (gofmt -l must print nothing)
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi
	go vet ./...

build: ## compile both static binaries into bin/ (ravn-server, ravn-mcp)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/ravn-server ./cmd/ravn-server
	@if [ -d cmd/ravn-mcp ]; then \
		echo "building ravn-mcp"; \
		CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/ravn-mcp ./cmd/ravn-mcp; \
	else \
		echo "cmd/ravn-mcp not present yet — skipping (later slice)"; \
	fi

test: ## run all Go tests
	CGO_ENABLED=0 $(GO) test ./... -count=1

ui: ## build the web UI into web/dist (served by ravn-server via RAVN_STATIC_DIR)
	cd web && npm ci && npm run build

e2e: ## run browser E2E test suite (Playwright)
	cd web && npm run test:e2e

up: ## start ravn-server + ravn-mcp detached (uses forgejo package by default)
	docker compose up -d

build-up: ## build images from scratch and start ravn-server + ravn-mcp detached
	docker compose up --build -d

down: ## stop and remove containers (keeps the ravn-data volume)
	docker compose down

logs: ## stream logs from both containers
	docker compose logs -f

smoke: ## curl both /healthz + GET /api/v1/projects on the overridable host ports
	@sh scripts/smoke.sh
