SHELL := /bin/zsh
GO ?= go
DB_URL ?= postgres://research:research@localhost:55433/research_scout?sslmode=disable

.PHONY: verify test-unit test-integration test-contract openapi-lint smoke-stage0 discovery-smoke eval-rag eval-retrieval eval-brief db-up db-down migrate

verify: openapi-lint test-unit test-integration test-contract

openapi-lint:
	@ruby -e "require 'yaml'; YAML.load_file('openapi/research-scout.yaml'); puts 'OpenAPI YAML OK'"

test-unit:
	$(GO) test ./internal/... ./cmd/... -run 'Test[^Integration]' -count=1

test-integration:
	RS_DATABASE_URL='$(DB_URL)' $(GO) test ./internal/... -run 'Integration' -count=1

test-contract:
	$(GO) test ./internal/httpserver -run 'Contract' -count=1

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

migrate:
	RS_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/worker migrate

smoke-stage0:
	RS_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/worker smoke-stage0

# Cost-bounded real-provider check: at most one request per source, no DB needed.
# Add WEB=1 to also make one provider-native web search request.
discovery-smoke:
	$(GO) run ./cmd/worker discovery-smoke $(if $(WEB),-web,)

eval-rag:
	RS_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/worker eval-rag

eval-retrieval:
	RS_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/worker eval-rag -kind retrieval

eval-brief:
	RS_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/worker eval-rag -kind brief
