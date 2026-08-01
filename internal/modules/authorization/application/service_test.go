package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type fakePermissions struct {
	allowed bool
	err     error
}

func (repository fakePermissions) HasPermission(context.Context, tenancy.Scope, domain.Permission) (bool, error) {
	return repository.allowed, repository.err
}

func TestCheckUsesKnownPermissionAndVerifiedTenantScope(t *testing.T) {
	scope := tenancy.Scope{UserID: uuid.New(), TenantID: uuid.New(), AuthorizationRevision: 1}
	service := New(fakePermissions{allowed: true})
	if err := service.Check(context.Background(), scope, domain.TaskCreate); err != nil {
		t.Fatalf("check permission: %v", err)
	}
}

func TestCheckFailsClosed(t *testing.T) {
	validScope := tenancy.Scope{UserID: uuid.New(), TenantID: uuid.New(), AuthorizationRevision: 1}
	tests := []struct {
		name       string
		repository fakePermissions
		scope      tenancy.Scope
		permission domain.Permission
		expected   error
	}{
		{name: "invalid scope", scope: tenancy.Scope{}, permission: domain.TaskCreate, expected: domain.ErrInvalidAuthorizationInput},
		{name: "unknown permission", scope: validScope, permission: domain.Permission("admin.all"), expected: domain.ErrUnknownPermission},
		{name: "denied", repository: fakePermissions{allowed: false}, scope: validScope, permission: domain.TaskCreate, expected: domain.ErrDenied},
		{name: "repository unavailable", repository: fakePermissions{err: errors.New("database unavailable")}, scope: validScope, permission: domain.TaskCreate, expected: domain.ErrAuthorizationUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.repository)
			if err := service.Check(context.Background(), test.scope, test.permission); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
		})
	}
}

func TestInitialPermissionVocabularyIsExactAndStable(t *testing.T) {
	expected := []domain.Permission{
		domain.KnowledgeRead,
		domain.PluginRead,
		domain.ScheduleRead,
		domain.StoreRead,
		domain.TaskCreate,
		domain.TaskRead,
		domain.WorkspaceUse,
	}
	actual := domain.InitialPermissions()
	if len(actual) != len(expected) {
		t.Fatalf("expected %d permissions, got %d: %v", len(expected), len(actual), actual)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("permission %d: expected %q, got %q", index, expected[index], actual[index])
		}
	}
}
