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

func (*fakeSecureRepository) RecordSecureAudit(context.Context, domain.SecureTaskAudit) error {
	return nil
}

func TestSecureCreateDerivesStableCanonicalDigest(t *testing.T) {
	repository := &fakeSecureRepository{}
	service := NewSecure(repository)
	command := domain.CreateSecureTask{
		SecureScope: domain.SecureScope{
			TenantID: "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			UserID:   "4b4986b0-58cc-4dc8-8a9c-a83715951b41",
		},
		TaskType:       " conversation ",
		Input:          []byte(`{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}`),
		IdempotencyKey: "ad17b95d-a91d-4ef8-9736-8bc36cefcbb6",
		RequestID:      "request-1",
	}
	if _, err := service.Create(context.Background(), command); err != nil {
		t.Fatalf("secure create: %v", err)
	}
	firstDigest := repository.created.CanonicalSHA256
	if len(firstDigest) != 64 || repository.created.TaskType != "conversation" {
		t.Fatalf("unexpected normalized command: %#v", repository.created)
	}
	command.Input = []byte(`{"client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37","content_mode":"local_only","schema_version":1}`)
	if _, err := service.Create(context.Background(), command); err != nil {
		t.Fatalf("secure create with reordered object: %v", err)
	}
	if repository.created.CanonicalSHA256 != firstDigest {
		t.Fatalf("canonical digest changed across object key order: %s != %s", repository.created.CanonicalSHA256, firstDigest)
	}
}

func TestSecureCreateRejectsNonContentFreeInput(t *testing.T) {
	service := NewSecure(&fakeSecureRepository{})
	base := domain.CreateSecureTask{
		SecureScope: domain.SecureScope{
			TenantID: "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
			UserID:   "4b4986b0-58cc-4dc8-8a9c-a83715951b41",
		},
		TaskType:       "conversation",
		IdempotencyKey: "ad17b95d-a91d-4ef8-9736-8bc36cefcbb6",
		RequestID:      "request-1",
	}
	for _, input := range []string{
		`{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37","prompt":"secret"}`,
		`{"schema_version":2,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}`,
		`{"schema_version":1,"content_mode":"cloud","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}`,
		`{"schema_version":1,"content_mode":"local_only","client_reference_id":"not-a-uuid"}`,
	} {
		command := base
		command.Input = []byte(input)
		if _, err := service.Create(context.Background(), command); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("expected authority field %s to fail, got %v", input, err)
		}
	}
}
