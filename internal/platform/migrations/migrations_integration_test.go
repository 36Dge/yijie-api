//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestExpandMigrationPreservesExistingDataAndTenantIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn, cleanup := migrationDatabase(t, ctx)
	defer cleanup()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open migration database: %v", err)
	}
	defer db.Close()
	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, db, "sql", 1); err != nil {
		t.Fatalf("migrate existing schema to version 1: %v", err)
	}

	taskID := uuid.New()
	tenantID := uuid.New()
	auditID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tasks (id, tenant_id, task_type, title, input)
		VALUES ($1, $2, 'migration_test', 'Existing task', '{}'::jsonb)
	`, taskID, tenantID); err != nil {
		t.Fatalf("seed existing task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs
			(id, tenant_id, actor_type, action, resource_type, resource_id, request_id, outcome)
		VALUES ($1, $2, 'system', 'task.created', 'task', $3, 'migration-existing', 'success')
	`, auditID, tenantID, taskID); err != nil {
		t.Fatalf("seed existing audit: %v", err)
	}

	if err := goose.UpToContext(ctx, db, "sql", LatestVersion); err != nil {
		t.Fatalf("apply expand migration: %v", err)
	}

	var taskCount, auditCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE id = $1", taskID).Scan(&taskCount); err != nil {
		t.Fatalf("count retained task: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs WHERE id = $1 AND resource_id = $2", auditID, taskID).Scan(&auditCount); err != nil {
		t.Fatalf("count retained audit: %v", err)
	}
	if taskCount != 1 || auditCount != 1 {
		t.Fatalf("existing data was not retained: tasks=%d audits=%d", taskCount, auditCount)
	}
	var legacyOwner *uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT created_by_user_id FROM tasks WHERE id = $1", taskID).Scan(&legacyOwner); err != nil {
		t.Fatalf("read legacy task owner after secure expansion: %v", err)
	}
	if legacyOwner != nil {
		t.Fatalf("legacy task was assigned an owner by guess: %v", *legacyOwner)
	}
	var secureIndex, idempotencyTable *string
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('tasks_creator_private_v2_idx')::text, to_regclass('task_create_idempotency')::text`).Scan(&secureIndex, &idempotencyTable); err != nil {
		t.Fatalf("inspect secure task expansion: %v", err)
	}
	if secureIndex == nil || idempotencyTable == nil {
		t.Fatalf("secure task migration is incomplete: index=%v idempotency=%v", secureIndex, idempotencyTable)
	}
	var expiresAtNullable, auditTenantNullable string
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT is_nullable FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'task_create_idempotency' AND column_name = 'expires_at'),
			(SELECT is_nullable FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'audit_logs' AND column_name = 'tenant_id')
	`).Scan(&expiresAtNullable, &auditTenantNullable); err != nil {
		t.Fatalf("inspect secure task hardening migration: %v", err)
	}
	if expiresAtNullable != "NO" || auditTenantNullable != "YES" {
		t.Fatalf("unexpected hardening nullability: expires=%s audit_tenant=%s", expiresAtNullable, auditTenantNullable)
	}

	rows, err := db.QueryContext(ctx, "SELECT permission_key FROM permissions ORDER BY permission_key")
	if err != nil {
		t.Fatalf("query permission vocabulary: %v", err)
	}
	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			rows.Close()
			t.Fatalf("scan permission vocabulary: %v", err)
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close permission rows: %v", err)
	}
	expectedPermissions := []string{
		"knowledge.read",
		"plugin.read",
		"schedule.read",
		"store.read",
		"task.create",
		"task.read",
		"workspace.use",
	}
	if !slices.Equal(permissions, expectedPermissions) {
		t.Fatalf("unexpected permission vocabulary: %#v", permissions)
	}

	var sessionsTable *string
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('sessions')::text").Scan(&sessionsTable); err != nil {
		t.Fatalf("check sessions table absence: %v", err)
	}
	if sessionsTable != nil {
		t.Fatalf("expand migration unexpectedly created sessions table %q", *sessionsTable)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs
			(tenant_id, actor_type, action, resource_type, resource_key, request_id, outcome)
		VALUES ($1, 'system', 'permission.seeded', 'permission', 'task.read', 'migration-resource-key', 'success')
	`, tenantID); err != nil {
		t.Fatalf("insert generic resource-key audit: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs
			(tenant_id, actor_type, action, resource_type, request_id, outcome)
		VALUES ($1, 'system', 'invalid', 'permission', 'migration-no-locator', 'failure')
	`, tenantID); err == nil {
		t.Fatal("expected audit resource locator constraint to reject an unlocated record")
	}
	if _, err := db.ExecContext(ctx, "UPDATE audit_logs SET outcome = 'failure' WHERE id = $1", auditID); err == nil {
		t.Fatal("expected append-only trigger to survive expansion")
	}

	userID := uuid.New()
	tenantA := uuid.New()
	tenantB := uuid.New()
	roleB := uuid.New()
	if _, err := db.ExecContext(ctx, "INSERT INTO users (id) VALUES ($1)", userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO tenants (id, name) VALUES ($1, 'Tenant A'), ($2, 'Tenant B')", tenantA, tenantB); err != nil {
		t.Fatalf("insert tenants: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO tenant_memberships (tenant_id, user_id) VALUES ($1, $2)", tenantA, userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO roles (id, tenant_id, role_key, name) VALUES ($1, $2, 'tenant_owner', 'Owner')", roleB, tenantB); err != nil {
		t.Fatalf("insert tenant B role: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO membership_roles (tenant_id, user_id, role_id) VALUES ($1, $2, $3)", tenantA, userID, roleB); err == nil {
		t.Fatal("expected composite foreign key to reject a cross-tenant role assignment")
	}

	if err := goose.DownContext(ctx, db, "sql"); err == nil || !strings.Contains(err.Error(), "forward-only") {
		t.Fatalf("expected explicit forward-only down failure, got %v", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("read migration version after rejected down: %v", err)
	}
	if version != LatestVersion {
		t.Fatalf("rejected down changed migration version to %d", version)
	}
}

func migrationDatabase(t *testing.T, ctx context.Context) (string, func()) {
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
	return parsed.String(), func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	}
}
