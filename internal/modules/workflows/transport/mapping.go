package transport

import (
	"encoding/json"
	"errors"
	"net/http"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
)

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func Limits() wire.Limits {
	return wire.Limits{InputBytes: 4096, PrefixBytes: 1024, OutputBytes: 5120, CanvasBytes: 262144, MessageBytes: 524288, MaxActiveRuns: 1, ExecutionBudgetSeconds: 30, EditorTtlSeconds: 300}
}
func Principal(p domain.Principal) wire.Principal {
	return wire.Principal{OwnerId: p.OwnerID, TenantId: p.TenantID, UserId: p.UserID, SpaceId: p.SpaceID}
}
func Workflow(w domain.Workflow) wire.Workflow {
	return wire.Workflow{WorkflowId: w.ID, Name: w.Name, Description: w.Description, Revision: w.Revision, Canvas: w.Canvas, Runnable: w.Runnable, UpdatedAtMs: w.UpdatedAtMS, PublishedVersion: ptr(w.PublishedVersion)}
}
func fromWorkflow(w wire.Workflow) domain.Workflow {
	return domain.Workflow{ID: w.WorkflowId, Name: w.Name, Description: w.Description, Revision: w.Revision, Canvas: w.Canvas, Runnable: w.Runnable, UpdatedAtMS: w.UpdatedAtMs, PublishedVersion: value(w.PublishedVersion)}
}
func Receipt(r domain.Receipt) wire.OperationReceipt {
	return wire.OperationReceipt{OperationId: r.OperationID, Kind: wire.OperationKind(r.Kind), Phase: wire.OperationPhase(r.Phase), WorkflowId: ptr(r.WorkflowID), Revision: ptr(r.Revision), Version: ptr(r.Version), RunId: ptr(r.RunID), Error: wireError(r.ErrorCode, r.OperationID)}
}
func fromReceipt(r wire.OperationReceipt) domain.Receipt {
	return domain.Receipt{OperationID: r.OperationId, Kind: string(r.Kind), Phase: string(r.Phase), WorkflowID: value(r.WorkflowId), Revision: value(r.Revision), Version: value(r.Version), RunID: value(r.RunId), ErrorCode: errorCode(r.Error)}
}
func Run(r domain.Run) wire.Run {
	w := wire.Run{RunId: r.ID, WorkflowId: r.WorkflowID, OperationId: r.OperationID, Mode: wire.RunMode(r.Mode), Revision: ptr(r.Revision), Version: ptr(r.Version), State: r.State, Terminal: r.Terminal, Output: r.Output, StartedAtMs: r.StartedAtMS, FinishedAtMs: r.FinishedAtMS, Error: wireError(r.ErrorCode, r.OperationID)}
	if r.Input != nil {
		w.Input = &wire.TextInput{Input: *r.Input}
	}
	if r.Nodes != nil {
		nodes := make([]wire.NodeResult, 0, len(r.Nodes))
		for _, n := range r.Nodes {
			nodes = append(nodes, wire.NodeResult{NodeId: n.ID, State: n.State, Output: n.Output, Error: wireError(n.ErrorCode, "")})
		}
		w.Nodes = &nodes
	}
	return w
}
func fromRun(w wire.Run) domain.Run {
	r := domain.Run{ID: w.RunId, WorkflowID: w.WorkflowId, OperationID: w.OperationId, Mode: string(w.Mode), Revision: value(w.Revision), Version: value(w.Version), State: w.State, Terminal: w.Terminal, Output: w.Output, StartedAtMS: w.StartedAtMs, FinishedAtMS: w.FinishedAtMs, ErrorCode: errorCode(w.Error)}
	if w.Input != nil {
		r.Input = &w.Input.Input
	}
	if w.Nodes != nil {
		r.Nodes = make([]domain.Node, 0, len(*w.Nodes))
		for _, n := range *w.Nodes {
			r.Nodes = append(r.Nodes, domain.Node{ID: n.NodeId, State: n.State, Output: n.Output, ErrorCode: errorCode(n.Error)})
		}
	}
	return r
}
func errorCode(e *wire.ErrorResponse) string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}
func wireError(code, id string) *wire.ErrorResponse {
	if code == "" {
		return nil
	}
	e := Error(&domain.Failure{Code: code, OperationID: id})
	return &e
}
func Error(err error) wire.ErrorResponse {
	code := "internal_error"
	id := ""
	var e *domain.Failure
	if errors.As(err, &e) {
		code = e.Code
		id = e.OperationID
	}
	if !wire.ErrorCode(code).Valid() {
		code = "internal_error"
	}
	return wire.ErrorResponse{Code: wire.ErrorCode(code), Message: code, OperationId: ptr(id)}
}
func ErrorStatus(err error) int {
	switch Error(err).Code {
	case wire.Unauthorized, wire.SessionExpired:
		return http.StatusUnauthorized
	case wire.ResourceNotFound:
		return http.StatusNotFound
	case wire.OperationConflict, wire.RevisionConflict, wire.RunBusy, wire.OperationUnknown:
		return http.StatusConflict
	case wire.InputTooLarge:
		return http.StatusRequestEntityTooLarge
	case wire.ProfileDisabled, wire.ServiceUnavailable, wire.StorageUnavailable, wire.InternalError:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

func EncodeResponse(action string, r domain.Response) (string, any, error) {
	switch action {
	case "delete":
		if r.Deletion != nil {
			return "DeleteResult", wire.DeleteResult{WorkflowId: r.Deletion.WorkflowID, Deleted: wire.DeleteResultDeleted(r.Deletion.Deleted)}, nil
		}
	case "status":
		if r.Status != nil {
			state := wire.Unavailable
			if r.Status.Ready {
				state = wire.Ready
			}
			return "ServiceStatus", wire.ServiceStatus{ProtocolVersion: 1, RunEpoch: r.Status.Epoch, Ready: r.Status.Ready, State: state, Limits: Limits()}, nil
		}
	case "principal":
		if r.Principal != nil {
			return "Principal", Principal(*r.Principal), nil
		}
	case "create", "read", "save", "publish":
		if r.Workflow != nil {
			return "Workflow", Workflow(*r.Workflow), nil
		}
	case "list":
		items := make([]wire.WorkflowSummary, 0, len(r.Workflows))
		for _, w := range r.Workflows {
			items = append(items, wire.WorkflowSummary{WorkflowId: w.ID, Name: w.Name, Description: w.Description, Revision: w.Revision, Runnable: w.Runnable, UpdatedAtMs: w.UpdatedAtMS, PublishedVersion: ptr(w.PublishedVersion)})
		}
		return "WorkflowList", wire.WorkflowList{Items: items, NextCursor: ptr(r.NextCursor)}, nil
	case "run", "test", "read_run":
		if r.Run != nil {
			return "Run", Run(*r.Run), nil
		}
	case "history":
		items := make([]wire.RunSummary, 0, len(r.Runs))
		for _, w := range r.Runs {
			items = append(items, wire.RunSummary{RunId: w.ID, WorkflowId: w.WorkflowID, OperationId: w.OperationID, Mode: wire.RunMode(w.Mode), Revision: ptr(w.Revision), Version: ptr(w.Version), State: w.State, Terminal: w.Terminal, StartedAtMs: w.StartedAtMS, FinishedAtMs: w.FinishedAtMS, Error: wireError(w.ErrorCode, w.OperationID)})
		}
		return "RunList", wire.RunList{Items: items, NextCursor: ptr(r.NextCursor)}, nil
	case "operation":
		if r.Receipt != nil {
			return "OperationReceipt", Receipt(*r.Receipt), nil
		}
	case "bootstrap":
		if r.Workflow != nil && r.Principal != nil {
			return "Bootstrap", wire.Bootstrap{Workflow: Workflow(*r.Workflow), Principal: Principal(*r.Principal), NodeTypes: []wire.BootstrapNodeTypes{1, 15, 2}, Limits: Limits()}, nil
		}
	}
	return "", nil, domain.Fail("protocol_mismatch")
}

func DecodeResponse(action string, data []byte) (domain.Response, error) {
	r := domain.Response{}
	name := map[string]string{"delete": "DeleteResult", "status": "ServiceStatus", "principal": "Principal", "create": "Workflow", "read": "Workflow", "save": "Workflow", "publish": "Workflow", "list": "WorkflowList", "run": "Run", "test": "Run", "read_run": "Run", "history": "RunList", "operation": "OperationReceipt", "bootstrap": "Bootstrap"}[action]
	if err := Validate(name, data); err != nil {
		return r, domain.Fail("protocol_mismatch")
	}
	switch name {
	case "DeleteResult":
		var v wire.DeleteResult
		_ = json.Unmarshal(data, &v)
		r.Deletion = &domain.Deletion{WorkflowID: v.WorkflowId, Deleted: bool(v.Deleted)}
	case "ServiceStatus":
		var w wire.ServiceStatus
		_ = json.Unmarshal(data, &w)
		r.Status = &domain.Status{Ready: w.Ready && w.State == wire.Ready, Epoch: w.RunEpoch}
	case "Principal":
		var w wire.Principal
		_ = json.Unmarshal(data, &w)
		r.Principal = &domain.Principal{OwnerID: w.OwnerId, TenantID: w.TenantId, UserID: w.UserId, SpaceID: w.SpaceId}
	case "Workflow":
		var w wire.Workflow
		_ = json.Unmarshal(data, &w)
		v := fromWorkflow(w)
		r.Workflow = &v
	case "WorkflowList":
		var w wire.WorkflowList
		_ = json.Unmarshal(data, &w)
		r.Workflows = make([]domain.Workflow, 0, len(w.Items))
		for _, i := range w.Items {
			r.Workflows = append(r.Workflows, domain.Workflow{ID: i.WorkflowId, Name: i.Name, Description: i.Description, Revision: i.Revision, Runnable: i.Runnable, UpdatedAtMS: i.UpdatedAtMs, PublishedVersion: value(i.PublishedVersion)})
		}
		r.NextCursor = value(w.NextCursor)
	case "Run":
		var w wire.Run
		_ = json.Unmarshal(data, &w)
		v := fromRun(w)
		r.Run = &v
	case "RunList":
		var w wire.RunList
		_ = json.Unmarshal(data, &w)
		r.Runs = make([]domain.Run, 0, len(w.Items))
		for _, i := range w.Items {
			r.Runs = append(r.Runs, domain.Run{ID: i.RunId, WorkflowID: i.WorkflowId, OperationID: i.OperationId, Mode: string(i.Mode), Revision: value(i.Revision), Version: value(i.Version), State: i.State, Terminal: i.Terminal, StartedAtMS: i.StartedAtMs, FinishedAtMS: i.FinishedAtMs, ErrorCode: errorCode(i.Error)})
		}
		r.NextCursor = value(w.NextCursor)
	case "OperationReceipt":
		var w wire.OperationReceipt
		_ = json.Unmarshal(data, &w)
		v := fromReceipt(w)
		r.Receipt = &v
	case "Bootstrap":
		var w wire.Bootstrap
		_ = json.Unmarshal(data, &w)
		v := fromWorkflow(w.Workflow)
		r.Workflow = &v
		r.Principal = &domain.Principal{OwnerID: w.Principal.OwnerId, TenantID: w.Principal.TenantId, UserID: w.Principal.UserId, SpaceID: w.Principal.SpaceId}
		if w.Limits != Limits() {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
	}
	return r, nil
}

func RequestBody(r domain.Request) (string, any) {
	switch r.Action {
	case "delete":
		return "DeleteRequest", wire.DeleteRequest{ExpectedRevision: r.Revision}
	case "create":
		return "CreateRequest", wire.CreateRequest{Name: r.Name, Description: r.Description, OperationId: r.OperationID}
	case "save":
		return "SaveRequest", wire.SaveRequest{Name: r.Name, Canvas: r.Canvas, ExpectedRevision: r.Revision, OperationId: r.OperationID}
	case "test":
		return "TestRequest", wire.TestRequest{ExpectedRevision: r.Revision, Input: wire.TextInput{Input: r.Input}, OperationId: r.OperationID}
	case "publish":
		return "PublishRequest", wire.PublishRequest{ExpectedRevision: r.Revision, SuccessfulTestRunId: r.TestRunID, OperationId: r.OperationID}
	case "run":
		return "RunRequest", wire.RunRequest{Version: r.Version, Input: wire.TextInput{Input: r.Input}, OperationId: r.OperationID}
	}
	return "", nil
}

func ParseRequest(action string, data []byte) (domain.Request, error) {
	r := domain.Request{Action: action}
	name := map[string]string{"delete": "DeleteRequest", "create": "CreateRequest", "save": "SaveRequest", "test": "TestRequest", "publish": "PublishRequest", "run": "RunRequest"}[action]
	if err := Validate(name, data); err != nil {
		if errors.Is(err, ErrSize) {
			return r, domain.Fail("input_too_large")
		}
		return r, domain.Fail("invalid_request")
	}
	switch action {
	case "delete":
		var v wire.DeleteRequest
		_ = json.Unmarshal(data, &v)
		r.Revision = v.ExpectedRevision
	case "create":
		var w wire.CreateRequest
		_ = json.Unmarshal(data, &w)
		r.Name = w.Name
		r.Description = w.Description
		r.OperationID = w.OperationId
	case "save":
		var w wire.SaveRequest
		_ = json.Unmarshal(data, &w)
		r.Name = w.Name
		r.Canvas = w.Canvas
		r.Revision = w.ExpectedRevision
		r.OperationID = w.OperationId
	case "test":
		var w wire.TestRequest
		_ = json.Unmarshal(data, &w)
		r.Revision = w.ExpectedRevision
		r.Input = w.Input.Input
		r.OperationID = w.OperationId
	case "publish":
		var w wire.PublishRequest
		_ = json.Unmarshal(data, &w)
		r.Revision = w.ExpectedRevision
		r.TestRunID = w.SuccessfulTestRunId
		r.OperationID = w.OperationId
	case "run":
		var w wire.RunRequest
		_ = json.Unmarshal(data, &w)
		r.Version = w.Version
		r.Input = w.Input.Input
		r.OperationID = w.OperationId
	}
	return r, nil
}
