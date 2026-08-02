-- +goose Up
ALTER TABLE tasks
    ADD COLUMN created_by_user_id uuid;

ALTER TABLE tasks
    ADD CONSTRAINT tasks_tenant_id_fkey
        FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT tasks_created_by_user_id_fkey
        FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT NOT VALID;

CREATE INDEX tasks_creator_private_v2_idx
    ON tasks (tenant_id, created_by_user_id, id)
    WHERE created_by_user_id IS NOT NULL;

CREATE TABLE task_create_idempotency (
    tenant_id uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
    created_by_user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    idempotency_key uuid NOT NULL,
    canonical_sha256 text NOT NULL
        CHECK (canonical_sha256 ~ '^[0-9a-f]{64}$'),
    task_id uuid REFERENCES tasks (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, created_by_user_id, idempotency_key)
);

CREATE UNIQUE INDEX task_create_idempotency_task_idx
    ON task_create_idempotency (task_id);

-- +goose Down
-- This migration is expand-only. Legacy NULL-owner rows stay isolated and must not be
-- assigned by guess; recovery rolls application code forward without dropping ownership.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 00003 is expand-only; roll forward instead of migrating down';
END;
$$;
-- +goose StatementEnd
