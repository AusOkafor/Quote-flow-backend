## ──────────────────────────────────────────────────────────
##  QuoteFlow Backend — Makefile
##  Usage: make <target>
## ──────────────────────────────────────────────────────────

BINARY   := quoteflow
CMD      := ./cmd/server
MODULE   := quoteflow-backend

.PHONY: run build test clean tidy lint docker-build docker-run migrate setup-storage

## Run database migrations (requires .env with SUPABASE_DB_URL)
migrate:
	go run ./cmd/migrate

## Create logos storage bucket (run once; requires .env with SUPABASE_URL + SUPABASE_SERVICE_ROLE_KEY)
setup-storage:
	go run ./cmd/setup-storage

## Run the server with hot-reload (requires: go install github.com/air-verse/air@latest)
dev:
	air -c .air.toml

## Run without hot-reload
run:
	go run $(CMD)/main.go

## Build production binary
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="-s -w" -o bin/$(BINARY) $(CMD)/main.go
	@echo "✅  Binary: bin/$(BINARY)"

## Download + tidy dependencies
tidy:
	go mod tidy
	go mod verify

## Run all tests
test:
	go test ./... -v -race -count=1

## Run tests with coverage
test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Lint (requires: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
lint:
	golangci-lint run ./...

## Remove build artifacts
clean:
	rm -rf bin/ coverage.out coverage.html

## Build Docker image
docker-build:
	docker build -t quoteflow-api:latest .

## Run with Docker Compose
docker-run:
	docker compose up -d

## Copy .env.example to .env if it doesn't exist
env:
	@[ -f .env ] || (cp .env.example .env && echo "✅  .env created — fill in your Supabase keys")

## Print all routes (requires the server to be running)
routes:
	@echo "GET    /health"
	@echo "GET    /q/:token"
	@echo "POST   /q/:token/accept"
	@echo "---"
	@echo "GET    /auth/me"
	@echo "GET    /dashboard"
	@echo "GET    /profile"
	@echo "PUT    /profile"
	@echo "---"
	@echo "GET    /clients"
	@echo "POST   /clients"
	@echo "GET    /clients/:id"
	@echo "PUT    /clients/:id"
	@echo "DELETE /clients/:id"
	@echo "---"
	@echo "GET    /quotes?status="
	@echo "POST   /quotes"
	@echo "GET    /quotes/export"
	@echo "GET    /quotes/:id"
	@echo "DELETE /quotes/:id"
	@echo "POST   /quotes/:id/send"
	@echo "POST   /quotes/:id/duplicate"
