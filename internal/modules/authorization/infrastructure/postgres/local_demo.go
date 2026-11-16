package postgres

import (
	"context"
	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	"github.com/google/uuid"
)

// EnsureLocalDemo creates the approved fixed local principal once. An existing
// suspended membership or explicitly removed permission is never restored.
func (repository *Repository) EnsureLocalDemo(ctx context.Context, user, tenant uuid.UUID) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id) VALUES($1) ON CONFLICT DO NOTHING`, []any{user}},
		{`INSERT INTO tenants(id,name) VALUES($1,'Local Demo') ON CONFLICT DO NOTHING`, []any{tenant}},
		{`INSERT INTO tenant_memberships(tenant_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, []any{tenant, user}},
	} {
		if _, err = tx.Exec(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	var role uuid.UUID
	tag, err := tx.Exec(ctx, `INSERT INTO roles(id,tenant_id,role_key,name,is_system) VALUES($1,$2,'local_demo','Local Demo',true) ON CONFLICT(tenant_id,role_key) DO NOTHING`, uuid.New(), tenant)
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM roles WHERE tenant_id=$1 AND role_key='local_demo'`, tenant).Scan(&role); err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		for _, permission := range domain.DefaultProductPermissions() {
			if _, err = tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission_key) VALUES($1,$2,$3)`, tenant, role, permission); err != nil {
				return err
			}
		}
	}
	if tag.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, `INSERT INTO membership_roles(tenant_id,user_id,role_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, tenant, user, role); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
