-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_identities (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    issuer text NOT NULL CHECK (octet_length(issuer) BETWEEN 1 AND 1024),
    subject text NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);

CREATE INDEX user_identities_user_id_idx ON user_identities (user_id);

CREATE TABLE tenants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    authorization_revision bigint NOT NULL DEFAULT 1
        CHECK (authorization_revision BETWEEN 1 AND 9007199254740991),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tenant_memberships (
    tenant_id uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE INDEX tenant_memberships_user_status_tenant_idx
    ON tenant_memberships (user_id, status, tenant_id);

CREATE TABLE permissions (
    permission_key text PRIMARY KEY
        CHECK (
            char_length(permission_key) BETWEEN 3 AND 128
            AND permission_key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'
        ),
    description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500)
);

INSERT INTO permissions (permission_key, description) VALUES
    ('knowledge.read', 'Read knowledge resources'),
    ('plugin.read', 'Read plugin resources'),
    ('schedule.read', 'Read scheduled tasks'),
    ('store.read', 'Read store resources'),
    ('task.create', 'Create tasks'),
    ('task.read', 'Read tasks'),
    ('workspace.use', 'Use workspace resources');

CREATE TABLE roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
    role_key text NOT NULL
        CHECK (
            char_length(role_key) BETWEEN 3 AND 100
            AND role_key ~ '^[a-z][a-z0-9_]*$'
        ),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    is_system boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, role_key),
    UNIQUE (tenant_id, id)
);

CREATE TABLE role_permissions (
    tenant_id uuid NOT NULL,
    role_id uuid NOT NULL,
    permission_key text NOT NULL REFERENCES permissions (permission_key) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, role_id, permission_key),
    FOREIGN KEY (tenant_id, role_id) REFERENCES roles (tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX role_permissions_permission_key_idx ON role_permissions (permission_key);

CREATE TABLE membership_roles (
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, role_id),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES tenant_memberships (tenant_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES roles (tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX membership_roles_tenant_role_idx ON membership_roles (tenant_id, role_id);

ALTER TABLE audit_logs
    DROP CONSTRAINT audit_logs_resource_id_fkey,
    ALTER COLUMN resource_id DROP NOT NULL,
    ADD COLUMN resource_key text
        CHECK (resource_key IS NULL OR char_length(resource_key) BETWEEN 1 AND 200),
    ADD CONSTRAINT audit_logs_resource_locator_check
        CHECK (resource_id IS NOT NULL OR resource_key IS NOT NULL);

CREATE INDEX audit_logs_tenant_resource_id_idx
    ON audit_logs (tenant_id, resource_type, resource_id)
    WHERE resource_id IS NOT NULL;

CREATE INDEX audit_logs_tenant_resource_key_idx
    ON audit_logs (tenant_id, resource_type, resource_key)
    WHERE resource_key IS NOT NULL;

-- +goose Down
-- This migration is expand-only. Application rollback retains the authorization schema;
-- recovery must roll forward so goose metadata never claims the retained schema is absent.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 00002 is expand-only; roll forward instead of migrating down';
END;
$$;
-- +goose StatementEnd
