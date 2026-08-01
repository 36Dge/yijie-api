package application

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type fakePermissions struct {
	scope       tenancy.Scope
	permissions []domain.Permission
	allowed     bool
	err         error
}

func (repository fakePermissions) ReadProjection(context.Context, uuid.UUID, uuid.UUID) (tenancy.Scope, []domain.Permission, error) {
	return repository.scope, repository.permissions, repository.err
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

func TestProjectReturnsAtomicSortedCapabilitySnapshot(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	service := New(
		fakePermissions{
			scope:       tenancy.Scope{UserID: userID, TenantID: tenantID, AuthorizationRevision: 42},
			permissions: []domain.Permission{domain.TaskRead, domain.ScheduleRead, domain.TaskRead, domain.TaskCreate},
		},
		WithClock(func() time.Time { return now }),
	)

	projection, err := service.Project(context.Background(), userID, tenantID)
	if err != nil {
		t.Fatalf("project capabilities: %v", err)
	}
	if projection.TenantID != tenantID || projection.AuthorizationRevision != 42 {
		t.Fatalf("unexpected projection context: %#v", projection)
	}
	if !projection.ExpiresAt.Equal(now.Add(MaxProjectionTTL)) {
		t.Fatalf("unexpected expiry: %s", projection.ExpiresAt)
	}
	expected := []domain.Permission{domain.ScheduleRead, domain.TaskCreate, domain.TaskRead}
	if !slices.Equal(projection.Permissions, expected) {
		t.Fatalf("expected %v, got %v", expected, projection.Permissions)
	}
}

func TestProjectFailsClosed(t *testing.T) {
	userID := uuid.New()
	tenantID := uuid.New()
	validScope := tenancy.Scope{UserID: userID, TenantID: tenantID, AuthorizationRevision: 1}
	tests := []struct {
		name       string
		repository fakePermissions
		userID     uuid.UUID
		tenantID   uuid.UUID
		expected   error
	}{
		{name: "zero user", userID: uuid.Nil, tenantID: tenantID, expected: domain.ErrInvalidAuthorizationInput},
		{name: "zero tenant", userID: userID, tenantID: uuid.Nil, expected: domain.ErrInvalidAuthorizationInput},
		{name: "tenant denied", repository: fakePermissions{err: domain.ErrDenied}, userID: userID, tenantID: tenantID, expected: domain.ErrDenied},
		{name: "repository unavailable", repository: fakePermissions{err: errors.New("database unavailable")}, userID: userID, tenantID: tenantID, expected: domain.ErrAuthorizationUnavailable},
		{name: "wrong scope", repository: fakePermissions{scope: tenancy.Scope{UserID: userID, TenantID: uuid.New(), AuthorizationRevision: 1}}, userID: userID, tenantID: tenantID, expected: domain.ErrInvalidProjection},
		{name: "unknown permission", repository: fakePermissions{scope: validScope, permissions: []domain.Permission{"admin.all"}}, userID: userID, tenantID: tenantID, expected: domain.ErrInvalidProjection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.repository)
			if _, err := service.Project(context.Background(), test.userID, test.tenantID); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
		})
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
