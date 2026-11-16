-- +goose Up
-- FEAT-175 user-approved default entitlement. Existing task-capable product
-- roles get web retrieval once; later explicit revocation stays effective.
WITH granted AS (
    INSERT INTO role_permissions(tenant_id,role_id,permission_key)
    SELECT DISTINCT tenant_id,role_id,'web.retrieve'
    FROM role_permissions WHERE permission_key='task.create'
    ON CONFLICT DO NOTHING RETURNING tenant_id
)
UPDATE tenants SET authorization_revision=authorization_revision+1,updated_at=now()
WHERE id IN (SELECT tenant_id FROM granted);

-- +goose Down
-- Authorization rollback disables retrieval; it does not discard operation state.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'migration 00006 is expand-only; roll forward'; END $$;
-- +goose StatementEnd
