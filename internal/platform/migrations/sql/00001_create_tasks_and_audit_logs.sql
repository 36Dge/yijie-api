-- +goose Up
CREATE TABLE tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    task_type text NOT NULL CHECK (char_length(task_type) BETWEEN 1 AND 100),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    status text NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'running', 'waiting_approval', 'completed', 'failed')),
    input jsonb NOT NULL CHECK (jsonb_typeof(input) = 'object'),
    result jsonb CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tasks_tenant_created_at_idx ON tasks (tenant_id, created_at DESC);
CREATE INDEX tasks_tenant_status_idx ON tasks (tenant_id, status);

CREATE TABLE audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    actor_type text NOT NULL CHECK (char_length(actor_type) BETWEEN 1 AND 50),
    actor_id text,
    action text NOT NULL CHECK (char_length(action) BETWEEN 1 AND 100),
    resource_type text NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 100),
    resource_id uuid NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    request_id text NOT NULL CHECK (char_length(request_id) BETWEEN 1 AND 200),
    trace_id text CHECK (trace_id IS NULL OR char_length(trace_id) BETWEEN 1 AND 200),
    outcome text NOT NULL CHECK (outcome IN ('success', 'failure')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_tenant_occurred_at_idx ON audit_logs (tenant_id, occurred_at DESC);
CREATE INDEX audit_logs_resource_idx ON audit_logs (resource_type, resource_id);
CREATE INDEX audit_logs_request_id_idx ON audit_logs (request_id);

-- +goose StatementBegin
CREATE FUNCTION reject_audit_log_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_logs_append_only
BEFORE UPDATE OR DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS audit_logs_append_only ON audit_logs;
DROP FUNCTION IF EXISTS reject_audit_log_mutation();
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS tasks;
