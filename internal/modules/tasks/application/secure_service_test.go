package application

import (
	"context"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
)

type fakeSecureRepository struct {
	created domain.CreateSecureTask
}

func (repository *fakeSecureRepository) CreateSecure(_ context.Context, command domain.CreateSecureTask) (domain.Task, error) {
	repository.created = command
	return domain.Task{ID: "15623625-e8b1-4f0d-b2e5-a16db0ce2751"}, nil
}

func (*fakeSecureRepository) GetSecure(context.Context, domain.SecureScope, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrNotFound
}

func TestSecureCreateDerivesStableCanonicalDigest(t *testing.T) {
	repository := &fakeSecureRepository{}
	service := NewSecure(repository)
	command := domain.CreateSecureTask{
		SecureScope: domain.SecureScope{
			TenantID: "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			UserID:   "4b4986b0-58cc-4dc8-8a9c-a83715951b41",
		},
		TaskType: " conversation ", Title: " Local task ",
		Input:          []byte(`{"z":1,"a":"synthetic"}`),
		IdempotencyKey: "ad17b95d-a91d-4ef8-9736-8bc36cefcbb6",
		RequestID:      "request-1",
	}
	if _, err := service.Create(context.Background(), command); err != nil {
		t.Fatalf("secure create: %v", err)
	}
	firstDigest := repository.created.CanonicalSHA256
	if len(firstDigest) != 64 || repository.created.TaskType != "conversation" || repository.created.Title != "Local task" {
		t.Fatalf("unexpected normalized command: %#v", repository.created)
	}
	command.Input = []byte(`{"a":"synthetic","z":1}`)
	if _, err := service.Create(context.Background(), command); err != nil {
		t.Fatalf("secure create with reordered object: %v", err)
	}
	if repository.created.CanonicalSHA256 != firstDigest {
		t.Fatalf("canonical digest changed across object key order: %s != %s", repository.created.CanonicalSHA256, firstDigest)
	}
}

func TestSecureCreateRejectsClientAuthorityFields(t *testing.T) {
	service := NewSecure(&fakeSecureRepository{})
	base := domain.CreateSecureTask{
		SecureScope: domain.SecureScope{
			TenantID: "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			UserID:   "4b4986b0-58cc-4dc8-8a9c-a83715951b41",
		},
		TaskType: "conversation", Title: "Local task",
		IdempotencyKey: "ad17b95d-a91d-4ef8-9736-8bc36cefcbb6",
		RequestID:      "request-1",
	}
	for _, input := range []string{
		`{"tenant_id":"9d1e1888-7ecf-42f6-8cbf-4d24101a801c"}`,
		`{"nested":{"createdByUserId":"4b4986b0-58cc-4dc8-8a9c-a83715951b41"}}`,
		`{"authorization":"allow"}`,
	} {
		command := base
		command.Input = []byte(input)
		if _, err := service.Create(context.Background(), command); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("expected authority field %s to fail, got %v", input, err)
		}
	}
}
