package taskhttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
)

type fakeRepository struct {
	command domain.CreateTask
}

func (repository *fakeRepository) Create(_ context.Context, command domain.CreateTask) (domain.Task, error) {
	repository.command = command
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	return domain.Task{
		ID:        "15623625-e8b1-4f0d-b2e5-a16db0ce2751",
		TenantID:  command.TenantID,
		TaskType:  command.TaskType,
		Title:     command.Title,
		Status:    "draft",
		Input:     command.Input,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (*fakeRepository) Get(context.Context, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrNotFound
}

func TestCreateReturnsPersistedTask(t *testing.T) {
	repository := &fakeRepository{}
	handler := New(application.New(repository))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		bytes.NewBufferString(`{
			"tenant_id":"9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			"task_type":"listing_diagnosis",
			"title":"Product review",
			"input":{"url":"https://example.com/product"}
		}`),
	)
	request.Header.Set("X-Request-ID", "request-1")
	response := httptest.NewRecorder()

	handler.Create(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", response.Code, response.Body.String())
	}
	if repository.command.RequestID != "request-1" {
		t.Fatalf("request ID was not propagated: %#v", repository.command)
	}
}

func TestCreateRejectsUnknownFields(t *testing.T) {
	handler := New(application.New(&fakeRepository{}))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		bytes.NewBufferString(`{
			"tenant_id":"9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			"task_type":"listing_diagnosis",
			"title":"Product review",
			"input":{},
			"unexpected":true
		}`),
	)
	response := httptest.NewRecorder()

	handler.Create(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", response.Code)
	}
}
