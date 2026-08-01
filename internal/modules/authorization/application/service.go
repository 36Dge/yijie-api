package application

import (
	"context"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
)

type PermissionRepository interface {
	HasPermission(ctx context.Context, scope tenancy.Scope, permission domain.Permission) (bool, error)
}

type Service struct {
	permissions PermissionRepository
}

func New(permissions PermissionRepository) *Service {
	return &Service{permissions: permissions}
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
