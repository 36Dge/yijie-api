package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type fakeMemberships struct {
	memberships []domain.Membership
	scope       domain.Scope
	err         error
}

func (repository fakeMemberships) ListActive(context.Context, uuid.UUID) ([]domain.Membership, error) {
	return repository.memberships, repository.err
}

func (repository fakeMemberships) VerifyActive(context.Context, uuid.UUID, uuid.UUID) (domain.Scope, error) {
	return repository.scope, repository.err
}

func TestVerifyActiveReturnsServerValidatedScope(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	expected := domain.Scope{UserID: userID, TenantID: tenantID, AuthorizationRevision: 42}
	service := New(fakeMemberships{scope: expected})

	scope, err := service.VerifyActive(context.Background(), userID, tenantID)
	if err != nil {
		t.Fatalf("verify membership: %v", err)
	}
	if scope != expected {
		t.Fatalf("unexpected scope: %#v", scope)
	}
}

func TestListActiveReturnsOnlyValidatedMemberships(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	expected := []domain.Membership{
		{TenantID: uuid.MustParse("019c0123-4567-7abc-8123-456789abcdee"), DisplayName: "Synthetic EU Tenant"},
		{TenantID: uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea"), DisplayName: "Synthetic US Tenant"},
	}
	service := New(fakeMemberships{memberships: expected})

	actual, err := service.ListActive(context.Background(), userID)
	if err != nil {
		t.Fatalf("list active memberships: %v", err)
	}
	if len(actual) != len(expected) || actual[0] != expected[0] || actual[1] != expected[1] {
		t.Fatalf("unexpected memberships: %#v", actual)
	}
	actual[0].DisplayName = "mutated"
	if expected[0].DisplayName == actual[0].DisplayName {
		t.Fatal("service returned repository-owned membership storage")
	}
}

func TestListActiveFailsClosed(t *testing.T) {
	userID := uuid.New()
	tests := []struct {
		name       string
		repository fakeMemberships
		userID     uuid.UUID
		expected   error
	}{
		{name: "zero user", userID: uuid.Nil, expected: domain.ErrInvalidScope},
		{name: "repository unavailable", repository: fakeMemberships{err: errors.New("database unavailable")}, userID: userID, expected: domain.ErrTenancyUnavailable},
		{name: "invalid repository result", repository: fakeMemberships{memberships: []domain.Membership{{DisplayName: "missing tenant"}}}, userID: userID, expected: domain.ErrTenancyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.repository)
			if _, err := service.ListActive(context.Background(), test.userID); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
		})
	}
}

func TestVerifyActiveFailsClosed(t *testing.T) {
	userID := uuid.New()
	tenantID := uuid.New()
	tests := []struct {
		name       string
		repository fakeMemberships
		userID     uuid.UUID
		tenantID   uuid.UUID
		expected   error
	}{
		{name: "zero user", userID: uuid.Nil, tenantID: tenantID, expected: domain.ErrInvalidScope},
		{name: "zero tenant", userID: userID, tenantID: uuid.Nil, expected: domain.ErrInvalidScope},
		{name: "membership denied", repository: fakeMemberships{err: domain.ErrTenantAccessDenied}, userID: userID, tenantID: tenantID, expected: domain.ErrTenantAccessDenied},
		{name: "repository unavailable", repository: fakeMemberships{err: errors.New("database unavailable")}, userID: userID, tenantID: tenantID, expected: domain.ErrTenancyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.repository)
			if _, err := service.VerifyActive(context.Background(), test.userID, test.tenantID); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
		})
	}
}
