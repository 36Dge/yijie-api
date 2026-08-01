package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/identity/domain"
	"github.com/google/uuid"
)

type fakeUsers struct {
	user domain.User
	err  error
}

func (repository fakeUsers) FindByExternalIdentity(context.Context, string, string) (domain.User, error) {
	return repository.user, repository.err
}

func TestResolvePrincipalUsesOnlyIssuerAndSubjectMapping(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	service := New(fakeUsers{user: domain.User{ID: userID, Status: domain.UserActive}})
	principal, err := service.ResolvePrincipal(context.Background(), "https://identity.test.example/", "synthetic-subject")
	if err != nil {
		t.Fatalf("resolve principal: %v", err)
	}
	if principal.UserID != userID {
		t.Fatalf("unexpected principal: %#v", principal)
	}
}

func TestResolvePrincipalFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		repository fakeUsers
		issuer     string
		subject    string
		expected   error
	}{
		{name: "missing issuer", issuer: "", subject: "subject", expected: domain.ErrInvalidIdentity},
		{name: "missing subject", issuer: "https://identity.test.example/", subject: "", expected: domain.ErrInvalidIdentity},
		{name: "not mapped", repository: fakeUsers{err: domain.ErrIdentityNotFound}, issuer: "https://identity.test.example/", subject: "subject", expected: domain.ErrIdentityNotFound},
		{name: "suspended", repository: fakeUsers{user: domain.User{ID: uuid.New(), Status: domain.UserSuspended}}, issuer: "https://identity.test.example/", subject: "subject", expected: domain.ErrUserSuspended},
		{name: "repository unavailable", repository: fakeUsers{err: errors.New("database unavailable")}, issuer: "https://identity.test.example/", subject: "subject", expected: domain.ErrIdentityUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.repository)
			if _, err := service.ResolvePrincipal(context.Background(), test.issuer, test.subject); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
		})
	}
}
