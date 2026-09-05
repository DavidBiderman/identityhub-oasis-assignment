# IdentityHub
#
# `make help` lists every target. `make deploy` is the one that gets you running.

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE := docker compose
BACKEND := backend
FRONTEND := frontend

WEB_URL      ?= http://localhost:3000
API_URL      ?= http://localhost:8080
TEMPORAL_URL ?= http://localhost:8233

# Which seeded person `make token` mints a token for.
EMAIL          ?= admin@acme.test
# The seeded password, from cmd/bootstrap. Demo data, not a credential.
DEMO_PASSWORD  ?= identityhub-demo

# Connection strings for tests run on the host against the Compose stack. The
# application connects as identityhub_app, which cannot change the schema.
TEST_DATABASE_URL ?= postgres://identityhub_app:identityhub_app@localhost:5432/identityhub?sslmode=disable
# Database 1, not 0: the stack uses 0, and a test run must not expire the
# sign-outs of the stack a developer is clicking through at the same time.
TEST_REDIS_URL    ?= redis://localhost:6379/1
OWNER_DATABASE_URL ?= postgres://identityhub:identityhub@localhost:5432/identityhub?sslmode=disable

# The local KMS speaks the real AWS protocol, so tests use the AWS SDK against it.
export AWS_ENDPOINT_URL     ?= http://localhost:4566
export AWS_REGION           ?= us-east-1
export AWS_ACCESS_KEY_ID    ?= local
export AWS_SECRET_ACCESS_KEY ?= local

.PHONY: help
help: ## Show this help
	@echo "IdentityHub"
	@echo
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  First time here?  brew bundle && make deploy"

# --- Running ------------------------------------------------------------

.PHONY: deploy
deploy: build up wait ## Build and run the whole stack, then wait until it is ready
	@echo
	@echo "  Application   $(WEB_URL)"
	@echo "  API           $(API_URL)"
	@echo "  Temporal      $(TEMPORAL_URL)"
	@echo
	@echo "  Sign in as admin@acme.test with the password identityhub-demo."
	@echo "  Three seeded people across two accounts; the README lists them."
	@echo "  stand-in for an identity provider."
	@echo
	@echo "  Connect Jira next: docs/CONNECTING-JIRA.md"

.PHONY: build
build: ## Build every container image
	$(COMPOSE) build

.PHONY: up
up: ## Start the stack in the background
	$(COMPOSE) up -d

.PHONY: down
down: ## Stop the stack, keeping data
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete its data
	@echo "This deletes the database and the encryption key. Stored Jira"
	@echo "credentials will not be decryptable afterwards and must be reconnected."
	$(COMPOSE) down -v

.PHONY: wait
wait: ## Block until every service reports healthy
	@echo "Waiting for the stack…"
	@for i in $$(seq 1 60); do \
	  unhealthy=$$($(COMPOSE) ps --format '{{.Service}} {{.Health}}' 2>/dev/null \
	    | awk '$$2 != "healthy" && $$2 != "" {print $$1}'); \
	  if [ -z "$$unhealthy" ]; then echo "Ready."; exit 0; fi; \
	  sleep 2; \
	done; \
	echo "Timed out. Try: make logs"; exit 1

.PHONY: logs
logs: ## Follow logs from every service
	$(COMPOSE) logs -f

.PHONY: ps
ps: ## Show what is running
	$(COMPOSE) ps

.PHONY: reseed
reseed: ## Recreate the demo organization, accounts and people
	$(COMPOSE) run --rm bootstrap -force

# --- Development --------------------------------------------------------

.PHONY: test
test: ## Run every test against the running stack
	cd $(BACKEND) && TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
	  TEST_TEMPORAL_HOSTPORT=localhost:7233 \
	  TEST_REDIS_URL="$(TEST_REDIS_URL)" go test ./...
	cd $(FRONTEND) && npm run typecheck

.PHONY: test-unit
test-unit: ## Run only tests that need no infrastructure
	cd $(BACKEND) && go test ./...

.PHONY: token
token: ## Print an access token for a seeded person (EMAIL=admin@acme.test)
	@# Signs in as one of the seeded people, the same way the interface does.
	@curl -fsS -X POST $(API_URL)/api/auth/signin \
	  -H 'Content-Type: application/json' \
	  -d '{"email":"$(EMAIL)","password":"$(DEMO_PASSWORD)"}' \
	  | jq -er '.data.accessToken' \
	  || { echo "Could not sign in as $(EMAIL). Is the stack up? Try: make deploy"; exit 1; }

.PHONY: docs
docs: ## Open the API documentation, where every endpoint can be tried
	@echo "$(WEB_URL)/docs  --  sign in there; the document requires a token"
	@command -v open >/dev/null && open $(WEB_URL)/docs || true

.PHONY: tools
tools: ## Install the code generators Homebrew does not carry
	@# oapi-codegen has no Homebrew formula, so it comes from the module that
	@# defines it -- which also pins it to a version this repository builds
	@# against, rather than whatever a tap happens to hold.
	cd $(BACKEND) && go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	@echo "installed into $$(cd $(BACKEND) && go env GOPATH)/bin -- make sure that is on your PATH"

.PHONY: generate
generate: ## Regenerate everything derived from a source of truth
	@command -v sqlc >/dev/null || { echo "sqlc is not installed. Try: brew bundle"; exit 1; }
	@command -v oapi-codegen >/dev/null || { echo "oapi-codegen is not installed. Try: make tools"; exit 1; }
	cd $(BACKEND) && sqlc generate
	cd $(BACKEND) && oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	@echo "regenerated: sqlc from db/queries, api types and routing from api/openapi.yaml"

.PHONY: lint
lint: ## Vet, format-check and lint both halves
	cd $(BACKEND) && go vet ./...
	@cd $(BACKEND) && test -z "$$(gofmt -l ./cmd ./internal | grep -v sqlcgen)" \
	  || { echo "gofmt needed:"; gofmt -l ./cmd ./internal | grep -v sqlcgen; exit 1; }
	@if command -v golangci-lint >/dev/null; then \
	  cd $(BACKEND) && golangci-lint run ./...; \
	else \
	  echo "golangci-lint not installed, skipping"; \
	fi
	cd $(FRONTEND) && npm run typecheck

.PHONY: migrate
migrate: ## Apply database migrations
	$(COMPOSE) run --rm migrate

# --- Inspecting ---------------------------------------------------------

.PHONY: psql
psql: ## Open a database shell
	$(COMPOSE) exec postgres psql -U identityhub -d identityhub

.PHONY: workflows
workflows: ## List recent Temporal workflows
	$(COMPOSE) exec temporal temporal workflow list \
	  --address 127.0.0.1:7233 --namespace identityhub --limit 20

.PHONY: schedules
schedules: ## List Temporal schedules, including the blog digest
	$(COMPOSE) exec temporal temporal schedule list \
	  --address 127.0.0.1:7233 --namespace identityhub


