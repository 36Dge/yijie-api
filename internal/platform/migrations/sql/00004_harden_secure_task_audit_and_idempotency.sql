-- +goose Up
ALTER TABLE audit_logs
    ALTER COLUMN tenant_id DROP NOT NULL;

ALTER TABLE task_create_idempotency
    ADD COLUMN expires_at timestamptz;

UPDATE task_create_idempotency
SET expires_at = created_at + interval '24 hours'
WHERE expires_at IS NULL;

ALTER TABLE task_create_idempotency
    ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours'),
    ALTER COLUMN expires_at SET NOT NULL,
    ADD CONSTRAINT task_create_idempotency_expiry_check
        CHECK (expires_at > created_at);

CREATE INDEX task_create_idempotency_expiry_idx
    ON task_create_idempotency (expires_at);

-- +goose Down
-- This security migration changes durable audit and idempotency semantics. Recovery
-- rolls application code forward instead of pretending the old retention model exists.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 00004 is forward-only; roll forward instead of migrating down';
END;
$$;
-- +goose StatementEnd
