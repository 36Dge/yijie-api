package domain

import (
	"errors"

	"github.com/google/uuid"
)

const MaxAuthorizationRevision int64 = 9007199254740991

var (
	ErrInvalidScope       = errors.New("tenant scope is invalid")
	ErrTenantAccessDenied = errors.New("tenant access is denied")
	ErrTenancyUnavailable = errors.New("tenancy repository is unavailable")
)

type Scope struct {
	UserID                uuid.UUID
	TenantID              uuid.UUID
	AuthorizationRevision int64
}

type Membership struct {
	TenantID    uuid.UUID
	DisplayName string
}

func (membership Membership) Valid() bool {
	nameLength := len([]rune(membership.DisplayName))
	return membership.TenantID != uuid.Nil && nameLength >= 1 && nameLength <= 200
}

func (scope Scope) Valid() bool {
	return scope.UserID != uuid.Nil &&
		scope.TenantID != uuid.Nil &&
		scope.AuthorizationRevision >= 1 &&
		scope.AuthorizationRevision <= MaxAuthorizationRevision
}
