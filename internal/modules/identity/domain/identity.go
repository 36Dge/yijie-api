package domain

import (
	"errors"

	"github.com/google/uuid"
)

var (
	ErrInvalidIdentity     = errors.New("identity input is invalid")
	ErrIdentityNotFound    = errors.New("identity is not mapped")
	ErrUserSuspended       = errors.New("user access is denied")
	ErrIdentityUnavailable = errors.New("identity repository is unavailable")
)

type UserStatus string

const (
	UserActive    UserStatus = "active"
	UserSuspended UserStatus = "suspended"
)

type User struct {
	ID     uuid.UUID
	Status UserStatus
}

type Principal struct {
	UserID  uuid.UUID
	Issuer  string
	Subject string
}
