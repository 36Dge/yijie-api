package workflowhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
)

const testEpoch = "15300000-0000-4000-8000-000000000001"
const testOp = "15300000-0000-4000-8000-000000000002"

type normalService struct{ calls []domain.Request }

func (s *normalService) Execute(_ context.Context, r domain.Request) (domain.Response, error) {
	s.calls = append(s.calls, r)
	workflow := domain.Workflow{ID: "123456789", Name: "Synthetic text", Revision: "revision1", Canvas: `{"nodes":[],"edges":[]}`, UpdatedAtMS: 1}
	switch r.Action {
	case "status":
		return domain.Response{Status: &domain.Status{Ready: true, Epoch: testEpoch}}, nil
	case "principal":
		return domain.Response{Principal: &domain.Principal{OwnerID: domain.OwnerID, TenantID: domain.TenantID, UserID: "15301", SpaceID: "15302"}}, nil
	case "bootstrap":
		return domain.Response{Workflow: &workflow, Principal: &domain.Principal{OwnerID: domain.OwnerID, TenantID: domain.TenantID, UserID: "15301", SpaceID: "15302"}}, nil
	case "list":
		return domain.Response{Workflows: []domain.Workflow{workflow}}, nil
	case "history":
		return domain.Response{Runs: []domain.Run{{ID: "run1", WorkflowID: workflow.ID, OperationID: testOp, Mode: "release", Version: "v0.0.1", State: "unknown", Terminal: false, StartedAtMS: 1}}}, nil
	case "operation":
		return domain.Response{Receipt: &domain.Receipt{OperationID: testOp, Kind: "create", Phase: "completed", WorkflowID: workflow.ID, Revision: workflow.Revision}}, nil
	case "run", "test", "read_run":
		input := "text"
		output := "prefixtext"
		mode := "release"
		if r.Action == "test" {
			mode = "debug"
		}
		return domain.Response{Run: &domain.Run{ID: "run1", WorkflowID: workflow.ID, OperationID: testOp, Mode: mode, Revision: "revision1", Version: "v0.0.1", State: "unknown", Terminal: false, Input: &input, Output: &output, StartedAtMS: 1}}, nil
	default:
		return domain.Response{Workflow: &workflow}, nil
	}
}
func request(t *testing.T, h http.Handler, method, path string, payload any, e string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if payload != nil {
		data, _ = json.Marshal(payload)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	r.Header.Set("X-Yijie-Run-Epoch", testEpoch)
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if e != "" {
		r.Header.Set("X-Yijie-Editor-Session", e)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestEditorSessionAbsoluteExpiryAndIdempotentClose(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	service := &normalService{}
	h := New(service, strings.Repeat("a", 64), testEpoch, func() time.Time { return now })
	defer h.Close()
	routes := h.Routes()
	opened := request(t, routes, "POST", "/v1/workflow-local/editor-sessions", wire.EditorOpenRequest{WorkflowId: "123456789"}, "")
	if opened.Code != 201 || transport.Validate("EditorSessionSecret", opened.Body.Bytes()) != nil {
		t.Fatalf("open status=%d", opened.Code)
	}
	var e wire.EditorSessionSecret
	_ = json.Unmarshal(opened.Body.Bytes(), &e)
	if e.ExpiresAtMs != now.Add(300*time.Second).UnixMilli() {
		t.Fatal("absolute expiry differs")
	}
	bootstrap := request(t, routes, "GET", "/v1/workflows/123456789/bootstrap", nil, e.Secret)
	if bootstrap.Code != 200 || transport.Validate("Bootstrap", bootstrap.Body.Bytes()) != nil {
		t.Fatal("bootstrap conformance")
	}
	for _, c := range []struct {
		method, path, schema string
		body                 any
		status               int
	}{
		{"PUT", "/v1/workflows/123456789", "Workflow", wire.SaveRequest{OperationId: testOp, ExpectedRevision: "revision1", Name: "Synthetic", Canvas: `{"nodes":[],"edges":[]}`}, 200},
		{"POST", "/v1/workflows/123456789/test-runs", "Run", wire.TestRequest{OperationId: testOp, ExpectedRevision: "revision1", Input: wire.TextInput{Input: "text"}}, 202},
		{"POST", "/v1/workflows/123456789/versions", "Workflow", wire.PublishRequest{OperationId: testOp, ExpectedRevision: "revision1", SuccessfulTestRunId: "run1"}, 200},
	} {
		w := request(t, routes, c.method, c.path, c.body, e.Secret)
		if w.Code != c.status || transport.Validate(c.schema, w.Body.Bytes()) != nil {
			t.Fatalf("editor operation %s status=%d", c.path, w.Code)
		}
	}
	now = now.Add(300 * time.Second)
	expired := request(t, routes, "GET", "/v1/workflows/123456789/bootstrap", nil, e.Secret)
	if expired.Code != 401 {
		t.Fatal("expired editor remained active")
	}
	if transport.Validate("ErrorResponse", expired.Body.Bytes()) != nil {
		t.Fatal("expired response contract")
	}
	for i := 0; i < 2; i++ {
		closed := request(t, routes, "DELETE", "/v1/workflow-local/editor-sessions/"+e.SessionId, nil, "")
		if closed.Code != 200 || transport.Validate("CloseResult", closed.Body.Bytes()) != nil {
			t.Fatal("close is not idempotent after expiry")
		}
	}
}
func TestNormalRoutesProduceSourceConformantStatuses(t *testing.T) {
	h := New(&normalService{}, strings.Repeat("a", 64), testEpoch, nil)
	defer h.Close()
	routes := h.Routes()
	for _, c := range []struct {
		method, path, schema string
		body                 any
		status               int
	}{
		{"GET", "/v1/workflow-local/status", "ServiceStatus", nil, 200},
		{"POST", "/v1/workflow-local/principal", "Principal", nil, 200},
		{"GET", "/v1/workflows?limit=20", "WorkflowList", nil, 200},
		{"POST", "/v1/workflows", "Workflow", wire.CreateRequest{Name: "Synthetic text", OperationId: testOp}, 201},
		{"GET", "/v1/workflows/123456789", "Workflow", nil, 200},
		{"GET", "/v1/workflows/123456789/runs", "RunList", nil, 200},
		{"GET", "/v1/workflows/123456789/runs/run1", "Run", nil, 200},
		{"GET", "/v1/workflow-local/operations/" + testOp, "OperationReceipt", nil, 200},
		{"POST", "/v1/workflows/123456789/runs", "Run", wire.RunRequest{OperationId: testOp, Version: "v0.0.1", Input: wire.TextInput{Input: "text"}}, 202},
	} {
		t.Run(c.schema+c.method, func(t *testing.T) {
			w := request(t, routes, c.method, c.path, c.body, "")
			if w.Code != c.status || transport.Validate(c.schema, w.Body.Bytes()) != nil {
				t.Fatalf("status=%d expected=%d", w.Code, c.status)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
}
func TestOrdinaryRequiredNameValidation(t *testing.T) {
	service := &normalService{}
	h := New(service, strings.Repeat("a", 64), testEpoch, nil)
	defer h.Close()
	w := request(t, h.Routes(), "POST", "/v1/workflows", wire.CreateRequest{Name: "", OperationId: testOp}, "")
	if w.Code != 400 || len(service.calls) != 0 {
		t.Fatal("ordinary empty name must be corrected before dispatch")
	}
}
func TestResponseBudgetCheckedBeforeHeaders(t *testing.T) {
	// Normal large whitespace in JSON can expand when the canvas is transported as a string.
	canvas := strings.Repeat("\t", 262142) + "{}"
	w := httptest.NewRecorder()
	writePayload(w, 200, "Workflow", wire.Workflow{WorkflowId: "1", Name: "Synthetic", Revision: "r1", Canvas: canvas, UpdatedAtMs: 1})
	if w.Code != 413 || !json.Valid(w.Body.Bytes()) || transport.Validate("ErrorResponse", w.Body.Bytes()) != nil {
		t.Fatal("response must be bounded valid error JSON, not partial success")
	}
}

func TestCommittedWriteResponseBudgetReturnsOriginalUnknownOperation(t *testing.T) {
	canvas := strings.Repeat("\t", 262142) + "{}"
	w := httptest.NewRecorder()
	writePayload(w, 201, "Workflow", wire.Workflow{WorkflowId: "1", Name: "Synthetic", Revision: "r1", Canvas: canvas, UpdatedAtMs: 1}, testOp)
	var reply wire.ErrorResponse
	if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.Code != wire.OperationUnknown || reply.OperationId == nil || *reply.OperationId != testOp {
		t.Fatal("post-write response failure must retain original uncertain operation")
	}
}
