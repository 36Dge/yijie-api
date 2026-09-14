package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
)

type Service struct {
	repo   domain.Repository
	engine domain.Engine
	epoch  string
}

func New(repo domain.Repository, engine domain.Engine, epoch string) *Service {
	return &Service{repo: repo, engine: engine, epoch: epoch}
}

func (s *Service) Execute(ctx context.Context, request domain.Request) (domain.Response, error) {
	if s.repo == nil || s.engine == nil {
		return domain.Response{}, domain.Fail("service_unavailable")
	}
	if request.Action == "status" {
		ready := s.repo.Ping(ctx) == nil
		if _, err := s.repo.Principal(ctx); err != nil {
			ready = false
		}
		upstream, err := s.engine.Execute(ctx, request)
		ready = ready && err == nil && upstream.Status != nil && upstream.Status.Ready && upstream.Status.Epoch == s.epoch
		return domain.Response{Status: &domain.Status{Ready: ready, Epoch: s.epoch}}, nil
	}
	if request.Action == "principal" {
		response, err := s.engine.Execute(ctx, request)
		if err != nil {
			return domain.Response{}, err
		}
		p := response.Principal
		if p == nil || p.OwnerID != domain.OwnerID || p.TenantID != domain.TenantID || p.UserID == "" || p.SpaceID == "" {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
		if err := s.repo.BindPrincipal(ctx, *p); err != nil {
			return domain.Response{}, err
		}
		return response, nil
	}
	if _, err := s.repo.Principal(ctx); err != nil {
		return domain.Response{}, domain.Fail("service_unavailable")
	}
	if request.Action == "operation" {
		receipt, err := s.reconcile(ctx, request.OperationID)
		return domain.Response{Receipt: &receipt}, err
	}
	if request.WorkflowID != "" {
		ok, err := s.repo.HasResource(ctx, request.WorkflowID)
		if err != nil {
			return domain.Response{}, err
		}
		if !ok {
			return domain.Response{}, domain.Fail("resource_not_found")
		}
	}
	if request.Action == "delete" {
		return s.delete(ctx, request)
	}
	if isWrite(request.Action) {
		return s.write(ctx, request)
	}
	response, err := s.engine.Execute(ctx, request)
	if err != nil {
		return domain.Response{}, err
	}
	if err := checkResponse(request, response); err != nil {
		return domain.Response{}, err
	}
	if response.Principal != nil {
		bound, err := s.repo.Principal(ctx)
		if err != nil || bound != *response.Principal {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
	}
	if request.Action == "list" {
		if err := s.repo.BindResources(ctx, response.Workflows); err != nil {
			return domain.Response{}, err
		}
	}
	if err := s.repo.Audit(ctx, request.Action, request.WorkflowID, "success"); err != nil {
		return domain.Response{}, err
	}
	return response, nil
}

func isWrite(action string) bool {
	return action == "create" || action == "save" || action == "test" || action == "publish" || action == "run"
}

func (s *Service) write(ctx context.Context, request domain.Request) (domain.Response, error) {
	if request.Action == "save" {
		if err := domain.ValidateCanvasEnvelope(request.Canvas); err != nil {
			return domain.Response{}, err
		}
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return domain.Response{}, domain.Fail("invalid_request")
	}
	digest := sha256.Sum256(canonical)
	receipt, fresh, err := s.repo.Claim(ctx, domain.Intent{ID: request.OperationID, Kind: request.Action, WorkflowID: request.WorkflowID, Hash: hex.EncodeToString(digest[:])})
	if err != nil {
		return domain.Response{}, err
	}
	if !fresh {
		return s.replay(ctx, request, receipt)
	}
	response, err := s.engine.Execute(ctx, request)
	if err == nil {
		err = checkResponse(request, response)
	}
	if err != nil {
		code := failureCode(err)
		receipt.Phase = "rejected"
		if code == "service_unavailable" || code == "protocol_mismatch" || code == "internal_error" || code == "storage_unavailable" || code == "operation_unknown" {
			receipt.Phase = "unknown"
			code = "operation_unknown"
		}
		receipt.ErrorCode = code
		if saveErr := s.repo.Finish(ctx, receipt); saveErr != nil {
			return domain.Response{}, &domain.Failure{Code: "operation_unknown", OperationID: request.OperationID}
		}
		return domain.Response{}, &domain.Failure{Code: code, OperationID: request.OperationID}
	}
	receipt.Phase = "completed"
	if response.Workflow != nil {
		receipt.WorkflowID = response.Workflow.ID
		receipt.Revision = response.Workflow.Revision
		// Workflow.published_version is current resource metadata. Only publish
		// registers that version as this operation's result; saving a newer draft
		// must not associate its revision with an older published version.
		if request.Action == "publish" {
			receipt.Version = response.Workflow.PublishedVersion
		}
	}
	if response.Run != nil {
		receipt.WorkflowID = response.Run.WorkflowID
		receipt.Revision = response.Run.Revision
		receipt.Version = response.Run.Version
		receipt.RunID = response.Run.ID
	}
	if err := s.repo.Finish(ctx, receipt); err != nil {
		return domain.Response{}, &domain.Failure{Code: "operation_unknown", OperationID: request.OperationID}
	}
	return response, nil
}

func (s *Service) replay(ctx context.Context, request domain.Request, receipt domain.Receipt) (domain.Response, error) {
	if receipt.Phase == "recorded" || receipt.Phase == "unknown" {
		var err error
		receipt, err = s.reconcile(ctx, request.OperationID)
		if err != nil {
			return domain.Response{}, err
		}
	}
	if receipt.Phase == "rejected" {
		return domain.Response{}, &domain.Failure{Code: receipt.ErrorCode, OperationID: receipt.OperationID}
	}
	if receipt.Phase != "completed" {
		return domain.Response{}, &domain.Failure{Code: "operation_unknown", OperationID: receipt.OperationID}
	}
	query := domain.Request{Action: "read", WorkflowID: receipt.WorkflowID}
	if receipt.RunID != "" {
		query.Action = "read_run"
		query.RunID = receipt.RunID
	}
	result, err := s.engine.Execute(ctx, query)
	if err != nil {
		return domain.Response{}, err
	}
	if err := checkResponse(query, result); err != nil {
		return domain.Response{}, err
	}
	// Never return a later draft as the result of an earlier save/create intent.
	if result.Workflow != nil && (result.Workflow.Revision != receipt.Revision || (receipt.Kind == "publish" && result.Workflow.PublishedVersion != receipt.Version)) {
		return domain.Response{}, &domain.Failure{Code: "operation_unknown", OperationID: receipt.OperationID}
	}
	return result, nil
}

func (s *Service) reconcile(ctx context.Context, id string) (domain.Receipt, error) {
	local, err := s.repo.ReadReceipt(ctx, id)
	if err != nil {
		return domain.Receipt{}, err
	}
	if local.Phase == "completed" || local.Phase == "rejected" {
		return local, nil
	}
	result, err := s.engine.Execute(ctx, domain.Request{Action: "operation", OperationID: id})
	if err != nil || result.Receipt == nil {
		local.Phase = "unknown"
		local.ErrorCode = "operation_unknown"
		return local, nil
	}
	r := *result.Receipt
	if r.OperationID != id || r.Kind != local.Kind || (local.WorkflowID != "" && r.WorkflowID != "" && r.WorkflowID != local.WorkflowID) {
		return domain.Receipt{}, domain.Fail("protocol_mismatch")
	}
	if err := s.repo.Finish(ctx, r); err != nil {
		return domain.Receipt{}, err
	}
	return r, nil
}

func checkResponse(request domain.Request, response domain.Response) error {
	switch request.Action {
	case "create", "save", "publish", "read":
		if response.Workflow == nil {
			return domain.Fail("protocol_mismatch")
		}
	case "bootstrap":
		if response.Workflow == nil || response.Principal == nil {
			return domain.Fail("protocol_mismatch")
		}
	case "run", "test", "read_run":
		if response.Run == nil {
			return domain.Fail("protocol_mismatch")
		}
	}
	if response.Workflow != nil && request.WorkflowID != "" && response.Workflow.ID != request.WorkflowID {
		return domain.Fail("protocol_mismatch")
	}
	if response.Run != nil {
		r := response.Run
		if r.WorkflowID != request.WorkflowID || (request.RunID != "" && r.ID != request.RunID) || (request.OperationID != "" && r.OperationID != request.OperationID) {
			return domain.Fail("protocol_mismatch")
		}
		if request.Action == "run" && (r.Mode != "release" || r.Version != request.Version) {
			return domain.Fail("protocol_mismatch")
		}
		if request.Action == "test" && (r.Mode != "debug" || r.Revision != request.Revision) {
			return domain.Fail("protocol_mismatch")
		}
	}
	for _, r := range response.Runs {
		if r.WorkflowID != request.WorkflowID {
			return domain.Fail("protocol_mismatch")
		}
	}
	return nil
}
func failureCode(err error) string {
	var f *domain.Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return "internal_error"
}

// Deletion is state-idempotent by resource/revision; no new legacy receipt kind.
func (s *Service) delete(ctx context.Context, request domain.Request) (domain.Response, error) {
	if err := s.repo.Audit(ctx, "delete", request.WorkflowID, "requested"); err != nil {
		return domain.Response{}, err
	}
	response, err := s.engine.Execute(ctx, request)
	if err == nil && (response.Deletion == nil || !response.Deletion.Deleted || response.Deletion.WorkflowID != request.WorkflowID) {
		err = domain.Fail("protocol_mismatch")
	}
	if err != nil {
		code := failureCode(err)
		if auditErr := s.repo.Audit(ctx, "delete", request.WorkflowID, code); auditErr != nil {
			return domain.Response{}, domain.Fail("operation_unknown")
		}
		if code == "service_unavailable" || code == "protocol_mismatch" || code == "storage_unavailable" || code == "internal_error" {
			return domain.Response{}, domain.Fail("operation_unknown")
		}
		return domain.Response{}, err
	}
	if err = s.repo.Audit(ctx, "delete", request.WorkflowID, "success"); err != nil {
		return domain.Response{}, domain.Fail("operation_unknown")
	}
	return response, nil
}
