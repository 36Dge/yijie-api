//go:build integration

package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	authorizationapplication "github.com/36Dge/yijie-api/internal/modules/authorization/application"
	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	identityapplication "github.com/36Dge/yijie-api/internal/modules/identity/application"
	identitydomain "github.com/36Dge/yijie-api/internal/modules/identity/domain"
	identitypostgres "github.com/36Dge/yijie-api/internal/modules/identity/infrastructure/postgres"
	tenancyapplication "github.com/36Dge/yijie-api/internal/modules/tenancy/application"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	tenancypostgres "github.com/36Dge/yijie-api/internal/modules/tenancy/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthoritativeIdentityTenantAndRBACMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := authorizationDatabase(t, ctx)
	defer cleanup()

	fixture := seedAuthorizationMatrix(t, ctx, pool)
	identities := identityapplication.New(identitypostgres.New(pool))
	tenancies := tenancyapplication.New(tenancypostgres.New(pool))
	authorization := authorizationapplication.New(New(pool))

	principal, err := identities.ResolvePrincipal(ctx, fixture.issuer, fixture.ownerSubject)
	if err != nil {
		t.Fatalf("resolve owner principal: %v", err)
	}
	if principal.UserID != fixture.ownerID {
		t.Fatalf("resolved owner ID %s, expected %s", principal.UserID, fixture.ownerID)
	}
	if _, err := identities.ResolvePrincipal(ctx, fixture.issuer, "unknown-subject"); !errors.Is(err, identitydomain.ErrIdentityNotFound) {
		t.Fatalf("expected unknown identity to fail closed, got %v", err)
	}

	type matrixEntry struct {
		name    string
		userID  uuid.UUID
		tenant  uuid.UUID
		allowed map[domain.Permission]bool
	}
	ownerPermissions := make(map[domain.Permission]bool, len(domain.InitialPermissions()))
	for _, permission := range domain.InitialPermissions() {
		ownerPermissions[permission] = true
	}
	memberPermissions := map[domain.Permission]bool{domain.TaskCreate: true, domain.TaskRead: true}
	matrix := []matrixEntry{
		{name: "owner in tenant A", userID: fixture.ownerID, tenant: fixture.tenantA, allowed: ownerPermissions},
		{name: "member in tenant A", userID: fixture.memberID, tenant: fixture.tenantA, allowed: memberPermissions},
		{name: "member in tenant B", userID: fixture.ownerID, tenant: fixture.tenantB, allowed: memberPermissions},
		{name: "owner in tenant B", userID: fixture.memberID, tenant: fixture.tenantB, allowed: ownerPermissions},
	}
	for _, entry := range matrix {
		t.Run(entry.name, func(t *testing.T) {
			scope, err := tenancies.VerifyActive(ctx, entry.userID, entry.tenant)
			if err != nil {
				t.Fatalf("verify tenant membership: %v", err)
			}
			for _, permission := range domain.InitialPermissions() {
				err := authorization.Check(ctx, scope, permission)
				if entry.allowed[permission] {
					if err != nil {
						t.Errorf("permission %q: expected allow, got %v", permission, err)
					}
					continue
				}
				if !errors.Is(err, domain.ErrDenied) {
					t.Errorf("permission %q: expected deny, got %v", permission, err)
				}
			}
		})
	}

	ownerScope, err := tenancies.VerifyActive(ctx, fixture.ownerID, fixture.tenantA)
	if err != nil {
		t.Fatalf("load scope before revision update: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tenants SET authorization_revision = authorization_revision + 1 WHERE id = $1", fixture.tenantA); err != nil {
		t.Fatalf("increment tenant authorization revision: %v", err)
	}
	if err := authorization.Check(ctx, ownerScope, domain.TaskRead); !errors.Is(err, domain.ErrDenied) {
		t.Fatalf("expected stale authorization scope to deny, got %v", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE tenant_memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2", fixture.tenantB, fixture.memberID); err != nil {
		t.Fatalf("suspend tenant membership: %v", err)
	}
	if _, err := tenancies.VerifyActive(ctx, fixture.memberID, fixture.tenantB); !errors.Is(err, tenancydomain.ErrTenantAccessDenied) {
		t.Fatalf("expected suspended membership to deny tenant access, got %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET status = 'suspended' WHERE id = $1", fixture.ownerID); err != nil {
		t.Fatalf("suspend owner user: %v", err)
	}
	if _, err := tenancies.VerifyActive(ctx, fixture.ownerID, fixture.tenantB); !errors.Is(err, tenancydomain.ErrTenantAccessDenied) {
		t.Fatalf("expected suspended user to deny tenant access, got %v", err)
	}
}

type authorizationFixture struct {
	issuer       string
	ownerSubject string
	ownerID      uuid.UUID
	memberID     uuid.UUID
	tenantA      uuid.UUID
	tenantB      uuid.UUID
	ownerRoleA   uuid.UUID
	memberRoleA  uuid.UUID
	ownerRoleB   uuid.UUID
	memberRoleB  uuid.UUID
}

func seedAuthorizationMatrix(t *testing.T, ctx context.Context, pool *pgxpool.Pool) authorizationFixture {
	t.Helper()
	fixture := authorizationFixture{
		issuer:       "https://identity.integration.test/",
		ownerSubject: "owner-subject",
		ownerID:      uuid.New(),
		memberID:     uuid.New(),
		tenantA:      uuid.New(),
		tenantB:      uuid.New(),
		ownerRoleA:   uuid.New(),
		memberRoleA:  uuid.New(),
		ownerRoleB:   uuid.New(),
		memberRoleB:  uuid.New(),
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "INSERT INTO users (id) VALUES ($1), ($2)", fixture.ownerID, fixture.memberID); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, issuer, subject)
		VALUES ($1, $3, $4), ($2, $3, 'member-subject')
	`, fixture.ownerID, fixture.memberID, fixture.issuer, fixture.ownerSubject); err != nil {
		t.Fatalf("insert identities: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO tenants (id, name, authorization_revision) VALUES ($1, 'Tenant A', 7), ($2, 'Tenant B', 11)", fixture.tenantA, fixture.tenantB); err != nil {
		t.Fatalf("insert tenants: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_id)
		VALUES ($1, $3), ($1, $4), ($2, $3), ($2, $4)
	`, fixture.tenantA, fixture.tenantB, fixture.ownerID, fixture.memberID); err != nil {
		t.Fatalf("insert memberships: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO roles (id, tenant_id, role_key, name, is_system) VALUES
			($1, $5, 'tenant_owner', 'Tenant owner', true),
			($2, $5, 'tenant_member', 'Tenant member', true),
			($3, $6, 'tenant_owner', 'Tenant owner', true),
			($4, $6, 'tenant_member', 'Tenant member', true)
	`, fixture.ownerRoleA, fixture.memberRoleA, fixture.ownerRoleB, fixture.memberRoleB, fixture.tenantA, fixture.tenantB); err != nil {
		t.Fatalf("insert roles: %v", err)
	}
	for _, role := range []struct {
		tenantID    uuid.UUID
		roleID      uuid.UUID
		permissions []domain.Permission
	}{
		{fixture.tenantA, fixture.ownerRoleA, domain.InitialPermissions()},
		{fixture.tenantA, fixture.memberRoleA, []domain.Permission{domain.TaskCreate, domain.TaskRead}},
		{fixture.tenantB, fixture.ownerRoleB, domain.InitialPermissions()},
		{fixture.tenantB, fixture.memberRoleB, []domain.Permission{domain.TaskCreate, domain.TaskRead}},
	} {
		for _, permission := range role.permissions {
			if _, err := tx.Exec(ctx, "INSERT INTO role_permissions (tenant_id, role_id, permission_key) VALUES ($1, $2, $3)", role.tenantID, role.roleID, permission); err != nil {
				t.Fatalf("insert role permission: %v", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO membership_roles (tenant_id, user_id, role_id) VALUES
			($1, $3, $5),
			($1, $4, $6),
			($2, $3, $8),
			($2, $4, $7)
	`, fixture.tenantA, fixture.tenantB, fixture.ownerID, fixture.memberID, fixture.ownerRoleA, fixture.memberRoleA, fixture.ownerRoleB, fixture.memberRoleB); err != nil {
		t.Fatalf("insert role assignments: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture transaction: %v", err)
	}
	return fixture
}

func authorizationDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseDSN := os.Getenv("YIJIE_API_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("YIJIE_API_TEST_POSTGRES_DSN is not set")
	}
	admin, err := database.Open(ctx, baseDSN, 0, 5)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		admin.Close()
		t.Fatalf("parse integration DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	if err := migrations.Run(ctx, parsed.String(), "up"); err != nil {
		admin.Close()
		t.Fatalf("run integration migrations: %v", err)
	}
	pool, err := database.Open(ctx, parsed.String(), 0, 5)
	if err != nil {
		admin.Close()
		t.Fatalf("open migrated integration database: %v", err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	}
}
