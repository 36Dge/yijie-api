package postgres

import (
	"context"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) HasPermission(ctx context.Context, scope tenancy.Scope, permission domain.Permission) (bool, error) {
	if repository == nil || repository.pool == nil {
		return false, fmt.Errorf("query permission assignment: %w", domain.ErrAuthorizationUnavailable)
	}
	var allowed bool
	err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM tenant_memberships AS membership
			JOIN users AS internal_user ON internal_user.id = membership.user_id
			JOIN tenants AS tenant ON tenant.id = membership.tenant_id
			JOIN membership_roles AS assignment
			  ON assignment.tenant_id = membership.tenant_id
			 AND assignment.user_id = membership.user_id
			JOIN roles AS role
			  ON role.tenant_id = assignment.tenant_id
			 AND role.id = assignment.role_id
			JOIN role_permissions AS role_permission
			  ON role_permission.tenant_id = role.tenant_id
			 AND role_permission.role_id = role.id
			JOIN permissions AS permission
			  ON permission.permission_key = role_permission.permission_key
			WHERE membership.tenant_id = $1
			  AND membership.user_id = $2
			  AND membership.status = 'active'
			  AND internal_user.status = 'active'
			  AND tenant.status = 'active'
			  AND tenant.authorization_revision = $4
			  AND permission.permission_key = $3
		)
	`, scope.TenantID, scope.UserID, permission, scope.AuthorizationRevision).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("query permission assignment: %w", err)
	}
	return allowed, nil
}
