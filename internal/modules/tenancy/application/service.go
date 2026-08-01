package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type MembershipRepository interface {
	VerifyActive(ctx context.Context, userID, tenantID uuid.UUID) (domain.Scope, error)
}

type Service struct {
	memberships MembershipRepository
}

func New(memberships MembershipRepository) *Service {
	return &Service{memberships: memberships}
}

func (service *Service) VerifyActive(ctx context.Context, userID, tenantID uuid.UUID) (domain.Scope, error) {
	if userID == uuid.Nil || tenantID == uuid.Nil || service == nil || service.memberships == nil {
		return domain.Scope{}, domain.ErrInvalidScope
	}
	scope, err := service.memberships.VerifyActive(ctx, userID, tenantID)
	if errors.Is(err, domain.ErrTenantAccessDenied) {
		return domain.Scope{}, domain.ErrTenantAccessDenied
	}
	if err != nil {
		return domain.Scope{}, fmt.Errorf("verify tenant membership: %w", domain.ErrTenancyUnavailable)
	}
	if !scope.Valid() || scope.UserID != userID || scope.TenantID != tenantID {
		return domain.Scope{}, domain.ErrTenantAccessDenied
	}
	return scope, nil
}
