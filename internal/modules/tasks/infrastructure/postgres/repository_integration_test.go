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

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSecureRepositoryEnforcesCreatorScopeAndIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _, cleanup := integrationDatabase(t, ctx)
	defer cleanup()

	tenantID := uuid.New()
	creatorID := uuid.New()
	otherUserID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id) VALUES ($1), ($2)", creatorID, otherUserID); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ($1, 'Synthetic tenant')", tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	repository := New(pool)
	command := domain.CreateSecureTask{
		SecureScope: domain.SecureScope{TenantID: tenantID.String(), UserID: creatorID.String()},
		TaskType:    "conversation", Input: []byte(`{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}`),
		IdempotencyKey: uuid.NewString(), CanonicalSHA256: strings.Repeat("a", 64),
		RequestID: "request-secure-1", TraceID: "trace-secure-1",
	}
	created, err := repository.CreateSecure(ctx, command)
	if err != nil {
		t.Fatalf("create secure task: %v", err)
	}
	replayed, err := repository.CreateSecure(ctx, command)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("idempotent replay: task=%#v err=%v", replayed, err)
	}
	conflict := command
	conflict.CanonicalSHA256 = strings.Repeat("b", 64)
	if _, err := repository.CreateSecure(ctx, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	if _, err := repository.GetSecure(ctx, domain.SecureScope{TenantID: tenantID.String(), UserID: otherUserID.String()}, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-creator read must be hidden as not found, got %v", err)
	}
	var taskCount, auditCount, idempotencyCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE id = $1", created.ID).Scan(&taskCount); err != nil {
		t.Fatalf("count secure tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE resource_id = $1 AND actor_id = $2", created.ID, creatorID.String()).Scan(&auditCount); err != nil {
		t.Fatalf("count secure task audits: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM task_create_idempotency WHERE task_id = $1", created.ID).Scan(&idempotencyCount); err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if taskCount != 1 || auditCount != 2 || idempotencyCount != 1 {
		t.Fatalf("unexpected atomic counts: tasks=%d audits=%d idempotency=%d", taskCount, auditCount, idempotencyCount)
	}
	if _, err := pool.Exec(ctx, "UPDATE task_create_idempotency SET created_at = now() - interval '48 hours', expires_at = now() - interval '24 hours' WHERE task_id = $1", created.ID); err != nil {
		t.Fatalf("expire idempotency record: %v", err)
	}
	expired := command
	expired.CanonicalSHA256 = strings.Repeat("c", 64)
	expired.Input = []byte(`{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f38"}`)
	recreated, err := repository.CreateSecure(ctx, expired)
	if err != nil || recreated.ID == created.ID {
		t.Fatalf("expired idempotency key was not reusable: task=%#v err=%v", recreated, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE task_create_idempotency SET created_at = now() - interval '48 hours', expires_at = now() - interval '24 hours' WHERE task_id = $1", recreated.ID); err != nil {
		t.Fatalf("expire replacement idempotency record: %v", err)
	}
	cleaned, err := repository.CleanupExpiredSecureIdempotency(ctx, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("bounded idempotency cleanup: cleaned=%d err=%v", cleaned, err)
	}
}

func TestSecureAuditSupportsContentFreeUnresolvedAndVerifiedMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _, cleanup := integrationDatabase(t, ctx)
	defer cleanup()

	repository := New(pool)
	requestID := uuid.NewString()
	if err := repository.RecordSecureAudit(ctx, domain.SecureTaskAudit{
		Operation: "task.create", Outcome: "failure", Code: "unauthenticated",
		RequestID: requestID, StatusCode: 401,
	}); err != nil {
		t.Fatalf("record unresolved audit: %v", err)
	}
	var tenantID, actorID *string
	var metadata string
	if err := pool.QueryRow(ctx, `
		SELECT tenant_id::text, actor_id, metadata::text
		FROM audit_logs WHERE request_id = $1
	`, requestID).Scan(&tenantID, &actorID, &metadata); err != nil {
		t.Fatalf("read unresolved audit: %v", err)
	}
	if tenantID != nil || actorID != nil || strings.Contains(metadata, "prompt") || !strings.Contains(metadata, "unauthenticated") {
		t.Fatalf("audit crossed unresolved/content-free boundary: tenant=%v actor=%v metadata=%s", tenantID, actorID, metadata)
	}
}

func TestRepositoryPersistsTaskAndAuditAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, scopedDSN, cleanup := integrationDatabase(t, ctx)
	defer cleanup()

	repository := New(pool)
	tenantID := "9d1e1888-7ecf-42f6-8cbf-4d24101a801c"
	if _, err := pool.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ($1, 'Synthetic legacy tenant')", tenantID); err != nil {
		t.Fatalf("seed legacy tenant: %v", err)
	}
	command := domain.CreateTask{
		TenantID:  tenantID,
		TaskType:  "listing_diagnosis",
		Title:     "Product review",
		Input:     []byte(`{"url":"https://example.com/product"}`),
		RequestID: "request-1",
		TraceID:   "trace-1",
	}
	created, err := repository.Create(ctx, command)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE resource_id = $1", created.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one audit log, got %d", auditCount)
	}
	if _, err := pool.Exec(ctx, "UPDATE audit_logs SET outcome = 'failure' WHERE resource_id = $1", created.ID); err == nil {
		t.Fatal("expected append-only audit update to fail")
	}

	pool.Close()
	pool, err = database.Open(ctx, scopedDSN, 0, 5)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer pool.Close()
	repository = New(pool)
	persisted, err := repository.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get task after reconnect: %v", err)
	}
	if persisted.Title != command.Title || persisted.Status != "draft" {
		t.Fatalf("unexpected persisted task: %#v", persisted)
	}

	var before, after int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks").Scan(&before); err != nil {
		t.Fatalf("count tasks before failed transaction: %v", err)
	}
	invalidAudit := command
	invalidAudit.RequestID = strings.Repeat("x", 201)
	if _, err := repository.Create(ctx, invalidAudit); err == nil {
		t.Fatal("expected audit constraint failure")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks").Scan(&after); err != nil {
		t.Fatalf("count tasks after failed transaction: %v", err)
	}
	if before != after {
		t.Fatalf("task insert was not rolled back: before=%d after=%d", before, after)
	}
}

func integrationDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, string, func()) {
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
	scopedDSN := withSearchPath(t, baseDSN, schema)
	if err := migrations.Run(ctx, scopedDSN, "up"); err != nil {
		admin.Close()
		t.Fatalf("run integration migrations: %v", err)
	}
	pool, err := database.Open(ctx, scopedDSN, 0, 5)
	if err != nil {
		admin.Close()
		t.Fatalf("open migrated integration database: %v", err)
	}
	if err := database.RequireSchema(ctx, pool); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("verify integration schema: %v", err)
	}
	return pool, scopedDSN, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	}
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
