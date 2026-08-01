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
	"testing"
	"time"

	authorizationdomain "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	identitydomain "github.com/36Dge/yijie-api/internal/modules/identity/domain"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/36Dge/yijie-api/internal/platform/authn"
	"github.com/google/uuid"
)

type fakeAccessTokenVerifier struct {
	identity authn.Identity
	err      error
}

func (verifier fakeAccessTokenVerifier) Verify(context.Context, string) (authn.Identity, error) {
	return verifier.identity, verifier.err
}

type fakePrincipalResolver struct {
	principal identitydomain.Principal
	err       error
}

func (resolver fakePrincipalResolver) ResolvePrincipal(context.Context, string, string) (identitydomain.Principal, error) {
	return resolver.principal, resolver.err
}

type fakeTenantDiscovery struct {
	memberships []tenancydomain.Membership
	err         error
}

func (discovery fakeTenantDiscovery) ListActive(context.Context, uuid.UUID) ([]tenancydomain.Membership, error) {
	return discovery.memberships, discovery.err
}

type fakeCapabilityProjector struct {
	projection authorizationdomain.Projection
	err        error
}

func (projector fakeCapabilityProjector) Project(context.Context, uuid.UUID, uuid.UUID) (authorizationdomain.Projection, error) {
	return projector.projection, projector.err
}

type recordedAccessMetric struct {
	operation string
	status    int
	reason    string
}

type fakeAccessMetrics struct {
	records []recordedAccessMetric
}

func (metrics *fakeAccessMetrics) ObserveAccess(operation string, status int, reason string, _ time.Duration) {
	metrics.records = append(metrics.records, recordedAccessMetric{operation: operation, status: status, reason: reason})
}

func TestListMyTenantsProducerConformance(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	metrics := &fakeAccessMetrics{}
	handler := NewAccessHandler(
		fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
		fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
		fakeTenantDiscovery{memberships: []tenancydomain.Membership{
			{TenantID: uuid.MustParse("019c0123-4567-7abc-8123-456789abcdee"), DisplayName: "Synthetic EU Tenant"},
			{TenantID: uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef"), DisplayName: "Synthetic US Tenant"},
		}},
		fakeCapabilityProjector{},
		metrics,
	)

	response := serveAccess(t, handler.ListMyTenants, "/v1/me/tenants", "synthetic-token", "")
	assertFixtureResponse(t, response, http.StatusOK, "tenant-list-v1-multiple.json")
	assertAccessHeaders(t, response, false, false)
	assertMetric(t, metrics, AccessOperationListMyTenants, http.StatusOK, AccessReasonSuccess)
}

func TestListMyTenantsEmptyAndSingleConformance(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tests := []struct {
		name        string
		memberships []tenancydomain.Membership
		fixture     string
	}{
		{name: "empty", memberships: []tenancydomain.Membership{}, fixture: "tenant-list-v1-empty.json"},
		{name: "single", memberships: []tenancydomain.Membership{{TenantID: uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef"), DisplayName: "Synthetic Tenant"}}, fixture: "tenant-list-v1-single.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewAccessHandler(
				fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
				fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
				fakeTenantDiscovery{memberships: test.memberships},
				fakeCapabilityProjector{},
				nil,
			)
			response := serveAccess(t, handler.ListMyTenants, "/v1/me/tenants", "synthetic-token", "")
			assertFixtureResponse(t, response, http.StatusOK, test.fixture)
			assertAccessHeaders(t, response, false, false)
		})
	}
}

func TestGetMyCapabilitiesProducerConformance(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	metrics := &fakeAccessMetrics{}
	handler := NewAccessHandler(
		fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
		fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
		fakeTenantDiscovery{},
		fakeCapabilityProjector{projection: authorizationdomain.Projection{
			TenantID:              tenantID,
			AuthorizationRevision: 42,
			ExpiresAt:             time.Date(2026, time.August, 1, 12, 5, 0, 0, time.UTC),
			Permissions: []authorizationdomain.Permission{
				authorizationdomain.ScheduleRead,
				authorizationdomain.TaskCreate,
				authorizationdomain.TaskRead,
			},
		}},
		metrics,
		WithAccessClock(func() time.Time { return now }),
	)

	response := serveAccess(t, handler.GetMyCapabilities, "/v1/me/capabilities", "synthetic-token", tenantID.String())
	assertFixtureResponse(t, response, http.StatusOK, "capability-v1-ready.json")
	assertAccessHeaders(t, response, false, false)
	assertMetric(t, metrics, AccessOperationGetMyCapabilities, http.StatusOK, AccessReasonSuccess)
}

func TestGetMyCapabilitiesEmptyConformance(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	handler := NewAccessHandler(
		fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
		fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
		fakeTenantDiscovery{},
		fakeCapabilityProjector{projection: authorizationdomain.Projection{
			TenantID:              tenantID,
			AuthorizationRevision: 43,
			ExpiresAt:             now.Add(5 * time.Minute),
			Permissions:           []authorizationdomain.Permission{},
		}},
		nil,
		WithAccessClock(func() time.Time { return now }),
	)
	response := serveAccess(t, handler.GetMyCapabilities, "/v1/me/capabilities", "synthetic-token", tenantID.String())
	assertFixtureResponse(t, response, http.StatusOK, "capability-v1-empty.json")
}

func TestCapabilityResponseValidationFailsClosed(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	valid := authorizationdomain.Projection{
		TenantID:              tenantID,
		AuthorizationRevision: 1,
		ExpiresAt:             now.Add(5 * time.Minute),
		Permissions:           []authorizationdomain.Permission{authorizationdomain.TaskRead},
	}
	tests := []struct {
		name       string
		projection authorizationdomain.Projection
	}{
		{name: "tenant mismatch", projection: func() authorizationdomain.Projection { value := valid; value.TenantID = uuid.New(); return value }()},
		{name: "zero revision", projection: func() authorizationdomain.Projection { value := valid; value.AuthorizationRevision = 0; return value }()},
		{name: "expired", projection: func() authorizationdomain.Projection { value := valid; value.ExpiresAt = now; return value }()},
		{name: "expiry over five minutes", projection: func() authorizationdomain.Projection {
			value := valid
			value.ExpiresAt = now.Add(5*time.Minute + time.Nanosecond)
			return value
		}()},
		{name: "duplicate capability", projection: func() authorizationdomain.Projection {
			value := valid
			value.Permissions = []authorizationdomain.Permission{authorizationdomain.TaskRead, authorizationdomain.TaskRead}
			return value
		}()},
		{name: "unsorted capability", projection: func() authorizationdomain.Projection {
			value := valid
			value.Permissions = []authorizationdomain.Permission{authorizationdomain.TaskRead, authorizationdomain.TaskCreate}
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewAccessHandler(
				fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}},
				fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}},
				fakeTenantDiscovery{},
				fakeCapabilityProjector{projection: test.projection},
				nil,
				WithAccessClock(func() time.Time { return now }),
			)
			response := serveAccess(t, handler.GetMyCapabilities, "/v1/me/capabilities", "synthetic-token", tenantID.String())
			assertFixtureResponse(t, response, http.StatusInternalServerError, "error-internal-error.json")
			assertAccessHeaders(t, response, false, false)
		})
	}
}

func TestAccessEndpointsFailClosedWithStableErrors(t *testing.T) {
	userID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdea")
	tenantID := uuid.MustParse("019c0123-4567-7abc-8123-456789abcdef")
	validVerifier := fakeAccessTokenVerifier{identity: authn.Identity{Issuer: "https://identity.test.example/", Subject: "synthetic-subject"}}
	validPrincipal := fakePrincipalResolver{principal: identitydomain.Principal{UserID: userID}}
	validProjection := fakeCapabilityProjector{projection: authorizationdomain.Projection{TenantID: tenantID, AuthorizationRevision: 1, ExpiresAt: time.Now().Add(time.Minute)}}

	tests := []struct {
		name           string
		endpoint       string
		token          string
		tenantHeader   string
		verifier       fakeAccessTokenVerifier
		principals     fakePrincipalResolver
		tenants        fakeTenantDiscovery
		projector      fakeCapabilityProjector
		status         int
		fixture        string
		challenge      bool
		retryAfter     bool
		expectedReason string
	}{
		{name: "tenant list missing bearer", endpoint: "tenants", principals: validPrincipal, token: "", status: 401, fixture: "error-unauthorized.json", challenge: true, expectedReason: AccessReasonUnauthorized},
		{name: "capability missing bearer wins over missing tenant", endpoint: "capabilities", principals: validPrincipal, token: "", status: 401, fixture: "error-unauthorized.json", challenge: true, expectedReason: AccessReasonUnauthorized},
		{name: "malformed bearer", endpoint: "tenants", principals: validPrincipal, token: "malformed", status: 401, fixture: "error-unauthorized.json", challenge: true, expectedReason: AccessReasonUnauthorized},
		{name: "JWKS unavailable", endpoint: "tenants", token: "synthetic-token", verifier: fakeAccessTokenVerifier{err: authn.ErrKeyUnavailable}, principals: validPrincipal, status: 503, fixture: "error-authorization-unavailable.json", retryAfter: true, expectedReason: AccessReasonDependencyUnavailable},
		{name: "unmapped user denied", endpoint: "tenants", token: "synthetic-token", verifier: validVerifier, principals: fakePrincipalResolver{err: identitydomain.ErrIdentityNotFound}, status: 403, fixture: "error-user-access-denied.json", expectedReason: AccessReasonUserDenied},
		{name: "suspended user denied", endpoint: "tenants", token: "synthetic-token", verifier: validVerifier, principals: fakePrincipalResolver{err: identitydomain.ErrUserSuspended}, status: 403, fixture: "error-user-access-denied.json", expectedReason: AccessReasonUserDenied},
		{name: "identity store unavailable", endpoint: "tenants", token: "synthetic-token", verifier: validVerifier, principals: fakePrincipalResolver{err: identitydomain.ErrIdentityUnavailable}, status: 503, fixture: "error-authorization-unavailable.json", retryAfter: true, expectedReason: AccessReasonDependencyUnavailable},
		{name: "tenant store unavailable", endpoint: "tenants", token: "synthetic-token", verifier: validVerifier, principals: validPrincipal, tenants: fakeTenantDiscovery{err: tenancydomain.ErrTenancyUnavailable}, status: 503, fixture: "error-authorization-unavailable.json", retryAfter: true, expectedReason: AccessReasonDependencyUnavailable},
		{name: "missing tenant header", endpoint: "capabilities", token: "synthetic-token", verifier: validVerifier, principals: validPrincipal, projector: validProjection, status: 400, fixture: "error-invalid-tenant-context.json", expectedReason: AccessReasonInvalidTenantContext},
		{name: "noncanonical tenant header", endpoint: "capabilities", token: "synthetic-token", tenantHeader: "{" + tenantID.String() + "}", verifier: validVerifier, principals: validPrincipal, projector: validProjection, status: 400, fixture: "error-invalid-tenant-context.json", expectedReason: AccessReasonInvalidTenantContext},
		{name: "tenant denied", endpoint: "capabilities", token: "synthetic-token", tenantHeader: tenantID.String(), verifier: validVerifier, principals: validPrincipal, projector: fakeCapabilityProjector{err: authorizationdomain.ErrDenied}, status: 403, fixture: "error-tenant-access-denied.json", expectedReason: AccessReasonTenantDenied},
		{name: "RBAC unavailable", endpoint: "capabilities", token: "synthetic-token", tenantHeader: tenantID.String(), verifier: validVerifier, principals: validPrincipal, projector: fakeCapabilityProjector{err: authorizationdomain.ErrAuthorizationUnavailable}, status: 503, fixture: "error-authorization-unavailable.json", retryAfter: true, expectedReason: AccessReasonDependencyUnavailable},
		{name: "invalid projection is internal error", endpoint: "capabilities", token: "synthetic-token", tenantHeader: tenantID.String(), verifier: validVerifier, principals: validPrincipal, projector: fakeCapabilityProjector{err: authorizationdomain.ErrInvalidProjection}, status: 500, fixture: "error-internal-error.json", expectedReason: AccessReasonInternalError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := &fakeAccessMetrics{}
			handler := NewAccessHandler(test.verifier, test.principals, test.tenants, test.projector, metrics)
			var endpoint http.HandlerFunc
			operation := AccessOperationListMyTenants
			path := "/v1/me/tenants"
			if test.endpoint == "capabilities" {
				endpoint = handler.GetMyCapabilities
				operation = AccessOperationGetMyCapabilities
				path = "/v1/me/capabilities"
			} else {
				endpoint = handler.ListMyTenants
			}
			response := serveAccess(t, endpoint, path, test.token, test.tenantHeader)
			assertFixtureResponse(t, response, test.status, test.fixture)
			assertAccessHeaders(t, response, test.challenge, test.retryAfter)
			assertMetric(t, metrics, operation, test.status, test.expectedReason)
			if response.Code == http.StatusConflict {
				t.Fatal("access endpoints must never return 409")
			}
		})
	}
}

func TestAccessEndpointsRejectDuplicateAuthorizationHeaders(t *testing.T) {
	handler := NewAccessHandler(fakeAccessTokenVerifier{}, fakePrincipalResolver{}, fakeTenantDiscovery{}, fakeCapabilityProjector{}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/me/tenants", nil)
	request.Header.Add("Authorization", "Bearer first-synthetic-token")
	request.Header.Add("Authorization", "Bearer second-synthetic-token")
	response := httptest.NewRecorder()
	handler.ListMyTenants(response, request)

	assertFixtureResponse(t, response, http.StatusUnauthorized, "error-unauthorized.json")
	assertAccessHeaders(t, response, true, false)
}

func serveAccess(t *testing.T, endpoint http.HandlerFunc, path, token, tenantHeader string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		if token == "malformed" {
			request.Header.Set("Authorization", token)
		} else {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	if tenantHeader != "" {
		request.Header.Set("X-Yijie-Tenant-ID", tenantHeader)
	}
	response := httptest.NewRecorder()
	endpoint(response, request)
	return response
}

func assertFixtureResponse(t *testing.T, response *httptest.ResponseRecorder, status int, fixtureName string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, response.Code, response.Body.String())
	}
	var actual any
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var expected any
	if err := json.Unmarshal(readContractFixture(t, fixtureName), &expected); err != nil {
		t.Fatalf("decode fixture %s: %v", fixtureName, err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("response does not match %s\nexpected: %#v\nactual:   %#v", fixtureName, expected, actual)
	}
}

func assertAccessHeaders(t *testing.T, response *httptest.ResponseRecorder, challenge, retryAfter bool) {
	t.Helper()
	if value := response.Header().Get("Cache-Control"); value != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", value)
	}
	if value := response.Header().Get("Content-Type"); value != "application/json" {
		t.Fatalf("expected application/json, got %q", value)
	}
	if got := response.Header().Get("WWW-Authenticate") != ""; got != challenge {
		t.Fatalf("unexpected WWW-Authenticate presence: %t", got)
	}
	if got := response.Header().Get("Retry-After") != ""; got != retryAfter {
		t.Fatalf("unexpected Retry-After presence: %t", got)
	}
}

func assertMetric(t *testing.T, metrics *fakeAccessMetrics, operation string, status int, reason string) {
	t.Helper()
	if len(metrics.records) != 1 {
		t.Fatalf("expected one metric, got %#v", metrics.records)
	}
	expected := recordedAccessMetric{operation: operation, status: status, reason: reason}
	if metrics.records[0] != expected {
		t.Fatalf("expected metric %#v, got %#v", expected, metrics.records[0])
	}
}

func readContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate conformance test source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	contractsDirectory := os.Getenv("YIJIE_API_CONTRACTS_DIR")
	if contractsDirectory == "" {
		contractsDirectory = "../yijie-contracts"
	}
	if !filepath.IsAbs(contractsDirectory) {
		contractsDirectory = filepath.Join(repositoryRoot, contractsDirectory)
	}
	fixture, err := os.ReadFile(filepath.Join(contractsDirectory, "tests/fixtures/public/access", name))
	if err != nil {
		t.Fatalf("read canonical contract fixture %s: %v", name, err)
	}
	return fixture
}
