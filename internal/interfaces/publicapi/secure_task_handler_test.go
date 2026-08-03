package publicapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	audits  []taskdomain.SecureTaskAudit
}

func (service *fakeSecureTaskService) RecordAudit(_ context.Context, audit taskdomain.SecureTaskAudit) error {
	service.audits = append(service.audits, audit)
	return nil
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
	userID := uuid.MustParse("019fbd88-cbc3-7bf1-934d-7b05cd693f36")
	tenantID := uuid.MustParse("019fbd88-cbc3-7bf1-934d-7b05cd693f35")
	service := &fakeSecureTaskService{task: syntheticSecureTask(userID, tenantID)}
	handler := secureTaskTestHandler(userID, tenantID, service, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/v2/tasks", strings.NewReader(string(readTaskContractFixture(t, "create-request.json"))))
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
	for _, forbidden := range []string{"title", "prompt", "message", "reasoning", "project"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("response crossed content-free boundary (%s): %s", forbidden, response.Body.String())
		}
	}
	assertJSONEqual(t, response.Body.Bytes(), readTaskContractFixture(t, "task-response.json"))
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
		{name: "idempotency conflict", authorization: "Bearer synthetic-token", tenantHeader: tenantID.String(), idempotency: uuid.NewString(), serviceErr: taskdomain.ErrIdempotencyConflict, body: contentFreeCreateBody(), wantStatus: 409, wantCode: "idempotency_conflict"},
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
			if strings.Contains(response.Body.String(), `"message"`) {
				t.Fatalf("v2 error leaked detail: %s", response.Body.String())
			}
			if len(service.audits) != 1 || service.audits[0].StatusCode != test.wantStatus || service.audits[0].Code != test.wantCode {
				t.Fatalf("missing content-free failure audit: %#v", service.audits)
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
	if len(service.audits) != 1 || service.audits[0].Operation != "task.read" || service.audits[0].StatusCode != http.StatusNotFound {
		t.Fatalf("missing not-found audit: %#v", service.audits)
	}
}

func TestSecureTaskInvalidTenantScopeMapsToDeniedWithoutTrustingTenant(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	service := &fakeSecureTaskService{task: syntheticSecureTask(userID, tenantID)}
	access := NewAccessHandler(
		fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
		fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
		fakeTenantDiscovery{}, fakeCapabilityProjector{}, nil,
	)
	handler := NewSecureTaskHandler(access, fakeActiveTenantVerifier{}, fakeTaskPermissionChecker{}, service)
	request := httptest.NewRequest(http.MethodPost, "/v2/tasks", strings.NewReader(contentFreeCreateBody()))
	request.Header.Set("Authorization", "Bearer synthetic-token")
	request.Header.Set("X-Yijie-Tenant-ID", tenantID.String())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	handler.CreateTask(response, request)
	if response.Code != http.StatusForbidden || len(service.audits) != 1 {
		t.Fatalf("invalid tenant scope did not fail closed: status=%d audits=%#v", response.Code, service.audits)
	}
	if service.audits[0].UserID != userID.String() || service.audits[0].TenantID != "" {
		t.Fatalf("unverified tenant entered audit scope: %#v", service.audits[0])
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
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	return taskdomain.Task{
		ID: "019fbd88-cbc3-7bf1-934d-7b05cd693f34", TenantID: tenantID.String(), CreatedByUserID: userID.String(),
		TaskType: "conversation", Title: "local-only-conversation", Status: "draft", Input: []byte(`{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}`),
		CreatedAt: now, UpdatedAt: now,
	}
}

func contentFreeCreateBody() string {
	return `{"task_type":"conversation","input":{"schema_version":1,"content_mode":"local_only","client_reference_id":"019fbd88-cbc3-7bf1-934d-7b05cd693f37"}}`
}

func readTaskContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate secure task conformance test source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	contractsDirectory := os.Getenv("YIJIE_API_CONTRACTS_DIR")
	if contractsDirectory == "" {
		contractsDirectory = "../yijie-contracts"
	}
	if !filepath.IsAbs(contractsDirectory) {
		contractsDirectory = filepath.Join(repositoryRoot, contractsDirectory)
	}
	fixture, err := os.ReadFile(filepath.Join(contractsDirectory, "tests/fixtures/public/tasks-v2", name))
	if err != nil {
		t.Fatalf("read canonical secure task fixture %s: %v", name, err)
	}
	return fixture
}

func assertJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	var actualValue, expectedValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("JSON mismatch:\nactual=%s\nexpected=%s", actual, expected)
	}
}
