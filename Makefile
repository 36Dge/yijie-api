LOCAL_POSTGRES_DSN ?= postgres://yijie:yijie@localhost:5432/yijie_api?sslmode=disable
YIJIE_API_POSTGRES_DSN ?= $(LOCAL_POSTGRES_DSN)
YIJIE_API_REDIS_URL ?= redis://localhost:6379/0
YIJIE_API_TEST_POSTGRES_DSN ?= $(LOCAL_POSTGRES_DSN)
CONTRACTS_DIR ?= ../yijie-contracts
CONTRACTS_LOCK ?= contracts/public-api.lock.json
WORKFLOW_GO ?= go

export YIJIE_API_POSTGRES_DSN
export YIJIE_API_REDIS_URL

.PHONY: dev test test-integration test-all lint generate generate-check migrate-up migrate-status bootstrap-nonprod-authz

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

generate-check:
	@test -f "$(CONTRACTS_DIR)/openapi/public/public.yaml" || (echo "Missing sibling yijie-contracts repository" && exit 1)
	@tmp_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	go run ./cmd/contract-pin source "$(CONTRACTS_LOCK)" "$(CONTRACTS_DIR)"; \
	go run ./cmd/contract-pin generator "$(CONTRACTS_LOCK)" "$(CURDIR)"; \
	go tool oapi-codegen -generate types -package publicapi -o "$$tmp_dir/types.gen.go" "$(CONTRACTS_DIR)/openapi/public/public.yaml"; \
	cmp -s "$$tmp_dir/types.gen.go" "$(CURDIR)/internal/interfaces/publicapi/types.gen.go" || \
		(echo "Generated public API types are stale; run make generate" >&2; diff -u "$(CURDIR)/internal/interfaces/publicapi/types.gen.go" "$$tmp_dir/types.gen.go" || true; exit 1); \
	go run ./cmd/contract-pin generated "$(CONTRACTS_LOCK)" "$(CURDIR)"

migrate-up:
	go run ./cmd/migrate up

migrate-status:
	go run ./cmd/migrate status

bootstrap-nonprod-authz:
	@test -n "$(INPUT)" || (echo "INPUT is required" >&2; exit 2)
	go run ./cmd/bootstrap-nonprod-authz --input "$(INPUT)" --profile "$(BOOTSTRAP_PROFILE)"

# FEAT-153 independent local candidate. Never changes the public API source lock.
.PHONY: workflow-generate-check workflow-test workflow-lint workflow-build workflow-pg-qualify workflow-qualify-build workflow-qualify-check workflow-session-qualify
workflow-generate-check:
	node "$(CONTRACTS_DIR)/scripts/sync-workflow-consumer.mjs" api --check

workflow-test: workflow-generate-check
	$(WORKFLOW_GO) test -race -count=1 ./internal/modules/workflows/... ./internal/workflowlocal ./cmd/workflow-local-server ./cmd/workflow-local-migrate ./cmd/workflow-local-qualify

workflow-lint:
	@workflow_files=$$(rg --files internal/modules/workflows internal/workflowlocal cmd/workflow-local-server cmd/workflow-local-migrate cmd/workflow-local-qualify -g '*.go'); workflow_unformatted=$$(gofmt -l $$workflow_files) && test -z "$$workflow_unformatted"
	$(WORKFLOW_GO) vet ./internal/modules/workflows/... ./internal/workflowlocal ./cmd/workflow-local-server ./cmd/workflow-local-migrate ./cmd/workflow-local-qualify

workflow-build: workflow-generate-check
	mkdir -p bin/workflow-local
	$(WORKFLOW_GO) build -o bin/workflow-local/workflow-local-server ./cmd/workflow-local-server
	$(WORKFLOW_GO) build -o bin/workflow-local/workflow-local-migrate ./cmd/workflow-local-migrate

# Explicitly opt-in. Requires root-controlled migration, real principal and the
# workflow ID from HTTP qualification. Never uses the legacy API test DSN.
workflow-pg-qualify: workflow-generate-check
	$(WORKFLOW_GO) test -p=2 -race -count=1 -tags=workflowintegration ./internal/modules/workflows/infrastructure/postgres

workflow-qualify-build: workflow-generate-check
	mkdir -p bin/workflow-local
	$(WORKFLOW_GO) build -p=2 -trimpath -buildvcs=false -o bin/workflow-local/workflow-local-qualify ./cmd/workflow-local-qualify

# Fast in-process checks only; this target does not wait 300 seconds or use HTTP.
workflow-qualify-check: workflow-generate-check
	$(WORKFLOW_GO) test -race -count=1 ./cmd/workflow-local-qualify

# Root-controlled real qualification. Only creates a named synthetic draft and
# exercises API sessions with natural elapsed time; no publish/run or App claims.
workflow-session-qualify: workflow-qualify-build
	@test -n "$(EVIDENCE)" || (echo "EVIDENCE is required" >&2; exit 2)
	bin/workflow-local/workflow-local-qualify --session-lifecycle --evidence "$(EVIDENCE)"
