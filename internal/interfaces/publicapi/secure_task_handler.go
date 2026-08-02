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
	principal, scope, failure := handler.authorize(r, authorizationdomain.TaskCreate)
	if failure != nil {
		writeSecureTaskFailure(w, *failure)
		return
	}
	idempotencyKey, err := parseRequiredUUIDHeader(r.Header.Values("Idempotency-Key"))
	if err != nil {
		writeSecureTaskFailure(w, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request", message: "idempotency key is missing or invalid"})
		return
	}
	var body CreateTaskV2Request
	if err := decodeSecureTaskJSON(w, r, &body); err != nil {
		writeSecureTaskFailure(w, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request", message: "request body must match the create task contract"})
		return
	}
	input, err := json.Marshal(body.Input)
	if err != nil {
		writeSecureTaskFailure(w, secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request", message: "input must be a JSON object"})
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = idempotencyKey.String()
	}
	task, err := handler.tasks.Create(r.Context(), taskdomain.CreateSecureTask{
		SecureScope: taskdomain.SecureScope{TenantID: scope.TenantID.String(), UserID: principal.UserID.String()},
		TaskType:    body.TaskType, Title: body.Title, Input: input,
		IdempotencyKey: idempotencyKey.String(), RequestID: requestID,
		TraceID: strings.TrimSpace(r.Header.Get("X-Trace-ID")),
	})
	if err != nil {
		writeSecureTaskFailure(w, mapSecureTaskServiceFailure(err))
		return
	}
	response, err := toTaskV2(task)
	if err != nil {
		writeSecureTaskFailure(w, internalSecureTaskFailure())
		return
	}
	writeSecureTaskJSON(w, http.StatusCreated, response)
}

func (handler *SecureTaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	principal, scope, failure := handler.authorize(r, authorizationdomain.TaskRead)
	if failure != nil {
		writeSecureTaskFailure(w, *failure)
		return
	}
	task, err := handler.tasks.Get(r.Context(), taskdomain.SecureScope{
		TenantID: scope.TenantID.String(), UserID: principal.UserID.String(),
	}, r.PathValue("task_id"))
	if err != nil {
		writeSecureTaskFailure(w, mapSecureTaskServiceFailure(err))
		return
	}
	response, err := toTaskV2(task)
	if err != nil {
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
		failure := secureTaskFailure{status: http.StatusBadRequest, code: "invalid_tenant_context", message: "tenant selection is missing or invalid"}
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
	}
	scope, err := handler.tenants.VerifyActive(r.Context(), principal.UserID, tenantID)
	switch {
	case errors.Is(err, tenancydomain.ErrTenantAccessDenied):
		failure := accessDeniedSecureTaskFailure()
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
	case errors.Is(err, tenancydomain.ErrTenancyUnavailable):
		failure := unavailableSecureTaskFailure()
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
	case err != nil || !scope.Valid() || scope.UserID != principal.UserID || scope.TenantID != tenantID:
		failure := internalSecureTaskFailure()
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
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
		return identitydomain.Principal{}, tenancydomain.Scope{}, &failure
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
	var input map[string]any
	if err := json.Unmarshal(task.Input, &input); err != nil {
		return TaskV2{}, err
	}
	var result *map[string]any
	if len(task.Result) > 0 {
		value := make(map[string]any)
		if err := json.Unmarshal(task.Result, &value); err != nil {
			return TaskV2{}, err
		}
		result = &value
	}
	return TaskV2{
		Id: id, TenantId: tenantID, CreatedByUserId: createdBy,
		TaskType: task.TaskType, Title: task.Title, Status: TaskV2Status(task.Status),
		Input: input, Result: result, ErrorMessage: task.ErrorMessage,
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
	message    string
	challenge  bool
	retryAfter bool
}

func mapAccessToSecureTaskFailure(failure accessFailure) secureTaskFailure {
	switch failure.status {
	case http.StatusUnauthorized:
		return secureTaskFailure{status: http.StatusUnauthorized, code: "unauthenticated", message: "valid bearer authentication is required", challenge: true}
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
		return secureTaskFailure{status: http.StatusBadRequest, code: "invalid_request", message: "request parameters are invalid"}
	case errors.Is(err, taskdomain.ErrIdempotencyConflict):
		return secureTaskFailure{status: http.StatusConflict, code: "idempotency_conflict", message: "idempotency key conflicts with an existing request"}
	case errors.Is(err, taskdomain.ErrNotFound):
		return secureTaskFailure{status: http.StatusNotFound, code: "task_not_found", message: "task was not found"}
	default:
		return internalSecureTaskFailure()
	}
}

func accessDeniedSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusForbidden, code: "access_denied", message: "task access is denied"}
}

func unavailableSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusServiceUnavailable, code: "authorization_unavailable", message: "authorization dependencies are unavailable", retryAfter: true}
}

func internalSecureTaskFailure() secureTaskFailure {
	return secureTaskFailure{status: http.StatusInternalServerError, code: "internal_error", message: "task operation failed"}
}

func writeSecureTaskFailure(w http.ResponseWriter, failure secureTaskFailure) {
	if failure.challenge {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if failure.retryAfter {
		w.Header().Set("Retry-After", "1")
	}
	writeSecureTaskJSON(w, failure.status, ErrorResponse{Code: failure.code, Message: failure.message})
}

func writeSecureTaskJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
