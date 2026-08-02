package publicapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authorizationdomain "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	identitydomain "github.com/36Dge/yijie-api/internal/modules/identity/domain"
	taskdomain "github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/36Dge/yijie-api/internal/platform/authn"
	"github.com/google/uuid"
)

type fakeActiveTenantVerifier struct {
	scope tenancydomain.Scope
	err   error
}

func (verifier fakeActiveTenantVerifier) VerifyActive(context.Context, uuid.UUID, uuid.UUID) (tenancydomain.Scope, error) {
	return verifier.scope, verifier.err
}

type fakeTaskPermissionChecker struct{ err error }

func (checker fakeTaskPermissionChecker) Check(context.Context, tenancydomain.Scope, authorizationdomain.Permission) error {
	return checker.err
}

type fakeSecureTaskService struct {
	created taskdomain.CreateSecureTask
	task    taskdomain.Task
	err     error
}

func (service *fakeSecureTaskService) Create(_ context.Context, command taskdomain.CreateSecureTask) (taskdomain.Task, error) {
	service.created = command
	return service.task, service.err
}

func (service *fakeSecureTaskService) Get(_ context.Context, scope taskdomain.SecureScope, _ string) (taskdomain.Task, error) {
	service.created.SecureScope = scope
	return service.task, service.err
}

func TestSecureTaskCreateBindsVerifiedIdentityAndTenant(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	service := &fakeSecureTaskService{task: syntheticSecureTask(userID, tenantID)}
	handler := secureTaskTestHandler(userID, tenantID, service, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/v2/tasks", strings.NewReader(`{"task_type":"conversation","title":"Synthetic task","input":{"text":"hello"}}`))
	request.Header.Set("Authorization", "Bearer synthetic-token")
	request.Header.Set("X-Yijie-Tenant-ID", tenantID.String())
	request.Header.Set("Idempotency-Key", "ad17b95d-a91d-4ef8-9736-8bc36cefcbb6")
	response := httptest.NewRecorder()
	handler.CreateTask(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if service.created.UserID != userID.String() || service.created.TenantID != tenantID.String() {
		t.Fatalf("request was not bound to verified scope: %#v", service.created)
	}
	if !strings.Contains(response.Body.String(), `"created_by_user_id":"`+userID.String()+`"`) {
		t.Fatalf("unexpected creator-private response: %s", response.Body.String())
	}
}

func TestSecureTaskCreateFailsClosedAcrossSecurityBoundary(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	tests := []struct {
		name          string
		authorization string
		tenantHeader  string
		idempotency   string
		tenantErr     error
		permissionErr error
		serviceErr    error
		body          string
		wantStatus    int
		wantCode      string
	}{
		{name: "missing bearer", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), body: `{}`, wantStatus: 401, wantCode: "unauthenticated"},
		{name: "invalid tenant", authorization: "Bearer synthetic-token", tenantHeader: "not-a-uuid", idempotency: uuid.NewString(), body: `{}`, wantStatus: 400, wantCode: "invalid_tenant_context"},
		{name: "membership denied", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), tenantErr: tenancydomain.ErrTenantAccessDenied, body: `{}`, wantStatus: 403, wantCode: "access_denied"},
		{name: "permission denied", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), permissionErr: authorizationdomain.ErrDenied, body: `{}`, wantStatus: 403, wantCode: "access_denied"},
		{name: "authorization unavailable", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), permissionErr: authorizationdomain.ErrAuthorizationUnavailable, body: `{}`, wantStatus: 503, wantCode: "authorization_unavailable"},
		{name: "missing idempotency", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), body: `{}`, wantStatus: 400, wantCode: "invalid_request"},
		{name: "unknown body field", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), body: `{"tenant_id":"` + tenantID.String() + `"}`, wantStatus: 400, wantCode: "invalid_request"},
		{name: "idempotency conflict", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), serviceErr: taskdomain.ErrIdempotencyConflict, body: `{"task_type":"conversation","title":"Synthetic","input":{}}`, wantStatus: 409, wantCode: "idempotency_conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeSecureTaskService{task: syntheticSecureTask(userID, tenantID), err: test.serviceErr}
			handler := secureTaskTestHandler(userID, tenantID, service, test.tenantErr, test.permissionErr)
			request := httptest.NewRequest(http.MethodPost, "/v2/tasks", strings.NewReader(test.body))
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			if test.tenantHeader != "" {
				request.Header.Set("X-Yijie-Tenant-ID", test.tenantHeader)
			}
			if test.idempotency != "" {
				request.Header.Set("Idempotency-Key", test.idempotency)
			}
			response := httptest.NewRecorder()
			handler.CreateTask(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("unexpected failure: status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("secure task response was cacheable: %v", response.Header())
			}
		})
	}
}

func TestSecureTaskGetHidesMissingOrOtherCreator(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	service := &fakeSecureTaskService{err: taskdomain.ErrNotFound}
	handler := secureTaskTestHandler(userID, tenantID, service, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v2/tasks/019c0123-4567-7abc-8123-456789abcd00", nil)
	request.SetPathValue("task_id", "019c0123-4567-7abc-8123-456789abcd00")
	request.Header.Set("Authorization", "Bearer synthetic-token")
	request.Header.Set("X-Yijie-Tenant-ID", tenantID.String())
	response := httptest.NewRecorder()
	handler.GetTask(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"task_not_found"`) {
		t.Fatalf("unexpected creator-private not-found response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func secureTaskTestHandler(userID, tenantID uuid.UUID, tasks *fakeSecureTaskService, tenantErr, permissionErr error) *SecureTaskHandler {
	access := NewAccessHandler(
		fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
		fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
		fakeTenantDiscovery{}, fakeCapabilityProjector{}, nil,
	)
	return NewSecureTaskHandler(
		access,
		fakeActiveTenantVerifier{scope: tenancydomain.Scope{UserID: userID, TenantID: tenantID, AuthorizationRevision: 1}, err: tenantErr},
		fakeTaskPermissionChecker{err: permissionErr},
		tasks,
	)
}

func syntheticSecureTask(userID, tenantID uuid.UUID) taskdomain.Task {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	return taskdomain.Task{
		ID: "019c0123-4567-7abc-8123-456789abcd00", TenantID: tenantID.String(), CreatedByUserID: userID.String(),
		TaskType: "conversation", Title: "Synthetic task", Status: "draft", Input: []byte(`{"text":"hello"}`),
		CreatedAt: now, UpdatedAt: now,
	}
}
