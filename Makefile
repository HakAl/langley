# Langley Makefile
# Requires: Go 1.24+, Node ^20.19.0 || >=22.12.0

.PHONY: all build build-backend build-frontend run dev dev-debug dev-backend dev-backend-debug dev-frontend test test-backend test-frontend test-cover clean clean-all help install-deps lint check stop stop-frontend stop-backend

# Default target
all: build

#---------------------------------------------------------------------------
# Build
#---------------------------------------------------------------------------

build: build-frontend ## Build everything (frontend first so assets are embedded)
	$(MAKE) build-backend

build-backend: web/dist/index.html ## Build Go binary
	go build -o langley.exe ./cmd/langley

build-frontend: ## Build frontend (production)
	cd web && npm run build

web/dist/index.html:
	cd web && npm run build

#---------------------------------------------------------------------------
# Development
#---------------------------------------------------------------------------

dev: web/dist/index.html ## Run in development mode (backend + frontend hot reload)
	@echo "Starting Langley in dev mode..."
	@echo "Backend: go run, Frontend: vite dev"
	@echo "Dashboard: http://localhost:5173"
	@echo "Proxy: localhost:9090"
	@echo "---"
	@$(MAKE) -j2 dev-backend dev-frontend

dev-debug: web/dist/index.html ## Run in dev mode with debug logging (backend only, run dev-frontend separately)
	@echo "Starting Langley backend in DEBUG mode..."
	@echo "Run 'make dev-frontend' in another terminal for the dashboard"
	@echo "Proxy: localhost:9090"
	@echo "---"
	go run ./cmd/langley -debug

dev-backend: web/dist/index.html
	go run ./cmd/langley

dev-backend-debug: web/dist/index.html
	go run ./cmd/langley -debug

dev-frontend: ## Run frontend dev server
	cd web && npm run dev

stop: stop-frontend stop-backend ## Kill all dev processes (Windows: after Ctrl+C)

stop-frontend: ## Kill orphaned vite process
	cd web && npm stop

stop-backend: ## Kill orphaned langley process
	npx --yes kill-port 9090

run: build ## Build and run production binary
	./langley.exe

#---------------------------------------------------------------------------
# Testing
#---------------------------------------------------------------------------

test: web/dist/index.html ## Run all tests
	$(MAKE) test-backend
	$(MAKE) test-frontend

test-backend: web/dist/index.html ## Run Go tests with race detector
	go test -race -v ./...

test-frontend: ## Run frontend tests
	cd web && npm test

test-cover: web/dist/index.html ## Run tests with coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

#---------------------------------------------------------------------------
# Quality
#---------------------------------------------------------------------------

lint: web/dist/index.html ## Run linters
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

check: web/dist/index.html ## Run all quality checks (lint + test)
	$(MAKE) lint
	$(MAKE) test

#---------------------------------------------------------------------------
# Dependencies
#---------------------------------------------------------------------------

install-deps: ## Install all dependencies
	go mod download
	cd web && npm install

#---------------------------------------------------------------------------
# Cleanup
#---------------------------------------------------------------------------

clean: ## Remove build artifacts
	rm -f langley.exe
	rm -f coverage.out coverage.html
	rm -rf web/dist

clean-all: clean ## Remove build artifacts and dependencies
	rm -rf web/node_modules

#---------------------------------------------------------------------------
# Help
#---------------------------------------------------------------------------

help: ## Show this help
	@echo "Langley - LLM Traffic Inspector"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make dev          # Start development servers"
	@echo "  make build        # Build production binary"
	@echo "  make test         # Run all tests"
	@echo "  make check        # Run lint + tests"
