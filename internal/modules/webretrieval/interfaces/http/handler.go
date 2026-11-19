package webhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
	"github.com/google/uuid"
)

type ProductIdentity func(*http.Request) (d.Scope, error)
type Handler struct {
	Service        *application.Service
	Identity       ProductIdentity
	RevokeIdentity ProductIdentity
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/web-retrieval/delegations", h.Mint)
	mux.HandleFunc("POST /v1/web-retrieval/operations", h.Execute)
	mux.HandleFunc("POST /v2/web-retrieval/searches", h.executeSearch)
	mux.HandleFunc("GET /v2/web-retrieval/searches/{scope}/{id}", h.readSearch)
	mux.HandleFunc("GET /v1/web-retrieval/operations/{id}", h.Get)
	mux.HandleFunc("POST /v1/web-retrieval/operations/{id}/cancel", h.Cancel)
	mux.HandleFunc("POST /v1/web-retrieval/bindings/{id}/revoke", h.Revoke)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	code := d.ErrorCode(err)
	status := http.StatusServiceUnavailable
	switch code {
	case d.Invalid, d.OldOperation:
		status = 400
	case d.Expired:
		status = 401
	case d.Denied:
		status = 403
	case d.Conflict:
		status = 409
	case d.Quota, d.Busy:
		status = 429
	}
	// Error text is fixed and does not include request/provider/SQL details.
	write(w, status, wire.APIError{Error: wire.AppError{Code: wire.AppErrorCode(code), Message: wire.ErrorMessages[string(code)]}, AttemptDispatched: false})
}
func read(r *http.Request, name string) ([]byte, error) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return nil, d.Invalid
	}
	if err = webwire.Validate(name, raw); err != nil {
		return nil, err
	}
	return raw, nil
}
func (h *Handler) grant(r *http.Request) (d.Binding, error) {
	if r.Header.Get("Origin") != "" {
		return d.Binding{}, d.Denied
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return d.Binding{}, d.Expired
	}
	bearer, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok {
		return d.Binding{}, d.Expired
	}
	return h.Service.Repository.Resolve(r.Context(), bearer)
}
func (h *Handler) Mint(w http.ResponseWriter, r *http.Request) {
	if h.Identity == nil || r.Header.Get("Origin") != "" {
		fail(w, d.Denied)
		return
	}
	scope, err := h.Identity(r)
	if err != nil {
		fail(w, err)
		return
	}
	raw, err := read(r, "DelegationRequest")
	if err != nil {
		fail(w, err)
		return
	}
	var request wire.DelegationRequest
	_ = json.Unmarshal(raw, &request)
	g, err := h.Service.Repository.Mint(r.Context(), scope, request.NativeBindingReference)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, wire.Delegation{BindingId: g.Binding.ID, GrantId: g.ID, Bearer: g.Bearer, ExpiresAtUnixMs: int(g.Expires.UnixMilli()), AuthorizationRevision: int(g.Binding.Scope.Revision), CredentialRevision: int(g.CredentialRevision), Audience: "builtin-web-retrieval/1"})
}
func Status(result d.Result) wire.OperationStatus {
	// Durable deadline plus dispatch fact remains meaningful after a normal
	// service restart, even if the former executor could not send its receipt.
	if !result.Operation.Deadline.IsZero() && time.Now().After(result.Operation.Deadline) {
		if result.Operation.State == "ready" {
			result.Operation.State = "cancelled"
		}
		if result.Operation.State == "dispatch_committed" {
			result.Operation.State = "unknown"
		}
	}
	available := wire.OperationStatusResultAvailability("none")
	if result.Operation.State == "completed" {
		available = "result_unavailable"
	}
	if len(result.Body) > 0 || result.Failure != "" {
		available = "available"
	}
	return wire.OperationStatus{OperationId: result.Operation.ID.String(), State: wire.OperationState(result.Operation.State), Dispatch: wire.DispatchFact(result.Operation.Dispatch), ResultAvailability: available}
}
func response(result d.Result) wire.ExecutionResult {
	value := wire.ExecutionResult{Operation: Status(result)}
	if result.Failure != "" {
		raw, _ := json.Marshal(wire.AppErrorOutput{Error: wire.AppError{Code: wire.AppErrorCode(result.Failure), Message: wire.ErrorMessages[string(result.Failure)]}})
		var body wire.ToolOutput
		_ = body.UnmarshalJSON(raw)
		value.Result = &body
		return value
	}
	if len(result.Body) > 0 {
		var body wire.ToolOutput
		if body.UnmarshalJSON(result.Body) == nil {
			value.Result = &body
		}
	}
	return value
}
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	b, err := h.grant(r)
	if err != nil {
		fail(w, err)
		return
	}
	raw, err := read(r, "OperationRequest")
	if err != nil {
		fail(w, err)
		return
	}
	request, err := webwire.Request(raw)
	if err != nil {
		fail(w, err)
		return
	}
	result, err := h.Service.Execute(r.Context(), b, request)
	if err != nil {
		var admission d.AdmissionFailure
		if errors.As(err, &admission) {
			fail(w, admission.Cause)
			return
		}
		// A failed settlement cannot be claimed free. A product 401 is returned
		// only by grant resolution above, before admission is attempted.
		write(w, 503, wire.APIError{Error: wire.AppError{Code: "outcome_unknown", Message: wire.ErrorMessages["outcome_unknown"]}, AttemptDispatched: true})
		return
	}
	write(w, 200, response(result))
}
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, d.Invalid
	}
	return id, nil
}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	b, err := h.grant(r)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	result, err := h.Service.Get(r.Context(), b, id)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, Status(result))
}
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	b, err := h.grant(r)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	o, err := h.Service.Cancel(r.Context(), b, id)
	if err != nil {
		fail(w, err)
		return
	}
	state := wire.CancelResultState("may_have_dispatched")
	if o.Dispatch == "not_dispatched" {
		state = "cancelled_before_dispatch"
	}
	if o.State == "completed" {
		state = "already_completed"
	}
	write(w, 200, wire.CancelResult{OperationId: id.String(), Dispatch: wire.DispatchFact(o.Dispatch), State: state})
}
func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	b, err := h.grant(r)
	if errors.Is(err, d.Expired) && h.RevokeIdentity != nil {
		var scope d.Scope
		scope, err = h.RevokeIdentity(r)
		if err == nil {
			b, err = h.Service.Repository.FindBinding(r.Context(), scope, id)
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	if id != b.ID {
		fail(w, d.Denied)
		return
	}
	if err = h.Service.Revoke(r.Context(), b); err != nil {
		fail(w, err)
		return
	}
	write(w, 200, wire.BindingRevocation{BindingId: id, State: "revoked"})
}

// InternalHandler is mounted only on the dedicated mutually authenticated TLS
// listener. The ordinary public listener never exposes these methods.
func (h *Handler) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/web-retrieval/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			fail(w, err)
			return
		}
		o, err := h.Service.Repository.GetInternal(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, Status(d.Result{Operation: o}))
	})
	mux.HandleFunc("POST /internal/v1/web-retrieval/claim", func(w http.ResponseWriter, r *http.Request) {
		raw, err := read(r, "ClaimRequest")
		if err != nil {
			fail(w, err)
			return
		}
		var q wire.ClaimRequest
		_ = json.Unmarshal(raw, &q)
		id, err := uuid.Parse(q.OperationId)
		if err != nil {
			fail(w, d.Invalid)
			return
		}
		receipt, err := h.Service.Repository.Claim(r.Context(), id, q.ParameterDigest, q.ExecutorInstance)
		if err != nil {
			fail(w, err)
			return
		}
		o := receipt.Operation
		write(w, 200, wire.ClaimReceipt{OperationId: id.String(), ClaimId: o.ClaimID, ExecutorInstance: o.Executor, ParameterDigest: o.Digest, DeadlineUnixMs: int(o.Deadline.UnixMilli())})
	})
	finish := func(w http.ResponseWriter, r *http.Request) {
		v2 := r.URL.Path == "/internal/v2/web-retrieval/finish"
		schema := "ClaimFinish"
		if v2 {
			schema = "ClaimFinishV2"
		}
		raw, err := read(r, schema)
		if err != nil {
			fail(w, err)
			return
		}
		var q wire.ClaimFinish
		facts := d.SearchFacts{}
		if v2 {
			var msg wire.ClaimFinishV2
			if json.Unmarshal(raw, &msg) != nil {
				fail(w, d.Invalid)
				return
			}
			q = msg.Finish
			facts.Transport = string(msg.Facts.Transport)
			facts.Outcome = string(msg.Facts.Outcome)
			if msg.Facts.ErrorCode != nil {
				facts.ErrorCode = d.Code(*msg.Facts.ErrorCode)
			}
			if (q.State == "completed") != (facts.Transport == "responded" && (facts.Outcome == "results" || facts.Outcome == "partial" || facts.Outcome == "empty")) || (facts.Outcome == "failed") != (facts.ErrorCode != "") {
				fail(w, d.Invalid)
				return
			}
		} else {
			_ = json.Unmarshal(raw, &q)
		}
		id, err := uuid.Parse(q.Receipt.OperationId)
		if err != nil {
			fail(w, d.Invalid)
			return
		}
		receipt := d.Receipt{Operation: d.Operation{Facts: facts, ID: id, ClaimID: q.Receipt.ClaimId, Executor: q.Receipt.ExecutorInstance, Digest: q.Receipt.ParameterDigest, Deadline: time.UnixMilli(int64(q.Receipt.DeadlineUnixMs))}}
		if err = h.Service.Repository.Finish(r.Context(), receipt, string(q.State), string(q.Dispatch)); err != nil {
			fail(w, err)
			return
		}
		availability := wire.OperationStatusResultAvailability("none")
		if q.State == "completed" {
			availability = "result_unavailable"
		}
		write(w, 200, wire.OperationStatus{OperationId: id.String(), State: wire.OperationState(q.State), Dispatch: q.Dispatch, ResultAvailability: availability})
	}
	mux.HandleFunc("POST /internal/v1/web-retrieval/finish", finish)
	mux.HandleFunc("POST /internal/v2/web-retrieval/finish", finish)
	return mux
}
