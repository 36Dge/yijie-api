package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type MembershipRepository interface {
	ListActive(ctx context.Context, userID uuid.UUID) ([]domain.Membership, error)
	VerifyActive(ctx context.Context, userID, tenantID uuid.UUID) (domain.Scope, error)
}

type Service struct {
	memberships MembershipRepository
}

func New(memberships MembershipRepository) *Service {
	return &Service{memberships: memberships}
}

func (service *Service) ListActive(ctx context.Context, userID uuid.UUID) ([]domain.Membership, error) {
	if userID == uuid.Nil || service == nil || service.memberships == nil {
		return nil, domain.ErrInvalidScope
	}
	memberships, err := service.memberships.ListActive(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list active tenant memberships: %w", domain.ErrTenancyUnavailable)
	}
	result := make([]domain.Membership, len(memberships))
	for index, membership := range memberships {
		if !membership.Valid() {
			return nil, fmt.Errorf("validate active tenant membership: %w", domain.ErrTenancyUnavailable)
		}
		result[index] = membership
	}
	return result, nil
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
