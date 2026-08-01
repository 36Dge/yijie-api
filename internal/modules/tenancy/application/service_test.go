package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

type fakeMemberships struct {
	scope domain.Scope
	err   error
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
