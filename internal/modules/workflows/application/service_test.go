package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
)

type memoryRepository struct {
	principal domain.Principal
	resources map[string]bool
	intents   map[string]domain.Intent
	receipts  map[string]domain.Receipt
	audits    int
}

func newMemory() *memoryRepository {
	return &memoryRepository{principal: domain.Principal{OwnerID: domain.OwnerID, TenantID: domain.TenantID, UserID: "15301", SpaceID: "15302"}, resources: map[string]bool{}, intents: map[string]domain.Intent{}, receipts: map[string]domain.Receipt{}}
}
func (r *memoryRepository) Ping(context.Context) error { return nil }
func (r *memoryRepository) Principal(context.Context) (domain.Principal, error) {
	return r.principal, nil
}
func (r *memoryRepository) BindPrincipal(_ context.Context, p domain.Principal) error {
	r.principal = p
	return nil
}
func (r *memoryRepository) BindResources(_ context.Context, ws []domain.Workflow) error {
	for _, w := range ws {
		r.resources[w.ID] = true
	}
	return nil
}
func (r *memoryRepository) HasResource(_ context.Context, id string) (bool, error) {
	return r.resources[id], nil
}
func (r *memoryRepository) Claim(_ context.Context, in domain.Intent) (domain.Receipt, bool, error) {
	if old, ok := r.intents[in.ID]; ok {
		if old.Hash != in.Hash || old.Kind != in.Kind || old.WorkflowID != in.WorkflowID {
			return domain.Receipt{}, false, domain.Fail("operation_conflict")
		}
		return r.receipts[in.ID], false, nil
	}
	r.intents[in.ID] = in
	v := domain.Receipt{OperationID: in.ID, WorkflowID: in.WorkflowID, Kind: in.Kind, Phase: "recorded"}
	r.receipts[in.ID] = v
	return v, true, nil
}
func (r *memoryRepository) ReadReceipt(_ context.Context, id string) (domain.Receipt, error) {
	v, ok := r.receipts[id]
	if !ok {
		return v, domain.Fail("resource_not_found")
	}
	return v, nil
}
func (r *memoryRepository) Finish(_ context.Context, v domain.Receipt) error {
	r.receipts[v.OperationID] = v
	if v.WorkflowID != "" {
		r.resources[v.WorkflowID] = true
	}
	r.audits++
	return nil
}
func (r *memoryRepository) Audit(context.Context, string, string, string) error {
	r.audits++
	return nil
}

type normalEngine struct {
	writes   int
	workflow domain.Workflow
	receipt  domain.Receipt
}

func (e *normalEngine) Execute(_ context.Context, r domain.Request) (domain.Response, error) {
	switch r.Action {
	case "create", "save", "publish":
		e.writes++
		return domain.Response{Workflow: &e.workflow}, nil
	case "read":
		return domain.Response{Workflow: &e.workflow}, nil
	case "operation":
		return domain.Response{Receipt: &e.receipt}, nil
	case "run":
		e.writes++
		return domain.Response{Run: &domain.Run{ID: "run1", WorkflowID: r.WorkflowID, OperationID: r.OperationID, Mode: "release", Version: r.Version, State: "unknown", Terminal: false, StartedAtMS: 1}}, nil
	default:
		return domain.Response{}, domain.Fail("invalid_request")
	}
}

const opID = "15300000-0000-4000-8000-000000000002"

func TestCreateIntentReplayAndChangedContentConflict(t *testing.T) {
	repo := newMemory()
	engine := &normalEngine{workflow: domain.Workflow{ID: "123", Name: "Synthetic", Revision: "r1"}}
	service := New(repo, engine, "epoch")
	r := domain.Request{Action: "create", OperationID: opID, Name: "Synthetic"}
	for i := 0; i < 2; i++ {
		result, err := service.Execute(context.Background(), r)
		if err != nil || result.Workflow == nil || result.Workflow.ID != "123" {
			t.Fatalf("normal create/replay: %v", err)
		}
	}
	if engine.writes != 1 || repo.receipts[opID].Phase != "completed" || !repo.resources["123"] {
		t.Fatal("intent replay dispatched twice or lost mapping")
	}
	r.Name = "Changed"
	_, err := service.Execute(context.Background(), r)
	var f *domain.Failure
	if !errors.As(err, &f) || f.Code != "operation_conflict" || engine.writes != 1 {
		t.Fatal("same operation with different content must conflict")
	}
}
func TestRecordedOperationQueriesThenReconcilesWithoutRedispatch(t *testing.T) {
	repo := newMemory()
	r := domain.Request{Action: "create", OperationID: opID, Name: "Synthetic"}
	body, _ := json.Marshal(r)
	hash := sha256.Sum256(body)
	_, _, _ = repo.Claim(context.Background(), domain.Intent{ID: opID, Kind: "create", Hash: hex.EncodeToString(hash[:])})
	engine := &normalEngine{workflow: domain.Workflow{ID: "123", Revision: "r1"}, receipt: domain.Receipt{OperationID: opID, Kind: "create", Phase: "recorded"}}
	service := New(repo, engine, "epoch")
	result, err := service.Execute(context.Background(), domain.Request{Action: "operation", OperationID: opID})
	if err != nil || result.Receipt.Phase != "recorded" || engine.writes != 0 {
		t.Fatal("recorded receipt became a fabricated execution")
	}
	engine.receipt.Phase = "completed"
	engine.receipt.WorkflowID = "123"
	engine.receipt.Revision = "r1"
	result, err = service.Execute(context.Background(), domain.Request{Action: "operation", OperationID: opID})
	if err != nil || result.Receipt.Phase != "completed" || !repo.resources["123"] {
		t.Fatal("registered resource was not reconciled")
	}
	if _, err = service.Execute(context.Background(), r); err != nil || engine.writes != 0 {
		t.Fatal("completed registration was resubmitted")
	}
}
func TestRegisteredRunPreservesEngineUnknownAndReceiptMeaning(t *testing.T) {
	repo := newMemory()
	repo.resources["123"] = true
	engine := &normalEngine{}
	service := New(repo, engine, "epoch")
	result, err := service.Execute(context.Background(), domain.Request{Action: "run", WorkflowID: "123", Version: "v0.0.1", OperationID: opID, Input: "text"})
	if err != nil || result.Run == nil || result.Run.State != "unknown" || result.Run.Terminal {
		t.Fatal("unknown engine fact changed")
	}
	if repo.receipts[opID].Phase != "completed" || repo.receipts[opID].RunID != "run1" || repo.receipts[opID].Version != "v0.0.1" {
		t.Fatal("execution registration receipt missing")
	}
}

func TestSaveReceiptKeepsOperationResultSeparateFromPublishedMetadata(t *testing.T) {
	ctx := context.Background()
	canvas := `{"nodes":[{"id":"100001","type":"1","data":{"nodeMeta":{"title":"开始"},"outputs":[{"name":"input","type":"string","required":true}]}},{"id":"900001","type":"2","data":{"nodeMeta":{"title":"结束"}}}],"edges":[]}`
	request := domain.Request{Action: "save", WorkflowID: "123", OperationID: opID, Revision: "r1", Name: "New draft", Canvas: canvas}
	expected := domain.Receipt{OperationID: opID, Kind: "save", Phase: "completed", WorkflowID: "123", Revision: "r2"}
	engine := &normalEngine{workflow: domain.Workflow{ID: "123", Name: request.Name, Revision: "r2", Canvas: canvas, PublishedVersion: "v0.0.1"}, receipt: expected}
	repo := newMemory()
	repo.resources["123"] = true
	service := New(repo, engine, "epoch")
	response, err := service.Execute(ctx, request)
	if err != nil || response.Workflow == nil || response.Workflow.PublishedVersion != "v0.0.1" {
		t.Fatal("saving a draft must preserve the separate current published-version metadata")
	}
	readback, err := service.Execute(ctx, domain.Request{Action: "operation", OperationID: opID})
	if err != nil || readback.Receipt == nil || *readback.Receipt != expected {
		t.Fatal("save receipt must contain its new draft revision without borrowing an old published version")
	}
	// Independently exercise the normal recorded-intent query path. No transport
	// failure is injected and no execution success is fabricated by this unit port.
	queryRepo := newMemory()
	queryRepo.resources["123"] = true
	body, _ := json.Marshal(request)
	hash := sha256.Sum256(body)
	_, _, err = queryRepo.Claim(ctx, domain.Intent{ID: opID, Kind: "save", WorkflowID: "123", Hash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal("normal intent registration failed")
	}
	queried, err := New(queryRepo, engine, "epoch").Execute(ctx, domain.Request{Action: "operation", OperationID: opID})
	if err != nil || queried.Receipt == nil || *queried.Receipt != *readback.Receipt || engine.writes != 1 {
		t.Fatal("direct save and provider receipt query must preserve the same operation result")
	}
	engine.workflow.PublishedVersion = "v0.0.2"
	publishID := "15300000-0000-4000-8000-000000000003"
	_, err = service.Execute(ctx, domain.Request{Action: "publish", WorkflowID: "123", OperationID: publishID, Revision: "r2", TestRunID: "debug-r2"})
	if err != nil || repo.receipts[publishID].Version != "v0.0.2" || repo.receipts[publishID].Revision != "r2" {
		t.Fatal("publish receipt must retain the version actually registered by its operation")
	}
}

func TestNormalMissingResourceDoesNotReachEngine(t *testing.T) {
	repo := newMemory()
	engine := &normalEngine{}
	service := New(repo, engine, "epoch")
	_, err := service.Execute(context.Background(), domain.Request{Action: "read", WorkflowID: "missing"})
	var f *domain.Failure
	if !errors.As(err, &f) || f.Code != "resource_not_found" {
		t.Fatal("missing resource must remain not found")
	}
}
