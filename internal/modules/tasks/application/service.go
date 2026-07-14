package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/google/uuid"
)

type Repository interface {
	Create(context.Context, domain.CreateTask) (domain.Task, error)
	Get(context.Context, string) (domain.Task, error)
}

type Service struct {
	repository Repository
}

func New(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Create(ctx context.Context, command domain.CreateTask) (domain.Task, error) {
	command.TaskType = strings.TrimSpace(command.TaskType)
	command.Title = strings.TrimSpace(command.Title)
	tenantID, err := uuid.Parse(command.TenantID)
	if err != nil || tenantID == uuid.Nil {
		return domain.Task{}, fmt.Errorf("%w: tenant_id must be a UUID", domain.ErrInvalid)
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
	return s.repository.Create(ctx, command)
}

func (s *Service) Get(ctx context.Context, id string) (domain.Task, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Task{}, fmt.Errorf("%w: task_id must be a UUID", domain.ErrInvalid)
	}
	return s.repository.Get(ctx, id)
}
