package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/google/uuid"
)

type SecureRepository interface {
	CreateSecure(context.Context, domain.CreateSecureTask) (domain.Task, error)
	GetSecure(context.Context, domain.SecureScope, string) (domain.Task, error)
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
	command.Title = strings.TrimSpace(command.Title)
	if err := validateSecureScope(command.SecureScope); err != nil {
		return domain.Task{}, err
	}
	if id, err := uuid.Parse(command.IdempotencyKey); err != nil || id == uuid.Nil {
		return domain.Task{}, fmt.Errorf("%w: idempotency key must be a UUID", domain.ErrInvalid)
	}
	if command.TaskType == "" || utf8.RuneCountInString(command.TaskType) > 100 {
		return domain.Task{}, fmt.Errorf("%w: task_type must contain 1 to 100 characters", domain.ErrInvalid)
	}
	if command.Title == "" || utf8.RuneCountInString(command.Title) > 200 {
		return domain.Task{}, fmt.Errorf("%w: title must contain 1 to 200 characters", domain.ErrInvalid)
	}
	if command.RequestID == "" || utf8.RuneCountInString(command.RequestID) > 200 {
		return domain.Task{}, fmt.Errorf("%w: request_id must contain 1 to 200 characters", domain.ErrInvalid)
	}
	if utf8.RuneCountInString(command.TraceID) > 200 {
		return domain.Task{}, fmt.Errorf("%w: trace_id must contain at most 200 characters", domain.ErrInvalid)
	}
	var input map[string]any
	if len(command.Input) == 0 || json.Unmarshal(command.Input, &input) != nil || input == nil {
		return domain.Task{}, fmt.Errorf("%w: input must be a JSON object", domain.ErrInvalid)
	}
	if containsAuthorityField(input) {
		return domain.Task{}, fmt.Errorf("%w: input must not contain identity, tenant, ownership, or authorization fields", domain.ErrInvalid)
	}
	canonical, err := json.Marshal(struct {
		TaskType string         `json:"task_type"`
		Title    string         `json:"title"`
		Input    map[string]any `json:"input"`
	}{TaskType: command.TaskType, Title: command.Title, Input: input})
	if err != nil {
		return domain.Task{}, fmt.Errorf("%w: input cannot be canonicalized", domain.ErrInvalid)
	}
	digest := sha256.Sum256(canonical)
	command.CanonicalSHA256 = hex.EncodeToString(digest[:])
	return service.repository.CreateSecure(ctx, command)
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

func containsAuthorityField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.Map(func(character rune) rune {
				if character >= 'A' && character <= 'Z' {
					return character + ('a' - 'A')
				}
				if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
					return character
				}
				return -1
			}, key)
			switch normalized {
			case "tenantid", "userid", "createdbyuserid", "ownerid", "authorization", "accesstoken":
				return true
			}
			if containsAuthorityField(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsAuthorityField(nested) {
				return true
			}
		}
	}
	return false
}
