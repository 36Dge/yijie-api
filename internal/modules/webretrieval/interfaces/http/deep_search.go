package webhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
	"github.com/google/uuid"
)

func (h *Handler) executeScoped(w http.ResponseWriter, r *http.Request, fetch bool) {
	b, err := h.grant(r)
	if err != nil {
		fail(w, err)
		return
	}
	schema := "DeepSearchRequest"
	if fetch {
		schema = "ScopedFetchRequest"
	}
	raw, err := read(r, schema)
	if err != nil {
		fail(w, err)
		return
	}
	var scope uuid.UUID
	var deadline int
	var body []byte
	if fetch {
		var message wire.ScopedFetchRequest
		if err = json.Unmarshal(raw, &message); err == nil {
			scope, deadline = message.ScopeId, message.DeadlineUnixMs
			body, err = json.Marshal(message.Request)
		}
	} else {
		var message wire.DeepSearchRequest
		if err = json.Unmarshal(raw, &message); err == nil {
			scope, deadline = message.ScopeId, message.DeadlineUnixMs
			body, err = json.Marshal(message.Request)
		}
	}
	if err != nil || scope == uuid.Nil {
		fail(w, d.Invalid)
		return
	}
	q, err := webwire.ScopedRequest(body)
	if err != nil {
		fail(w, err)
		return
	}
	q.SearchScope = scope
	q.Deadline = time.UnixMilli(int64(deadline))
	result, err := h.Service.Execute(r.Context(), b, q)
	if err != nil {
		var admission d.AdmissionFailure
		if errors.As(err, &admission) {
			fail(w, admission.Cause)
		} else {
			write(w, 503, wire.APIError{Error: wire.AppError{Code: "outcome_unknown", Message: wire.ErrorMessages["outcome_unknown"]}, AttemptDispatched: true})
		}
		return
	}
	relation := "first"
	if result.Existing {
		relation = "reused"
		if result.Operation.State == "ready" || result.Operation.State == "dispatch_committed" {
			relation = "joined"
		}
	}
	write(w, 200, searchResponse(q.ID, result, relation))
}
func (h *Handler) readScoped(w http.ResponseWriter, r *http.Request, tool string) {
	b, err := h.grant(r)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := pathID(r)
	scope, scopeErr := uuid.Parse(r.PathValue("scope"))
	if err != nil || scopeErr != nil || scope == uuid.Nil {
		fail(w, d.Invalid)
		return
	}
	result, err := h.Service.ReadScoped(r.Context(), b, scope, id)
	if err != nil {
		fail(w, err)
		return
	}
	if result.Operation.Tool != tool {
		fail(w, d.Denied)
		return
	}
	write(w, 200, searchResponse(id, result, "reused"))
}

func searchResponse(call uuid.UUID, r d.Result, relation string) wire.DeepSearchResponse {
	status := Status(r)
	o := r.Operation
	fact := wire.DeepSearchReceipt{Version: 2, OperationId: &o.ID, Relation: wire.DeepSearchReceiptRelation(relation),
		Dispatch: wire.DeepSearchReceiptDispatch(o.Facts.Transport), Outcome: wire.DeepSearchReceiptOutcome(o.Facts.Outcome), Availability: "unavailable"}
	if fact.Dispatch == "" {
		fact.Dispatch = "uncertain"
	}
	if status.Dispatch == "not_dispatched" {
		fact.Dispatch = "not_dispatched"
	}
	body := response(r).Result
	if status.State == "ready" || status.State == "dispatch_committed" {
		fact.Outcome = "pending"
		fact.Availability = "pending"
		body = nil
	} else {
		if fact.Outcome == "" || fact.Outcome == "pending" {
			fact.Outcome = "unknown"
		}
		if body != nil {
			fact.Availability = "available"
		}
		code := o.Facts.ErrorCode
		if code == "" {
			code = r.Failure
		}
		if code != "" {
			c := wire.DeepSearchReceiptErrorCode(code)
			fact.ErrorCode = &c
		}
	}
	return wire.DeepSearchResponse{CallId: call.String(), Receipt: fact, Result: body}
}
