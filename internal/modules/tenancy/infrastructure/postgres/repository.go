package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
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

func (repository *Repository) VerifyActive(ctx context.Context, userID, tenantID uuid.UUID) (domain.Scope, error) {
	if repository == nil || repository.pool == nil {
		return domain.Scope{}, fmt.Errorf("query active tenant membership: %w", domain.ErrTenancyUnavailable)
	}
	var scope domain.Scope
	err := repository.pool.QueryRow(ctx, `
		SELECT membership.user_id, membership.tenant_id, tenant.authorization_revision
		FROM tenant_memberships AS membership
		JOIN users AS internal_user ON internal_user.id = membership.user_id
		JOIN tenants AS tenant ON tenant.id = membership.tenant_id
		WHERE membership.user_id = $1
		  AND membership.tenant_id = $2
		  AND membership.status = 'active'
		  AND internal_user.status = 'active'
		  AND tenant.status = 'active'
	`, userID, tenantID).Scan(&scope.UserID, &scope.TenantID, &scope.AuthorizationRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Scope{}, domain.ErrTenantAccessDenied
	}
	if err != nil {
		return domain.Scope{}, fmt.Errorf("query active tenant membership: %w", err)
	}
	if !scope.Valid() {
		return domain.Scope{}, domain.ErrTenantAccessDenied
	}
	return scope, nil
}
