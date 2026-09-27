# crowban — Makefile. Slice 6: docker-first deployment (Dockerfile with two
# build targets + docker-compose.yml). Host-side ports are overridable via
# CROWBAN_SERVER_PORT / CROWBAN_MCP_PORT (.env or environment); see .env.example.

GO      ?= go
BIN_DIR := bin

.PHONY: help fmt vet build test ui up down logs smoke

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-8s %s\n", $$1, $$2}'

fmt: ## gofmt all Go code
	gofmt -l -w .

vet: ## go vet + gofmt check (gofmt -l must print nothing)
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi
	go vet ./...

build: ## compile both static binaries into bin/ (crowban-server, crowban-mcp)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/crowban-server ./cmd/crowban-server
	@if [ -d cmd/crowban-mcp ]; then \
		echo "building crowban-mcp"; \
		CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/crowban-mcp ./cmd/crowban-mcp; \
	else \
		echo "cmd/crowban-mcp not present yet — skipping (later slice)"; \
	fi

test: ## run all Go tests
	CGO_ENABLED=0 $(GO) test ./... -count=1

ui: ## build the web UI into web/dist (served by crowban-server via CROWBAN_STATIC_DIR)
	cd web && npm ci && npm run build

up: ## build both images and start crowban-server + crowban-mcp detached
	docker compose up --build -d

down: ## stop and remove containers (keeps the crowban-data volume)
	docker compose down

logs: ## stream logs from both containers
	docker compose logs -f

smoke: ## curl both /healthz + GET /api/v1/projects on the overridable host ports
	@sh scripts/smoke.sh
