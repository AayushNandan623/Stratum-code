# Stratum Makefile
# All targets are self-documenting: `make help` lists them

.PHONY: help build build-server build-worker build-ctl \
        migrate migrate-create migrate-down \
        dev-setup dev-up dev-down seed \
        run-server run-worker \
        lint test test-unit test-int \
        clean docker-build docker-push

# ─── Variables ────────────────────────────────────────────────────────────────

BINARY_DIR   := bin
SERVER_BIN   := $(BINARY_DIR)/stratum-server
WORKER_BIN   := $(BINARY_DIR)/stratum-worker
CTL_BIN      := $(BINARY_DIR)/stratum-ctl

DB_URL       ?= postgresql://stratum:stratum@localhost:5432/stratum?sslmode=disable
MIGRATIONS   := ./migrations

DOCKER_REGISTRY ?= ghcr.io/yourorg
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT          ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)"

GO      := go
GOFLAGS := -trimpath

# ─── Help ─────────────────────────────────────────────────────────────────────

help: ## Show this help message
	@echo "Stratum build targets:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ─── Build ────────────────────────────────────────────────────────────────────

build: build-server build-worker build-ctl ## Build all binaries

build-server: ## Build stratum-server binary
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(SERVER_BIN) ./cmd/stratum-server

build-worker: ## Build stratum-worker binary
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(WORKER_BIN) ./cmd/stratum-worker

build-ctl: ## Build stratum-ctl binary
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(CTL_BIN) ./cmd/stratum-ctl

# ─── Database ─────────────────────────────────────────────────────────────────

migrate: ## Run all pending migrations
	@which migrate > /dev/null || (echo "golang-migrate not found. Install: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest" && exit 1)
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" up
	@echo "Migrations complete."

migrate-down: ## Roll back the most recent migration
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" down 1

migrate-create: ## Create a new migration file: make migrate-create NAME=add_something
ifndef NAME
	$(error NAME is required: make migrate-create NAME=add_something)
endif
	migrate create -ext sql -dir $(MIGRATIONS) -seq $(NAME)
	@echo "Created migration: $(MIGRATIONS)"

migrate-version: ## Show current migration version
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" version

# ─── Local Development ────────────────────────────────────────────────────────

dev-setup: ## One-command local setup: start deps, migrate, seed
	@echo "Starting development dependencies..."
	docker compose -f deploy/docker-compose.yml up -d
	@echo "Waiting for PostgreSQL..."
	@until pg_isready -h localhost -p 5432 -U stratum > /dev/null 2>&1; do sleep 1; done
	@echo "PostgreSQL ready."
	@$(MAKE) migrate
	@$(MAKE) seed
	@cp -n deploy/.env.dev .env 2>/dev/null || true
	@echo ""
	@echo "✓ Development environment ready."
	@echo "  Run: make run-server   (in terminal 1)"
	@echo "  Run: make run-worker   (in terminal 2)"

dev-up: ## Start background services (PostgreSQL, NATS)
	docker compose -f deploy/docker-compose.yml up -d

dev-down: ## Stop and remove background services
	docker compose -f deploy/docker-compose.yml down

seed: ## Insert development seed data (org, user, stack)
	$(GO) run ./scripts/seed

run-server: ## Run the control plane server (requires dev-setup)
	@[ -f .env ] && export $$(cat .env | xargs) ; $(GO) run ./cmd/stratum-server

run-worker: ## Run a worker agent (requires dev-setup and server running)
	@[ -f .env ] && export $$(cat .env | xargs) ; $(GO) run ./cmd/stratum-worker

# ─── Quality ──────────────────────────────────────────────────────────────────

lint: ## Run golangci-lint
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. See: https://golangci-lint.run/usage/install/" && exit 1)
	golangci-lint run ./...

fmt: ## Format all Go code
	$(GO) fmt ./...
	$(GO) run golang.org/x/tools/cmd/goimports -w .

vet: ## Run go vet
	$(GO) vet ./...

# ─── Tests ────────────────────────────────────────────────────────────────────

test: ## Run all tests (requires running PostgreSQL)
	$(GO) test -race -timeout 120s ./...

test-unit: ## Run unit tests only (no external dependencies)
	$(GO) test -race -timeout 30s -short ./...

test-int: ## Run integration tests only (requires running PostgreSQL)
	$(GO) test -race -timeout 120s -run Integration ./...

test-cover: ## Run tests with coverage report
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ─── Clean ────────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts and coverage files
	rm -rf $(BINARY_DIR)
	rm -f coverage.out coverage.html

# ─── Docker ───────────────────────────────────────────────────────────────────

docker-build: ## Build Docker images for server and worker
	docker build -f deploy/Dockerfile.server -t $(DOCKER_REGISTRY)/stratum-server:$(VERSION) .
	docker build -f deploy/Dockerfile.worker -t $(DOCKER_REGISTRY)/stratum-worker:$(VERSION) .

docker-push: docker-build ## Build and push Docker images to registry
	docker push $(DOCKER_REGISTRY)/stratum-server:$(VERSION)
	docker push $(DOCKER_REGISTRY)/stratum-worker:$(VERSION)
	@echo "Pushed version: $(VERSION)"

# ─── Tools ────────────────────────────────────────────────────────────────────

tools: ## Install required development tools
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	go install github.com/golangci-lint/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Tools installed."

check-tools: ## Verify required tools are installed
	@which migrate    > /dev/null && echo "✓ migrate"    || echo "✗ migrate (run: make tools)"
	@which golangci-lint > /dev/null && echo "✓ golangci-lint" || echo "✗ golangci-lint (run: make tools)"
	@which docker     > /dev/null && echo "✓ docker"     || echo "✗ docker (required)"
	@which psql       > /dev/null && echo "✓ psql"       || echo "✗ psql (optional, for direct DB access)"
