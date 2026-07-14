package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
)

type fakeRepository struct {
	created domain.CreateTask
}

func (repository *fakeRepository) Create(_ context.Context, command domain.CreateTask) (domain.Task, error) {
	repository.created = command
	return domain.Task{ID: "15623625-e8b1-4f0d-b2e5-a16db0ce2751"}, nil
}

func (*fakeRepository) Get(context.Context, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrNotFound
}

func TestCreateValidatesAndNormalizesTask(t *testing.T) {
	repository := &fakeRepository{}
	service := New(repository)
	_, err := service.Create(context.Background(), domain.CreateTask{
		TenantID:  "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
		TaskType:  " listing_diagnosis ",
		Title:     " Product review ",
		Input:     []byte(`{"url":"https://example.com/product"}`),
		RequestID: "request-1",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if repository.created.TaskType != "listing_diagnosis" || repository.created.Title != "Product review" {
		t.Fatalf("task was not normalized: %#v", repository.created)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	service := New(&fakeRepository{})
	_, err := service.Create(context.Background(), domain.CreateTask{
		TenantID:  "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
		TaskType:  "listing_diagnosis",
		Title:     "Product review",
		Input:     []byte(`[]`),
		RequestID: "request-1",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestCreateRejectsMissingTenant(t *testing.T) {
	service := New(&fakeRepository{})
	_, err := service.Create(context.Background(), domain.CreateTask{
		TaskType:  "listing_diagnosis",
		Title:     "Product review",
		Input:     []byte(`{}`),
		RequestID: "request-1",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
