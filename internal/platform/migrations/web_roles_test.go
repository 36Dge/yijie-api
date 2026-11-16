package migrations

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuiltinWebDefaultRoleUpgrade(t *testing.T) {
	dsn := os.Getenv("FEAT175_ROLE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit fresh local role test database required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/yijie_feat175_test_") {
		t.Fatal("refusing non-test database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	goose.SetBaseFS(files)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpToContext(ctx, db, "sql", 4); err != nil {
		t.Fatal(err)
	}
	tenant, role := uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{{`INSERT INTO tenants(id,name) VALUES($1,'Ordinary role upgrade')`, []any{tenant}}, {`INSERT INTO roles(id,tenant_id,role_key,name,is_system) VALUES($1,$2,'tenant_member','Tenant member',true)`, []any{role, tenant}}, {`INSERT INTO role_permissions(tenant_id,role_id,permission_key) VALUES($1,$2,'task.create')`, []any{tenant, role}}} {
		if _, err = db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = goose.UpContext(ctx, db, "sql"); err != nil {
		t.Fatal(err)
	}
	var count, revision int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission_key='web.retrieve'`, tenant, role).Scan(&count); err != nil || count != 1 {
		t.Fatal("default web entitlement missing", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT authorization_revision FROM tenants WHERE id=$1`, tenant).Scan(&revision); err != nil || revision != 2 {
		t.Fatal("authorization revision not advanced", err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission_key='web.retrieve'`, tenant, role); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, db, "sql"); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission_key='web.retrieve'`, tenant, role).Scan(&count); err != nil || count != 0 {
		t.Fatal("explicit removal was undone", err)
	}
}
