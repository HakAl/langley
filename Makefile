# Langley Makefile
# Requires: Go 1.21+, Node 18+, Git Bash (Windows)

.PHONY: all build run dev test clean help install-deps lint check

# Default target
all: build

#---------------------------------------------------------------------------
# Build
#---------------------------------------------------------------------------

build: build-frontend build-backend ## Build everything

build-backend: ## Build Go binary
	go build -o langley.exe ./cmd/langley

build-frontend: ## Build frontend (production)
	cd web && npm run build

#---------------------------------------------------------------------------
# Development
#---------------------------------------------------------------------------

dev: ## Run in development mode (backend + frontend hot reload)
	@echo "Starting Langley in dev mode..."
	@echo "Backend: go run, Frontend: vite dev"
	@echo "Dashboard: http://localhost:5173"
	@echo "Proxy: localhost:9090"
	@echo "---"
	@$(MAKE) -j2 dev-backend dev-frontend

dev-debug: ## Run in dev mode with debug logging (backend only, run dev-frontend separately)
	@echo "Starting Langley backend in DEBUG mode..."
	@echo "Run 'make dev-frontend' in another terminal for the dashboard"
	@echo "Proxy: localhost:9090"
	@echo "---"
	go run ./cmd/langley -debug

dev-backend:
	go run ./cmd/langley

dev-backend-debug:
	go run ./cmd/langley -debug

dev-frontend: ## Run frontend dev server
	cd web && npx vite

run: build ## Build and run production binary
	./langley.exe

#---------------------------------------------------------------------------
# Testing
#---------------------------------------------------------------------------

test: test-backend test-frontend ## Run all tests

test-backend: ## Run Go tests with race detector
	go test -race -v ./...

test-frontend: ## Run frontend tests
	cd web && npm test 2>/dev/null || echo "No frontend tests configured"

test-cover: ## Run tests with coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

#---------------------------------------------------------------------------
# Quality
#---------------------------------------------------------------------------

lint: ## Run linters
	go vet ./...
	@which golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

check: lint test ## Run all quality checks (lint + test)

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
