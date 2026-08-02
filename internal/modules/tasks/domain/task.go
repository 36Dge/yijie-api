package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalid             = errors.New("task is invalid")
	ErrNotFound            = errors.New("task was not found")
	ErrIdempotencyConflict = errors.New("task idempotency key conflicts with an existing request")
	ErrUnavailable         = errors.New("task repository is unavailable")
)

type Task struct {
	ID              string
	TenantID        string
	CreatedByUserID string
	TaskType        string
	Title           string
	Status          string
	Input           []byte
	Result          []byte
	ErrorMessage    *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type SecureScope struct {
	TenantID string
	UserID   string
}

type CreateSecureTask struct {
	SecureScope
	TaskType        string
	Title           string
	Input           []byte
	IdempotencyKey  string
	CanonicalSHA256 string
	RequestID       string
	TraceID         string
}

type CreateTask struct {
	TenantID  string
	TaskType  string
	Title     string
	Input     []byte
	RequestID string
	TraceID   string
}
