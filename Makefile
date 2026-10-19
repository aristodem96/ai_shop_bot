.DEFAULT_GOAL := help
BIN := bin

# Load .env when it exists and put it in the environment of every recipe.
#
# Go has no built-in .env support, so without this `make run` would ignore
# .env and abort on the first required variable. Rewriting `KEY=value` into
# `KEY ?= value` keeps a variable already exported in the shell in charge:
# `LOG_LEVEL=debug make run` works. Only the first `=` is touched, so values
# that contain one (a DSN query string) survive intact.
ifneq (,$(wildcard .env))
$(shell sed -E 's/^([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=/\1 ?= /' .env > .env.mk)
include .env.mk
export
endif

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the bot, seed and import commands into ./bin
	@mkdir -p $(BIN)
	go build -trimpath -ldflags "-s -w" -o $(BIN)/bot ./cmd/bot
	go build -trimpath -ldflags "-s -w" -o $(BIN)/seed ./cmd/seed
	go build -trimpath -ldflags "-s -w" -o $(BIN)/import ./cmd/import

.PHONY: run
run: ## Run the bot locally (needs Postgres up and TELEGRAM_BOT_TOKEN in .env)
	go run ./cmd/bot

.PHONY: seed
seed: ## Migrate and load (or reset) the demo catalogue
	go run ./cmd/seed

# A supplier is set up in the managers' chat from its first file; then
#   make import SUPPLIER="China Avto" FILE=price.xlsx
# loads its next ones from the command line too, with DRY_RUN=1 first.
FILE    ?= docs/import/example-price.csv
DRY_RUN ?=

.PHONY: import
import: ## Import a price list of a supplier set up in the chat: make import SUPPLIER=... FILE=... [DRY_RUN=1]
	go run ./cmd/import -supplier "$(SUPPLIER)" -file $(FILE) $(if $(DRY_RUN),-dry-run)

VEHICLES ?= data/vehicles.csv

.PHONY: import-vehicles
import-vehicles: ## Load the vehicle directory (before any price list): make import-vehicles [VEHICLES=...] [DRY_RUN=1]
	go run ./cmd/import -vehicles $(VEHICLES) $(if $(DRY_RUN),-dry-run)

.PHONY: test
test: ## Run unit tests (integration tests are skipped)
	go test -race -short ./...

.PHONY: test-integration
test-integration: ## Run the full suite, including testcontainers-backed tests (needs Docker)
	go test -race -count=1 ./...

.PHONY: eval
eval: ## Run the consultant against the real model (needs ANTHROPIC_API_KEY and Docker; costs ~1 cent)
	go test -tags eval -count=1 -v -timeout 15m ./internal/agent/evals/

.PHONY: cover
cover: ## Run unit tests and open an HTML coverage report
	go test -short -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "report: coverage.html"

.PHONY: lint
lint: ## Run golangci-lint (installed on demand)
	@command -v golangci-lint >/dev/null 2>&1 || \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	golangci-lint run ./...

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	go mod tidy

.PHONY: fmt
fmt: ## Format the code
	go fmt ./...

.PHONY: deps-up
deps-up: ## Start Postgres and Redis (for `make run`)
	docker compose up -d --wait postgres redis

.PHONY: up
up: ## Start Postgres, Redis and the bot in Docker
	docker compose --profile bot up --build -d

.PHONY: docker-seed
docker-seed: ## Load (or reset) the demo catalogue from inside the bot image
	docker compose --profile bot run --rm --no-deps --entrypoint /seed bot

# down keeps the data: stopping the stack is common, wiping its database is
# not, and the two must not be one keystroke apart.
.PHONY: down
down: ## Stop the stack; the database and Redis are kept
	docker compose --profile bot down

.PHONY: reset
reset: ## Stop the stack and DELETE its data (database, Redis) to start from scratch
	docker compose --profile bot down -v

.PHONY: logs
logs: ## Tail the bot container logs
	docker compose logs -f bot

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN) coverage.out coverage.html
