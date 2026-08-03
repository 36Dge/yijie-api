package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/google/uuid"
)

type SecureRepository interface {
	CreateSecure(context.Context, domain.CreateSecureTask) (domain.Task, error)
	GetSecure(context.Context, domain.SecureScope, string) (domain.Task, error)
	RecordSecureAudit(context.Context, domain.SecureTaskAudit) error
}

type SecureService struct {
	repository SecureRepository
}

func NewSecure(repository SecureRepository) *SecureService {
	return &SecureService{repository: repository}
}

func (service *SecureService) Create(ctx context.Context, command domain.CreateSecureTask) (domain.Task, error) {
	if service == nil || service.repository == nil {
		return domain.Task{}, domain.ErrUnavailable
	}
	command.TaskType = strings.TrimSpace(command.TaskType)
	if err := validateSecureScope(command.SecureScope); err != nil {
		return domain.Task{}, err
	}
	if id, err := uuid.Parse(command.IdempotencyKey); err != nil || id == uuid.Nil {
		return domain.Task{}, fmt.Errorf("%w: idempotency key must be a UUID", domain.ErrInvalid)
	}
	if command.TaskType != "conversation" {
		return domain.Task{}, fmt.Errorf("%w: task_type must be conversation", domain.ErrInvalid)
	}
	if command.RequestID == "" || utf8.RuneCountInString(command.RequestID) > 200 {
		return domain.Task{}, fmt.Errorf("%w: request_id must contain 1 to 200 characters", domain.ErrInvalid)
	}
	if utf8.RuneCountInString(command.TraceID) > 200 {
		return domain.Task{}, fmt.Errorf("%w: trace_id must contain at most 200 characters", domain.ErrInvalid)
	}
	var input struct {
		SchemaVersion     int    `json:"schema_version"`
		ContentMode       string `json:"content_mode"`
		ClientReferenceID string `json:"client_reference_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(command.Input))
	decoder.DisallowUnknownFields()
	if len(command.Input) == 0 || decoder.Decode(&input) != nil {
		return domain.Task{}, fmt.Errorf("%w: input must be a content-free reference", domain.ErrInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.Task{}, fmt.Errorf("%w: input must contain one content-free reference", domain.ErrInvalid)
	}
	clientReferenceID, err := uuid.Parse(input.ClientReferenceID)
	if input.SchemaVersion != 1 || input.ContentMode != "local_only" || err != nil || clientReferenceID == uuid.Nil {
		return domain.Task{}, fmt.Errorf("%w: input must be a content-free reference", domain.ErrInvalid)
	}
	canonical, err := json.Marshal(struct {
		TaskType string `json:"task_type"`
		Input    any    `json:"input"`
	}{TaskType: command.TaskType, Input: input})
	if err != nil {
		return domain.Task{}, fmt.Errorf("%w: input cannot be canonicalized", domain.ErrInvalid)
	}
	digest := sha256.Sum256(canonical)
	command.CanonicalSHA256 = hex.EncodeToString(digest[:])
	return service.repository.CreateSecure(ctx, command)
}

func (service *SecureService) RecordAudit(ctx context.Context, audit domain.SecureTaskAudit) error {
	if service == nil || service.repository == nil {
		return domain.ErrUnavailable
	}
	return service.repository.RecordSecureAudit(ctx, audit)
}

func (service *SecureService) Get(ctx context.Context, scope domain.SecureScope, id string) (domain.Task, error) {
	if service == nil || service.repository == nil {
		return domain.Task{}, domain.ErrUnavailable
	}
	if err := validateSecureScope(scope); err != nil {
		return domain.Task{}, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return domain.Task{}, fmt.Errorf("%w: task_id must be a UUID", domain.ErrInvalid)
	}
	return service.repository.GetSecure(ctx, scope, parsed.String())
}

func validateSecureScope(scope domain.SecureScope) error {
	for name, value := range map[string]string{"tenant_id": scope.TenantID, "user_id": scope.UserID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil {
			return fmt.Errorf("%w: %s must be a UUID", domain.ErrInvalid, name)
		}
	}
	return nil
}
