package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAccessEndpoints struct {
	tenantsCalls      int
	capabilitiesCalls int
}

func (endpoints *fakeAccessEndpoints) ListMyTenants(w http.ResponseWriter, _ *http.Request) {
	endpoints.tenantsCalls++
	w.WriteHeader(http.StatusNoContent)
}

func (endpoints *fakeAccessEndpoints) GetMyCapabilities(w http.ResponseWriter, _ *http.Request) {
	endpoints.capabilitiesCalls++
	w.WriteHeader(http.StatusNoContent)
}

type fakeDatabase struct {
	err error
}

func (database fakeDatabase) Ping(context.Context) error {
	return database.err
}

func TestHealthzReportsDatabaseState(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	NewHandler(Config{Environment: "test", Port: "0"}, fakeDatabase{}, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if body := response.Body.String(); body == "" || !containsAll(body, `"status":"ok"`, `"database":"connected"`) {
		t.Fatalf("unexpected health response: %s", body)
	}
}

func TestReadyzFailsWhenDatabaseIsUnavailable(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	NewHandler(
		Config{Environment: "test", Port: "0"},
		fakeDatabase{err: errors.New("database unavailable")},
		nil,
	).ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", response.Code)
	}
	if body := response.Body.String(); !containsAll(body, `"status":"not_ready"`) {
		t.Fatalf("unexpected readiness response: %s", body)
	}
}

func TestAccessEndpointsAreRegisteredOnlyWhenExplicitlyWired(t *testing.T) {
	disabled := httptest.NewRecorder()
	NewHandler(Config{Environment: "test", Port: "0"}, fakeDatabase{}, nil).ServeHTTP(
		disabled,
		httptest.NewRequest(http.MethodGet, "/v1/me/tenants", nil),
	)
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("expected disabled access endpoint to return 404, got %d", disabled.Code)
	}

	endpoints := &fakeAccessEndpoints{}
	handler := NewHandler(Config{Environment: "test", Port: "0"}, fakeDatabase{}, nil, WithAccessEndpoints(endpoints))
	for path, expectedCalls := range map[string]*int{
		"/v1/me/tenants":      &endpoints.tenantsCalls,
		"/v1/me/capabilities": &endpoints.capabilitiesCalls,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNoContent || *expectedCalls != 1 {
			t.Fatalf("endpoint %s was not explicitly registered: status=%d calls=%d", path, response.Code, *expectedCalls)
		}
	}
}

func TestLoadConfigKeepsPermissionProjectionOffByDefault(t *testing.T) {
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load disabled config: %v", err)
	}
	if config.PermissionProjectionEnabled {
		t.Fatal("permission projection must be disabled by default")
	}
}

func TestLoadConfigRequiresIdentitySettingsOnlyWhenProjectionEnabled(t *testing.T) {
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected enabled projection without identity configuration to fail")
	}

	t.Setenv("YIJIE_API_ACCESS_ISSUER", "https://identity.test.example/")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "https://identity.test.example/.well-known/jwks.json")
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load enabled config: %v", err)
	}
	if !config.PermissionProjectionEnabled || config.AccessIssuer == "" || config.AccessJWKSURL == "" {
		t.Fatalf("unexpected enabled access config: %#v", config)
	}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
