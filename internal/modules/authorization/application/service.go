package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

const (
	MaxProjectionTTL          = 5 * time.Minute
	MaxProjectionCapabilities = 256
)

type PermissionRepository interface {
	HasPermission(ctx context.Context, scope tenancy.Scope, permission domain.Permission) (bool, error)
	ReadProjection(ctx context.Context, userID, tenantID uuid.UUID) (tenancy.Scope, []domain.Permission, error)
}

type Service struct {
	permissions PermissionRepository
	clock       func() time.Time
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

func New(permissions PermissionRepository, options ...Option) *Service {
	service := &Service{permissions: permissions, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (service *Service) Check(ctx context.Context, scope tenancy.Scope, permission domain.Permission) error {
	if service == nil || service.permissions == nil || !scope.Valid() {
		return domain.ErrInvalidAuthorizationInput
	}
	if !permission.Known() {
		return domain.ErrUnknownPermission
	}
	allowed, err := service.permissions.HasPermission(ctx, scope, permission)
	if err != nil {
		return fmt.Errorf("check permission: %w", domain.ErrAuthorizationUnavailable)
	}
	if !allowed {
		return domain.ErrDenied
	}
	return nil
}

func (service *Service) Project(ctx context.Context, userID, tenantID uuid.UUID) (domain.Projection, error) {
	if userID == uuid.Nil || tenantID == uuid.Nil || service == nil || service.permissions == nil || service.clock == nil {
		return domain.Projection{}, domain.ErrInvalidAuthorizationInput
	}
	scope, permissions, err := service.permissions.ReadProjection(ctx, userID, tenantID)
	if errors.Is(err, domain.ErrDenied) {
		return domain.Projection{}, domain.ErrDenied
	}
	if err != nil {
		return domain.Projection{}, fmt.Errorf("read permission projection: %w", domain.ErrAuthorizationUnavailable)
	}
	if !scope.Valid() || scope.UserID != userID || scope.TenantID != tenantID || len(permissions) > MaxProjectionCapabilities {
		return domain.Projection{}, domain.ErrInvalidProjection
	}

	// The legacy public capability enum remains unchanged. Web eligibility is
	// resolved at the dedicated delegation endpoint using the same RBAC store.
	result := slices.DeleteFunc(slices.Clone(permissions), func(p domain.Permission) bool { return p == domain.WebRetrieve })
	slices.Sort(result)
	result = slices.Compact(result)
	for _, permission := range result {
		if !permission.Known() {
			return domain.Projection{}, domain.ErrInvalidProjection
		}
	}
	return domain.Projection{
		TenantID:              scope.TenantID,
		AuthorizationRevision: scope.AuthorizationRevision,
		ExpiresAt:             service.clock().UTC().Add(MaxProjectionTTL),
		Permissions:           result,
	}, nil
}
