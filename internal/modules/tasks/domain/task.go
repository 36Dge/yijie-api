package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalid  = errors.New("task is invalid")
	ErrNotFound = errors.New("task was not found")
)

type Task struct {
	ID           string
	TenantID     string
	TaskType     string
	Title        string
	Status       string
	Input        []byte
	Result       []byte
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateTask struct {
	TenantID  string
	TaskType  string
	Title     string
	Input     []byte
	RequestID string
	TraceID   string
}
