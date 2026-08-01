LOCAL_POSTGRES_DSN ?= postgres://yijie:yijie@localhost:5432/yijie_api?sslmode=disable
YIJIE_API_POSTGRES_DSN ?= $(LOCAL_POSTGRES_DSN)
YIJIE_API_REDIS_URL ?= redis://localhost:6379/0
YIJIE_API_TEST_POSTGRES_DSN ?= $(LOCAL_POSTGRES_DSN)
CONTRACTS_DIR ?= ../yijie-contracts
CONTRACTS_LOCK ?= contracts/public-api.lock.json

export YIJIE_API_POSTGRES_DSN
export YIJIE_API_REDIS_URL

.PHONY: dev test test-integration test-all lint generate generate-check migrate-up migrate-status

dev: migrate-up
	go run ./cmd/api-server

test:
	YIJIE_API_CONTRACTS_DIR="$(CONTRACTS_DIR)" go test -race -cover ./...

test-integration:
	YIJIE_API_CONTRACTS_DIR="$(CONTRACTS_DIR)" YIJIE_API_TEST_POSTGRES_DSN="$(YIJIE_API_TEST_POSTGRES_DSN)" go test -race -tags=integration -cover ./internal/...

test-all: test test-integration

lint:
	@test -z "$$(gofmt -l $$(find cmd internal -type f -name '*.go'))" || (gofmt -l $$(find cmd internal -type f -name '*.go') && exit 1)
	go vet ./...

generate:
	@test -f "$(CONTRACTS_DIR)/openapi/public/public.yaml" || (echo "Missing sibling yijie-contracts repository" && exit 1)
	go run ./cmd/contract-pin source "$(CONTRACTS_LOCK)" "$(CONTRACTS_DIR)"
	go run ./cmd/contract-pin generator "$(CONTRACTS_LOCK)" "$(CURDIR)"
	go tool oapi-codegen -generate types -package publicapi -o "$(CURDIR)/internal/interfaces/publicapi/types.gen.go" "$(CONTRACTS_DIR)/openapi/public/public.yaml"
	go run ./cmd/contract-pin generated "$(CONTRACTS_LOCK)" "$(CURDIR)"

generate-check: generate

migrate-up:
	go run ./cmd/migrate up

migrate-status:
	go run ./cmd/migrate status
