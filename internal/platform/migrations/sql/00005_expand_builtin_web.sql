-- +goose Up
-- FEAT-175: metadata only; supplier credentials, queries and page bodies are
-- never stored here. No role is silently granted the new permission.
INSERT INTO permissions (permission_key, description)
VALUES ('web.retrieve', 'Use server-managed read-only web retrieval');

CREATE TABLE web_bindings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    native_reference uuid NOT NULL,
    authorization_revision bigint NOT NULL CHECK (authorization_revision > 0),
    credential_revision bigint NOT NULL CHECK (credential_revision > 0),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES tenant_memberships (tenant_id, user_id),
    UNIQUE (tenant_id, user_id, native_reference)
);
CREATE TABLE web_grants (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL REFERENCES web_bindings(id),
    bearer_sha256 text UNIQUE NOT NULL CHECK (length(bearer_sha256) = 64),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX web_grants_expiry ON web_grants(expires_at);
CREATE TABLE web_operations (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL REFERENCES web_bindings(id),
    parameter_digest text NOT NULL CHECK (length(parameter_digest) = 64),
    tool text NOT NULL CHECK (tool IN ('kimi_web_search', 'kimi_web_search_pro', 'kimi_web_fetch')),
    state text NOT NULL CHECK (state IN ('ready', 'dispatch_committed', 'completed', 'unknown', 'cancelled', 'rejected')),
    dispatch text NOT NULL CHECK (dispatch IN ('not_dispatched', 'may_have_dispatched', 'completed')),
    claim_id uuid,
    executor_instance uuid,
    deadline timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX web_operations_binding_deadline ON web_operations(binding_id, deadline);
CREATE INDEX web_operations_created_at ON web_operations(created_at);
CREATE TABLE web_operation_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation_id uuid NOT NULL,
    binding_id uuid NOT NULL,
    state text NOT NULL,
    dispatch text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION web_operation_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'web_operation_events is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER web_operation_events_append_only BEFORE UPDATE OR DELETE ON web_operation_events
FOR EACH ROW EXECUTE FUNCTION web_operation_events_immutable();

-- +goose Down
-- Application rollback disables the feature and retains metering/idempotency.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'migration 00005 is expand-only; roll forward';
END $$;
-- +goose StatementEnd
