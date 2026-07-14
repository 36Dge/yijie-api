//go:build integration

package postgres

import (
	"context"
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

func TestRepositoryPersistsTaskAndAuditAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, scopedDSN, cleanup := integrationDatabase(t, ctx)
	defer cleanup()

	repository := New(pool)
	command := domain.CreateTask{
		TenantID:  "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
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
