.PHONY: help lint lint-fix test test-verbose fmt vet pre-commit install-hooks

help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

lint: ## Run golangci-lint
	@echo "Running golangci-lint..."
	@golangci-lint run ./...

lint-fix: ## Run golangci-lint with auto-fix
	@echo "Running golangci-lint with auto-fix..."
	@golangci-lint run --fix ./...

test: ## Run tests
	@echo "Running tests..."
	@go test ./ssh/... ./controllers/...

test-verbose: ## Run tests with verbose output
	@echo "Running tests with verbose output..."
	@go test -v ./ssh/... ./controllers/...

test-coverage: ## Run tests with coverage report
	@echo "Running tests with coverage..."
	@go test -coverprofile=coverage.out ./ssh/... ./controllers/...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

fmt: ## Format code with gofmt
	@echo "Formatting code..."
	@gofmt -s -w .

vet: ## Run go vet
	@echo "Running go vet..."
	@go vet ./...

pre-commit: fmt vet lint test ## Run all pre-commit checks
	@echo "✓ All pre-commit checks passed!"

install-hooks: ## Install git pre-commit hook
	@echo "Installing git pre-commit hook..."
	@echo '#!/bin/bash' > ../../.git/hooks/pre-commit
	@echo 'cd gateway-api/gateway-controller && make pre-commit' >> ../../.git/hooks/pre-commit
	@chmod +x ../../.git/hooks/pre-commit
	@echo "✓ Git pre-commit hook installed!"
	@echo "  Hook will run: fmt, vet, lint, and test"
	@echo "  To skip: git commit --no-verify"

.DEFAULT_GOAL := help
