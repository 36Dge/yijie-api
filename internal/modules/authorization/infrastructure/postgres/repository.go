package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (repository *Repository) ReadProjection(ctx context.Context, userID, tenantID uuid.UUID) (tenancy.Scope, []domain.Permission, error) {
	if repository == nil || repository.pool == nil {
		return tenancy.Scope{}, nil, fmt.Errorf("query permission projection: %w", domain.ErrAuthorizationUnavailable)
	}
	var scope tenancy.Scope
	var rawPermissions []string
	err := repository.pool.QueryRow(ctx, `
		WITH active_scope AS (
			SELECT membership.user_id, membership.tenant_id, tenant.authorization_revision
			FROM tenant_memberships AS membership
			JOIN users AS internal_user ON internal_user.id = membership.user_id
			JOIN tenants AS tenant ON tenant.id = membership.tenant_id
			WHERE membership.user_id = $1
			  AND membership.tenant_id = $2
			  AND membership.status = 'active'
			  AND internal_user.status = 'active'
			  AND tenant.status = 'active'
		),
		capability_set AS (
			SELECT DISTINCT role_permission.permission_key
			FROM active_scope AS scope
			JOIN membership_roles AS assignment
			  ON assignment.tenant_id = scope.tenant_id
			 AND assignment.user_id = scope.user_id
			JOIN roles AS role
			  ON role.tenant_id = assignment.tenant_id
			 AND role.id = assignment.role_id
			JOIN role_permissions AS role_permission
			  ON role_permission.tenant_id = role.tenant_id
			 AND role_permission.role_id = role.id
			JOIN permissions AS permission
			  ON permission.permission_key = role_permission.permission_key
		)
		SELECT scope.user_id,
		       scope.tenant_id,
		       scope.authorization_revision,
		       COALESCE(
		           array_agg(capability.permission_key ORDER BY capability.permission_key)
		               FILTER (WHERE capability.permission_key IS NOT NULL),
		           ARRAY[]::text[]
		       )
		FROM active_scope AS scope
		LEFT JOIN capability_set AS capability ON true
		GROUP BY scope.user_id, scope.tenant_id, scope.authorization_revision
	`, userID, tenantID).Scan(
		&scope.UserID,
		&scope.TenantID,
		&scope.AuthorizationRevision,
		&rawPermissions,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenancy.Scope{}, nil, domain.ErrDenied
	}
	if err != nil {
		return tenancy.Scope{}, nil, fmt.Errorf("query permission projection: %w", err)
	}
	permissions := make([]domain.Permission, len(rawPermissions))
	for index, permission := range rawPermissions {
		permissions[index] = domain.Permission(permission)
	}
	return scope, permissions, nil
}
