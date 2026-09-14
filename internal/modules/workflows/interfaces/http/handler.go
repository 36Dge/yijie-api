package workflowhttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
	"github.com/google/uuid"
)

type Service interface {
	Execute(context.Context, domain.Request) (domain.Response, error)
}
type session struct {
	id, workflow string
	digest       [32]byte
	expires      time.Time
}
type Handler struct {
	service      Service
	token, epoch string
	clock        func() time.Time
	mu           sync.Mutex
	sessions     map[string]session
}

func New(service Service, token, epoch string, clock func() time.Time) *Handler {
	if clock == nil {
		clock = time.Now
	}
	return &Handler{service: service, token: token, epoch: epoch, clock: clock, sessions: map[string]session{}}
}
func (h *Handler) Close() { h.mu.Lock(); defer h.mu.Unlock(); clear(h.sessions) }

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, r := range []struct{ pattern, action string }{
		{"GET /v1/workflow-local/status", "status"}, {"POST /v1/workflow-local/principal", "principal"},
		{"DELETE /v1/workflows/{workflow_id}", "delete"}, {"GET /v1/workflows", "list"}, {"POST /v1/workflows", "create"}, {"GET /v1/workflows/{workflow_id}", "read"}, {"PUT /v1/workflows/{workflow_id}", "save"},
		{"GET /v1/workflows/{workflow_id}/bootstrap", "bootstrap"}, {"POST /v1/workflows/{workflow_id}/test-runs", "test"}, {"POST /v1/workflows/{workflow_id}/versions", "publish"},
		{"POST /v1/workflows/{workflow_id}/runs", "run"}, {"GET /v1/workflows/{workflow_id}/runs", "history"}, {"GET /v1/workflows/{workflow_id}/runs/{run_id}", "read_run"},
		{"GET /v1/workflow-local/operations/{operation_id}", "operation"}, {"POST /v1/workflow-local/editor-sessions", "editor_open"}, {"DELETE /v1/workflow-local/editor-sessions/{session_id}", "editor_close"},
	} {
		action := r.action
		mux.HandleFunc(r.pattern, func(w http.ResponseWriter, r *http.Request) { h.handle(w, r, action) })
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 || len(r.Header.Values("X-Yijie-Run-Epoch")) != 1 || r.Header.Get("X-Yijie-Run-Epoch") != h.epoch {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeFailure(w, domain.Fail("unauthorized"))
			return
		}
		// There is no credentialed browser API/CORS surface. Browser editor only gets static assets in step 5.
		if r.Header.Get("Origin") != "" {
			writeFailure(w, domain.Fail("unauthorized"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request, action string) {
	request := domain.Request{Action: action, WorkflowID: r.PathValue("workflow_id"), RunID: r.PathValue("run_id"), OperationID: r.PathValue("operation_id"), Limit: 20}
	for _, id := range []string{request.WorkflowID, request.RunID, r.PathValue("session_id")} {
		if id != "" && !valid("Identifier", id) {
			writeFailure(w, domain.Fail("invalid_request"))
			return
		}
	}
	if request.OperationID != "" && !valid("OperationId", request.OperationID) {
		writeFailure(w, domain.Fail("invalid_request"))
		return
	}
	if action == "list" || action == "history" {
		q, err := parseList(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		request.Cursor = q.Cursor
		request.Limit = q.Limit
	} else if r.URL.RawQuery != "" {
		writeFailure(w, domain.Fail("invalid_request"))
		return
	}
	if action == "save" || action == "test" || action == "publish" || action == "bootstrap" {
		if !h.authorizeSession(r, request.WorkflowID) {
			writeFailure(w, domain.Fail("session_expired"))
			return
		}
	}
	if action == "editor_close" {
		h.mu.Lock()
		delete(h.sessions, r.PathValue("session_id"))
		h.mu.Unlock()
		writePayload(w, 200, "CloseResult", wire.CloseResult{Closed: true})
		return
	}
	if action == "editor_open" {
		data, err := readBody(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		if transport.Validate("EditorOpenRequest", data) != nil {
			writeFailure(w, domain.Fail("invalid_request"))
			return
		}
		var input wire.EditorOpenRequest
		_ = json.Unmarshal(data, &input)
		if _, err = h.service.Execute(r.Context(), domain.Request{Action: "read", WorkflowID: input.WorkflowId}); err != nil {
			writeFailure(w, err)
			return
		}
		secret, err := h.open(input.WorkflowId)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writePayload(w, 201, "EditorSessionSecret", secret)
		return
	}
	if action == "delete" || action == "create" || action == "save" || action == "test" || action == "publish" || action == "run" {
		data, err := readBody(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		parsed, err := transport.ParseRequest(action, data)
		if err != nil {
			writeFailure(w, err)
			return
		}
		parsed.WorkflowID = request.WorkflowID
		request = parsed
	} else {
		data, err := io.ReadAll(io.LimitReader(r.Body, 2))
		if err != nil || len(data) != 0 {
			writeFailure(w, domain.Fail("invalid_request"))
			return
		}
	}
	result, err := h.service.Execute(r.Context(), request)
	if err != nil {
		writeFailure(w, err)
		return
	}
	if action == "delete" && result.Deletion != nil && result.Deletion.Deleted {
		h.mu.Lock()
		for id, session := range h.sessions {
			if session.workflow == request.WorkflowID {
				delete(h.sessions, id)
			}
		}
		h.mu.Unlock()
	}
	// Old strict readers receive their original projection, even for new resources.
	if r.Header.Get("X-Yijie-Workflow-Metadata") != "description-v1" {
		if result.Workflow != nil {
			copy := *result.Workflow
			copy.Description = nil
			result.Workflow = &copy
		}
		result.Workflows = append([]domain.Workflow(nil), result.Workflows...)
		for i := range result.Workflows {
			result.Workflows[i].Description = nil
		}
	}
	name, payload, err := transport.EncodeResponse(action, result)
	if err != nil {
		if request.OperationID != "" {
			err = &domain.Failure{Code: "operation_unknown", OperationID: request.OperationID}
		}
		writeFailure(w, err)
		return
	}
	status := 200
	if action == "create" {
		status = 201
	}
	if action == "run" || action == "test" {
		status = 202
	}
	writePayload(w, status, name, payload, request.OperationID)
}
func readBody(r *http.Request) ([]byte, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, domain.Fail("invalid_request")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, domain.MessageBytes+1))
	if err != nil {
		return nil, domain.Fail("invalid_request")
	}
	if len(data) > domain.MessageBytes {
		return nil, domain.Fail("input_too_large")
	}
	return data, nil
}
func valid(name, value string) bool {
	data, _ := json.Marshal(value)
	return transport.Validate(name, data) == nil
}
func parseList(r *http.Request) (domain.Request, error) {
	result := domain.Request{Limit: 20}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return result, domain.Fail("invalid_request")
	}
	for k, v := range q {
		if (k != "limit" && k != "cursor") || len(v) != 1 {
			return result, domain.Fail("invalid_request")
		}
	}
	if values, ok := q["limit"]; ok {
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 1 || n > 50 {
			return result, domain.Fail("invalid_request")
		}
		result.Limit = n
	}
	if values, ok := q["cursor"]; ok {
		result.Cursor = values[0]
		if len(result.Cursor) > 256 {
			return result, domain.Fail("invalid_request")
		}
	}
	return result, nil
}
func (h *Handler) open(workflow string) (wire.EditorSessionSecret, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return wire.EditorSessionSecret{}, domain.Fail("internal_error")
	}
	encoded := hex.EncodeToString(secret)
	now := h.clock()
	entry := session{id: uuid.NewString(), workflow: workflow, digest: sha256.Sum256([]byte(encoded)), expires: now.Add(300 * time.Second)}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.sessions {
		if !now.Before(s.expires) {
			delete(h.sessions, id)
		}
	}
	if len(h.sessions) >= 64 {
		return wire.EditorSessionSecret{}, domain.Fail("service_unavailable")
	}
	h.sessions[entry.id] = entry
	return wire.EditorSessionSecret{SessionId: entry.id, WorkflowId: workflow, RunEpoch: h.epoch, Secret: encoded, ExpiresAtMs: entry.expires.UnixMilli()}, nil
}
func (h *Handler) authorizeSession(r *http.Request, workflow string) bool {
	if len(r.Header.Values("X-Yijie-Editor-Session")) != 1 {
		return false
	}
	token := r.Header.Get("X-Yijie-Editor-Session")
	if len(token) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	now := h.clock()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.sessions {
		if !now.Before(s.expires) {
			delete(h.sessions, id)
			continue
		}
		if s.workflow == workflow && subtle.ConstantTimeCompare(digest[:], s.digest[:]) == 1 {
			return true
		}
	}
	return false
}
func writePayload(w http.ResponseWriter, status int, name string, payload any, operationIDs ...string) {
	failure := func(code string) error {
		if len(operationIDs) > 0 && operationIDs[0] != "" {
			return &domain.Failure{Code: "operation_unknown", OperationID: operationIDs[0]}
		}
		return domain.Fail(code)
	}
	data, err := json.Marshal(payload)
	if err != nil || transport.Validate(name, data) != nil {
		writeFailure(w, failure("protocol_mismatch"))
		return
	}
	if len(data) > domain.MessageBytes {
		writeFailure(w, failure("input_too_large"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
func writeFailure(w http.ResponseWriter, err error) {
	data, _ := json.Marshal(transport.Error(err))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(transport.ErrorStatus(err))
	_, _ = w.Write(data)
}
