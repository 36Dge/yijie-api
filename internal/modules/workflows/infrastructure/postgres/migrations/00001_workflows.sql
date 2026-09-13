-- +goose Up
CREATE TABLE workflow_scope_bindings (
    scope_key text PRIMARY KEY CHECK (scope_key = 'feat-153-local-v1'),
    owner_user_id uuid NOT NULL CHECK (owner_user_id = '12500000-0000-4000-8000-000000000001'),
    tenant_id uuid NOT NULL CHECK (tenant_id = '12500000-0000-4000-8000-100000000001'),
    coze_user_id varchar(64) NOT NULL UNIQUE,
    coze_space_id varchar(64) NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE workflow_resources (
    workflow_id varchar(64) PRIMARY KEY,
    scope_key text NOT NULL REFERENCES workflow_scope_bindings(scope_key),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scope_key, workflow_id)
);
CREATE TABLE workflow_operations (
    operation_id uuid PRIMARY KEY,
    scope_key text NOT NULL REFERENCES workflow_scope_bindings(scope_key),
    kind text NOT NULL CHECK (kind IN ('create','save','test','publish','run')),
    request_sha256 char(64) NOT NULL,
    workflow_id varchar(64),
    phase text NOT NULL CHECK (phase IN ('recorded','completed','unknown','rejected')),
    receipt jsonb NOT NULL CHECK (jsonb_typeof(receipt) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (scope_key, workflow_id) REFERENCES workflow_resources(scope_key, workflow_id)
);
CREATE INDEX workflow_operations_resource_created ON workflow_operations(scope_key,workflow_id,created_at DESC,operation_id DESC);
CREATE TABLE workflow_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope_key text NOT NULL REFERENCES workflow_scope_bindings(scope_key),
    operation_id uuid,
    workflow_id varchar(64),
    action varchar(40) NOT NULL,
    outcome_code varchar(64) NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workflow_audit_scope_time ON workflow_audit(scope_key,occurred_at DESC,id DESC);
-- +goose StatementBegin
CREATE FUNCTION workflow_reject_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'workflow audit is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER workflow_audit_append_only BEFORE UPDATE OR DELETE ON workflow_audit FOR EACH ROW EXECUTE FUNCTION workflow_reject_audit_mutation();

-- +goose Down
-- This independent local data set is retained on rollback. Do not drop its history.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'workflow migration is expand-only; retain data and roll forward'; END $$;
-- +goose StatementEnd
