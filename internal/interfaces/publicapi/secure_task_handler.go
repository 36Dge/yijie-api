package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	authorizationdomain "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	identitydomain "github.com/36Dge/yijie-api/internal/modules/identity/domain"
	taskdomain "github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
)

const secureTaskMaxRequestBodyBytes = 1 << 20

type SecureTaskService interface {
	Create(context.Context, taskdomain.CreateSecureTask) (taskdomain.Task, error)
	Get(context.Context, taskdomain.SecureScope, string) (taskdomain.Task, error)
	RecordAudit(context.Context, taskdomain.SecureTaskAudit) error
}

type ActiveTenantVerifier interface {
	VerifyActive(context.Context, uuid.UUID, uuid.UUID) (tenancydomain.Scope, error)
}

type TaskPermissionChecker interface {
	Check(context.Context, tenancydomain.Scope, authorizationdomain.Permission) error
}

type SecureTaskHandler struct {
	access      *AccessHandler
	tenants     ActiveTenantVerifier
	permissions TaskPermissionChecker
	tasks       SecureTaskService
}

func NewSecureTaskHandler(
	access *AccessHandler,
	tenants ActiveTenantVerifier,
	permissions TaskPermissionChecker,
	tasks SecureTaskService,
) *SecureTaskHandler {
	return &SecureTaskHandler{access: access, tenants: tenants, permissions: permissions, tasks: tasks}
}

func (handler *SecureTaskHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	audit := newSecureTaskAudit(r, "task.create")
	principal, scope, failure := handler.authorize(r, authorizationdomain.TaskCreate)
	bindSecureTaskAudit(&audit, principal, scope)
	if failure != nil {
		handler.writeFailure(w, r, audit, *failure)
		return
	}
	idempotencyKey, err := parseRequiredUUIDHeader(r.Header.Values("Idempotency-Key"))
	if err != nil {
		handler.writeFailure(w, r, audit, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	var body CreateTaskV2Request
	if err := decodeSecureTaskJSON(w, r, &body); err != nil {
		handler.writeFailure(w, r, audit, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	if !body.TaskType.Valid() || !body.Input.ContentMode.Valid() || !body.Input.SchemaVersion.Valid() || body.Input.ClientReferenceId == uuid.Nil {
		handler.writeFailure(w, r, audit, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	input, err := json.Marshal(body.Input)
	if err != nil {
		handler.writeFailure(w, r, audit, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	audit.RequestID = idempotencyKey.String()
	task, err := handler.tasks.Create(r.Context(), taskdomain.CreateSecureTask{
		SecureScope: taskdomain.SecureScope{TenantID: scope.TenantID.String(), UserID: principal.UserID.String()},
		TaskType:    string(body.TaskType), Input: input,
		IdempotencyKey: idempotencyKey.String(), RequestID: audit.RequestID,
		TraceID: audit.TraceID,
	})
	if err != nil {
		handler.writeFailure(w, r, audit, mapSecureTaskServiceFailure(err))
		return
	}
	response, err := toTaskV2(task)
	if err != nil {
		handler.writeFailure(w, r, audit, internalSecureTaskFailure())
		return
	}
	writeSecureTaskJSON(w, http.StatusCreated, response)
}

func (handler *SecureTaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	audit := newSecureTaskAudit(r, "task.read")
	principal, scope, failure := handler.authorize(r, authorizationdomain.TaskRead)
	bindSecureTaskAudit(&audit, principal, scope)
	if parsed, err := uuid.Parse(r.PathValue("task_id")); err == nil && parsed != uuid.Nil {
		audit.TaskID = parsed.String()
	}
	if failure != nil {
		handler.writeFailure(w, r, audit, *failure)
		return
	}
	task, err := handler.tasks.Get(r.Context(), taskdomain.SecureScope{
		TenantID: scope.TenantID.String(), UserID: principal.UserID.String(),
	}, r.PathValue("task_id"))
	if err != nil {
		handler.writeFailure(w, r, audit, mapSecureTaskServiceFailure(err))
		return
	}
	response, err := toTaskV2(task)
	if err != nil {
		handler.writeFailure(w, r, audit, internalSecureTaskFailure())
		return
	}
	audit.Outcome = "success"
	audit.Code = "success"
	audit.StatusCode = http.StatusOK
	if err := handler.tasks.RecordAudit(r.Context(), audit); err != nil {
		writeSecureTaskFailure(w, internalSecureTaskFailure())
		return
	}
	writeSecureTaskJSON(w, http.StatusOK, response)
}

func (handler *SecureTaskHandler) authorize(r *http.Request, permission authorizationdomain.Permission) (identitydomain.Principal, tenancydomain.Scope, *secureTaskFailure) {
	if handler == nil || handler.access == nil || handler.tenants == nil || handler.permissions == nil || handler.tasks == nil {
		failure := internalSecureTaskFailure()
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
	}
	principal, accessFailure := handler.access.authenticate(r)
	if accessFailure != nil {
		failure := mapAccessToSecureTaskFailure(*accessFailure)
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
	}
	tenantID, err := parseTenantHeader(r.Header.Values("X-Yijie-Tenant-ID"))
	if err != nil {
		failure := secureTaskFailure{status: http.StatusBadRequest, code: "invalid_tenant_context"}
		return principal, tenancydomain.Scope{}, &failure
	}
	scope, err := handler.tenants.VerifyActive(r.Context(), principal.UserID, tenantID)
	switch {
	case errors.Is(err, tenancydomain.ErrTenantAccessDenied):
		failure := accessDeniedSecureTaskFailure()
		return principal, tenancydomain.Scope{}, &failure
	case errors.Is(err, tenancydomain.ErrTenancyUnavailable):
		failure := unavailableSecureTaskFailure()
		return principal, tenancydomain.Scope{}, &failure
	case err != nil:
		failure := unavailableSecureTaskFailure()
		return principal, tenancydomain.Scope{}, &failure
	case !scope.Valid() || scope.UserID != principal.UserID || scope.TenantID != tenantID:
		failure := accessDeniedSecureTaskFailure()
		return principal, tenancydomain.Scope{}, &failure
	}
	if err := handler.permissions.Check(r.Context(), scope, permission); err != nil {
		var failure secureTaskFailure
		if errors.Is(err, authorizationdomain.ErrDenied) || errors.Is(err, authorizationdomain.ErrUnknownPermission) {
			failure = accessDeniedSecureTaskFailure()
		} else if errors.Is(err, authorizationdomain.ErrAuthorizationUnavailable) {
			failure = unavailableSecureTaskFailure()
		} else {
			failure = internalSecureTaskFailure()
		}
		return principal, scope, &failure
	}
	return principal, scope, nil
}

func toTaskV2(task taskdomain.Task) (TaskV2, error) {
	id, err := uuid.Parse(task.ID)
	if err != nil {
		return TaskV2{}, err
	}
	tenantID, err := uuid.Parse(task.TenantID)
	if err != nil {
		return TaskV2{}, err
	}
	createdBy, err := uuid.Parse(task.CreatedByUserID)
	if err != nil {
		return TaskV2{}, err
	}
	var input TaskContentReferenceV2
	decoder := json.NewDecoder(strings.NewReader(string(task.Input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return TaskV2{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return TaskV2{}, errors.New("secure task input contains trailing data")
	}
	taskType := TaskV2TaskType(task.TaskType)
	status := TaskV2Status(task.Status)
	if !input.ContentMode.Valid() || !input.SchemaVersion.Valid() || input.ClientReferenceId == uuid.Nil || !taskType.Valid() || !status.Valid() {
		return TaskV2{}, errors.New("secure task contains invalid content-free metadata")
	}
	return TaskV2{
		Id: id, TenantId: tenantID, CreatedByUserId: createdBy,
		TaskType: taskType, Status: status,
		Input:     input,
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}, nil
}

func parseRequiredUUIDHeader(values []string) (uuid.UUID, error) {
	if len(values) != 1 || len(values[0]) != 36 || strings.TrimSpace(values[0]) != values[0] {
		return uuid.Nil, errors.New("UUID header is missing or invalid")
	}
	parsed, err := uuid.Parse(values[0])
	if err != nil || parsed == uuid.Nil || !strings.EqualFold(values[0], parsed.String()) {
		return uuid.Nil, errors.New("UUID header is missing or invalid")
	}
	return parsed, nil
}

func decodeSecureTaskJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, secureTaskMaxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

type secureTaskFailure struct {
	status     int
	code       string
	challenge  bool
	retryAfter bool
}

func mapAccessToSecureTaskFailure(failure accessFailure) secureTaskFailure {
	switch failure.status {
	case http.StatusUnauthorized:
		return secureTaskFailure{status: http.StatusUnauthorized, code: "unauthenticated", challenge: true}
	case http.StatusForbidden:
		return accessDeniedSecureTaskFailure()
	case http.StatusServiceUnavailable:
		return unavailableSecureTaskFailure()
	default:
		return internalSecureTaskFailure()
	}
}

func mapSecureTaskServiceFailure(err error) secureTaskFailure {
	switch {
	case errors.Is(err, taskdomain.ErrInvalid):
		return secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request"}
	case errors.Is(err, taskdomain.ErrIdempotencyConflict):
		return secureTaskFailure{status: http.StatusConflict, code: "idempotency_conflict"}
	case errors.Is(err, taskdomain.ErrNotFound):
		return secureTaskFailure{status: http.StatusNotFound, code: "task_not_found"}
	default:
		return internalSecureTaskFailure()
	}
}

func accessDeniedSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusForbidden, code: "access_denied"}
}

func unavailableSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusServiceUnavailable, code: "authorization_unavailable", retryAfter: true}
}

func internalSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusInternalServerError, code: "internal_error"}
}

func writeSecureTaskFailure(w http.ResponseWriter, failure secureTaskFailure) {
	if failure.challenge {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if failure.retryAfter {
		w.Header().Set("Retry-After", "1")
	}
	writeSecureTaskJSON(w, failure.status, TaskV2ErrorResponse{Code: TaskV2ErrorResponseCode(failure.code)})
}

func newSecureTaskAudit(r *http.Request, operation string) taskdomain.SecureTaskAudit {
	requestID := contentFreeUUIDHeader(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	return taskdomain.SecureTaskAudit{
		Operation: operation,
		RequestID: requestID,
		TraceID:   contentFreeUUIDHeader(r.Header.Get("X-Trace-ID")),
	}
}

func contentFreeUUIDHeader(value string) string {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || !strings.EqualFold(value, parsed.String()) {
		return ""
	}
	return parsed.String()
}

func bindSecureTaskAudit(audit *taskdomain.SecureTaskAudit, principal identitydomain.Principal, scope tenancydomain.Scope) {
	if principal.UserID != uuid.Nil {
		audit.UserID = principal.UserID.String()
	}
	if scope.Valid() && scope.UserID == principal.UserID {
		audit.TenantID = scope.TenantID.String()
	}
}

func (handler *SecureTaskHandler) writeFailure(w http.ResponseWriter, r *http.Request, audit taskdomain.SecureTaskAudit, failure secureTaskFailure) {
	audit.Outcome = "failure"
	audit.Code = failure.code
	audit.StatusCode = failure.status
	if handler != nil && handler.tasks != nil {
		_ = handler.tasks.RecordAudit(r.Context(), audit)
	}
	writeSecureTaskFailure(w, failure)
}

func writeSecureTaskJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
