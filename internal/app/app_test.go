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

type fakeSecureTaskEndpoints struct {
	createCalls int
	getCalls    int
}

func (endpoints *fakeSecureTaskEndpoints) CreateTask(w http.ResponseWriter, _ *http.Request) {
	endpoints.createCalls++
	w.WriteHeader(http.StatusNoContent)
}

func (endpoints *fakeSecureTaskEndpoints) GetTask(w http.ResponseWriter, _ *http.Request) {
	endpoints.getCalls++
	w.WriteHeader(http.StatusNoContent)
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

func TestClosedLocalLabProfilesDoNotRegisterLegacyTasks(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		method  string
		path    string
		body    string
	}{
		{name: "FEAT-125 create", profile: ServiceProfileFeat125LocalLab, method: http.MethodPost, path: "/v1/tasks", body: `{}`},
		{name: "FEAT-125 get", profile: ServiceProfileFeat125LocalLab, method: http.MethodGet, path: "/v1/tasks/not-a-uuid"},
		{name: "FEAT-126 create", profile: ServiceProfileFeat126S10LocalLab, method: http.MethodPost, path: "/v1/tasks", body: `{}`},
		{name: "FEAT-126 get", profile: ServiceProfileFeat126S10LocalLab, method: http.MethodGet, path: "/v1/tasks/not-a-uuid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defaultResponse := httptest.NewRecorder()
			NewHandler(Config{Environment: "test"}, fakeDatabase{}, nil).ServeHTTP(
				defaultResponse,
				httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)),
			)
			if defaultResponse.Code == http.StatusNotFound {
				t.Fatalf("default profile must retain legacy route %s %s", test.method, test.path)
			}

			localLabResponse := httptest.NewRecorder()
			NewHandler(
				Config{Environment: "nonproduction", ServiceProfile: test.profile},
				fakeDatabase{},
				nil,
			).ServeHTTP(
				localLabResponse,
				httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)),
			)
			if localLabResponse.Code != http.StatusNotFound {
				t.Fatalf("local-lab profile must not register %s %s: got %d", test.method, test.path, localLabResponse.Code)
			}
		})
	}
}

func TestSecureTasksRequireFlagAndExplicitWiring(t *testing.T) {
	endpoints := &fakeSecureTaskEndpoints{}
	for _, config := range []Config{
		{Environment: "local", ServiceProfile: ServiceProfileFeat125LocalLab},
		{Environment: "local", ServiceProfile: ServiceProfileFeat125LocalLab, SecureTasksEnabled: true},
		{Environment: "nonproduction", ServiceProfile: ServiceProfileFeat126S10LocalLab},
		{Environment: "nonproduction", ServiceProfile: ServiceProfileFeat126S10LocalLab, SecureTasksEnabled: true},
	} {
		handler := NewHandler(config, fakeDatabase{}, nil, WithSecureTaskEndpoints(endpoints))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v2/tasks", strings.NewReader(`{}`)))
		if !config.SecureTasksEnabled && response.Code != http.StatusNotFound {
			t.Fatalf("disabled secure task route returned %d", response.Code)
		}
		if config.SecureTasksEnabled && response.Code != http.StatusNoContent {
			t.Fatalf("enabled secure task route returned %d", response.Code)
		}
	}
	if endpoints.createCalls != 2 {
		t.Fatalf("secure task endpoint call count = %d", endpoints.createCalls)
	}
}

func TestSecureTasksConfigurationFailsClosedOutsidePinnedLocalProfile(t *testing.T) {
	const validPin = "07a3bb2ef51a5b559fe42b423339f6c886c5e17903b1cf2d8ca26bf1b5574650"
	setValid := func(t *testing.T) {
		t.Helper()
		t.Setenv("YIJIE_ENV", "local")
		t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
		t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat125LocalLab)
		t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
		t.Setenv("YIJIE_API_SECURE_TASKS_ENABLED", "true")
		t.Setenv("YIJIE_API_ACCESS_ISSUER", Feat125LocalLabIssuer)
		t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat125LocalLabJWKSURL)
		t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "/tmp/feat-125-local-ca.pem")
		t.Setenv("YIJIE_API_LOCAL_CA_SHA256", validPin)
	}
	setValid(t)
	config, err := LoadConfig()
	if err != nil || !config.SecureTasksEnabled {
		t.Fatalf("load local secure tasks: config=%#v err=%v", config, err)
	}
	for name, change := range map[string]func(*testing.T){
		"default profile":     func(t *testing.T) { t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileDefault) },
		"projection disabled": func(t *testing.T) { t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "false") },
		"production":          func(t *testing.T) { t.Setenv("YIJIE_ENV", "production") },
	} {
		t.Run(name, func(t *testing.T) {
			setValid(t)
			change(t)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("expected secure tasks configuration to fail closed")
			}
		})
	}
}

func TestServerAddressPinsClosedLocalLabProfilesToIPv4Loopback(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected string
	}{
		{name: "zero value keeps legacy behavior", config: Config{Port: "8080"}, expected: ":8080"},
		{name: "default profile keeps legacy behavior", config: Config{ServiceProfile: ServiceProfileDefault, Port: "18080"}, expected: ":18080"},
		{name: "FEAT-125 local lab uses loopback", config: Config{ServiceProfile: ServiceProfileFeat125LocalLab, Port: "18080"}, expected: "127.0.0.1:18080"},
		{name: "FEAT-126 local lab uses loopback", config: Config{ServiceProfile: ServiceProfileFeat126S10LocalLab, Port: "18080"}, expected: "127.0.0.1:18080"},
		{name: "local lab host cannot be supplied through port", config: Config{ServiceProfile: ServiceProfileFeat126S10LocalLab, Port: "0.0.0.0:18080"}, expected: "127.0.0.1:0.0.0.0:18080"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := ServerAddress(test.config); actual != test.expected {
				t.Fatalf("expected server address %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestLoadConfigKeepsPermissionProjectionOffByDefault(t *testing.T) {
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	t.Setenv("YIJIE_API_SERVICE_PROFILE", "")
	t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "")
	t.Setenv("YIJIE_API_LOCAL_CA_SHA256", "")
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load disabled config: %v", err)
	}
	if config.PermissionProjectionEnabled {
		t.Fatal("permission projection must be disabled by default")
	}
	if config.ServiceProfile != ServiceProfileDefault {
		t.Fatalf("expected default service profile, got %q", config.ServiceProfile)
	}
}

func TestLoadConfigRequiresIdentitySettingsOnlyWhenProjectionEnabled(t *testing.T) {
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "")
	t.Setenv("YIJIE_API_LOCAL_CA_SHA256", "")
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

func TestLoadConfigValidatesFeat125LocalLabProfile(t *testing.T) {
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat125LocalLab)
	t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "")
	t.Setenv("YIJIE_API_LOCAL_CA_SHA256", "")
	t.Setenv("YIJIE_ENV", "nonproduction")
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load local-lab profile: %v", err)
	}
	if config.ServiceProfile != ServiceProfileFeat125LocalLab {
		t.Fatalf("unexpected service profile: %#v", config)
	}

	t.Setenv("YIJIE_ENV", "production")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("local-lab profile must reject production environment")
	}

	t.Setenv("YIJIE_ENV", "nonproduction")
	t.Setenv("YIJIE_API_SERVICE_PROFILE", "unknown")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unknown service profile must be rejected")
	}
}

func TestLoadConfigPinsFeat125LocalLabCA(t *testing.T) {
	const validDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name       string
		configure  func(*testing.T)
		wantErr    bool
		wantFields bool
	}{
		{name: "exact local projection", wantFields: true},
		{name: "missing CA path", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "") }, wantErr: true},
		{name: "uppercase pin", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_SHA256", strings.ToUpper(validDigest)) }, wantErr: true},
		{name: "wrong issuer", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_ISSUER", "https://localhost:8443/realms/other") }, wantErr: true},
		{name: "wrong JWKS URL", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat125LocalLabIssuer+"/other") }, wantErr: true},
		{name: "default profile rejects local CA", configure: func(t *testing.T) { t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileDefault) }, wantErr: true},
		{name: "disabled projection rejects unused local CA", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "false") }, wantErr: true},
		{name: "production rejects local profile", configure: func(t *testing.T) { t.Setenv("YIJIE_ENV", "production") }, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
			t.Setenv("YIJIE_ENV", "nonproduction")
			t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat125LocalLab)
			t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
			t.Setenv("YIJIE_API_ACCESS_ISSUER", Feat125LocalLabIssuer)
			t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat125LocalLabJWKSURL)
			t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "/synthetic/local/root.pem")
			t.Setenv("YIJIE_API_LOCAL_CA_SHA256", validDigest)
			if test.configure != nil {
				test.configure(t)
			}

			config, err := LoadConfig()
			if test.wantErr {
				if err == nil {
					t.Fatal("expected configuration to fail closed")
				}
				return
			}
			if err != nil {
				t.Fatalf("load exact local-lab configuration: %v", err)
			}
			if test.wantFields && (config.LocalCAPEMPath == "" || config.LocalCASHA256 != validDigest) {
				t.Fatalf("local CA pin was not retained: %#v", config)
			}
		})
	}
}

func TestLoadConfigPinsFeat125LocalLabProjectionAndLocalCA(t *testing.T) {
	const validPin = "07a3bb2ef51a5b559fe42b423339f6c886c5e17903b1cf2d8ca26bf1b5574650"
	setValid := func(t *testing.T) {
		t.Helper()
		t.Setenv("YIJIE_ENV", "nonproduction")
		t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/yijie")
		t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
		t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat125LocalLab)
		t.Setenv("YIJIE_API_ACCESS_ISSUER", Feat125LocalLabIssuer)
		t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat125LocalLabJWKSURL)
		t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "/tmp/feat-125-local-ca.pem")
		t.Setenv("YIJIE_API_LOCAL_CA_SHA256", validPin)
	}

	t.Run("exact configuration", func(t *testing.T) {
		setValid(t)
		config, err := LoadConfig()
		if err != nil {
			t.Fatalf("load exact local-lab config: %v", err)
		}
		if config.AccessIssuer != Feat125LocalLabIssuer || config.AccessJWKSURL != Feat125LocalLabJWKSURL ||
			config.LocalCAPEMPath != "/tmp/feat-125-local-ca.pem" || config.LocalCASHA256 != validPin {
			t.Fatalf("unexpected local-lab config: %#v", config)
		}
	})

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "issuer drift", key: "YIJIE_API_ACCESS_ISSUER", value: "https://localhost:8443/realms/other"},
		{name: "JWKS drift", key: "YIJIE_API_ACCESS_JWKS_URL", value: "https://localhost:8443/realms/yijie-local/protocol/openid-connect/other"},
		{name: "missing CA path", key: "YIJIE_API_LOCAL_CA_PEM_PATH", value: ""},
		{name: "missing CA pin", key: "YIJIE_API_LOCAL_CA_SHA256", value: ""},
		{name: "uppercase CA pin", key: "YIJIE_API_LOCAL_CA_SHA256", value: strings.ToUpper(validPin)},
		{name: "local CA in default profile", key: "YIJIE_API_SERVICE_PROFILE", value: ServiceProfileDefault},
		{name: "local CA without projection", key: "YIJIE_API_PERMISSION_PROJECTION_ENABLED", value: "false"},
		{name: "local profile in production", key: "YIJIE_ENV", value: "production"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValid(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("expected local-only configuration to fail closed")
			}
		})
	}
}

func TestLoadConfigPinsFeat126S10RuntimeProfile(t *testing.T) {
	const (
		validPin = "07a3bb2ef51a5b559fe42b423339f6c886c5e17903b1cf2d8ca26bf1b5574650"
		validDSN = "postgres://synthetic-user:synthetic-password@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable"
	)
	setValid := func(t *testing.T) {
		t.Helper()
		t.Setenv("YIJIE_ENV", "nonproduction")
		t.Setenv("YIJIE_API_POSTGRES_DSN", validDSN)
		t.Setenv("YIJIE_API_PORT", "18080")
		t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat126S10LocalLab)
		t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "true")
		t.Setenv("YIJIE_API_SECURE_TASKS_ENABLED", "true")
		t.Setenv("YIJIE_API_ACCESS_ISSUER", Feat126S10LocalLabIssuer)
		t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat126S10LocalLabJWKSURL)
		t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "/tmp/feat-126-s10-local-ca.pem")
		t.Setenv("YIJIE_API_LOCAL_CA_SHA256", validPin)
	}

	setValid(t)
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load exact FEAT-126 S10 runtime profile: %v", err)
	}
	if config.ServiceProfile != ServiceProfileFeat126S10LocalLab ||
		config.Environment != "nonproduction" ||
		!config.PermissionProjectionEnabled || !config.SecureTasksEnabled ||
		config.AccessIssuer != Feat126S10LocalLabIssuer ||
		config.AccessJWKSURL != Feat126S10LocalLabJWKSURL ||
		config.LocalCAPEMPath != "/tmp/feat-126-s10-local-ca.pem" ||
		config.LocalCASHA256 != validPin || config.PostgresDSN != validDSN {
		t.Fatal("FEAT-126 S10 runtime profile did not preserve its closed authority")
	}

	tests := []struct {
		name      string
		configure func(*testing.T)
	}{
		{name: "local environment", configure: func(t *testing.T) { t.Setenv("YIJIE_ENV", "local") }},
		{name: "production environment", configure: func(t *testing.T) { t.Setenv("YIJIE_ENV", "production") }},
		{name: "permission projection disabled", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "false") }},
		{name: "permission projection noncanonical true", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "TRUE") }},
		{name: "secure tasks disabled", configure: func(t *testing.T) { t.Setenv("YIJIE_API_SECURE_TASKS_ENABLED", "false") }},
		{name: "secure tasks noncanonical true", configure: func(t *testing.T) { t.Setenv("YIJIE_API_SECURE_TASKS_ENABLED", "1") }},
		{name: "wrong issuer", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_ISSUER", "https://localhost:8443/realms/other") }},
		{name: "issuer whitespace", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_ISSUER", Feat126S10LocalLabIssuer+" ") }},
		{name: "wrong JWKS", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_JWKS_URL", Feat126S10LocalLabIssuer+"/other") }},
		{name: "JWKS whitespace", configure: func(t *testing.T) { t.Setenv("YIJIE_API_ACCESS_JWKS_URL", " "+Feat126S10LocalLabJWKSURL) }},
		{name: "missing CA path", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "") }},
		{name: "CA path whitespace", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "/tmp/feat-126-s10-local-ca.pem ") }},
		{name: "uppercase CA pin", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_SHA256", strings.ToUpper(validPin)) }},
		{name: "CA pin whitespace", configure: func(t *testing.T) { t.Setenv("YIJIE_API_LOCAL_CA_SHA256", validPin+" ") }},
		{name: "FEAT-125 database", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic-user:synthetic-password@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable")
		}},
		{name: "localhost host", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic-user:synthetic-password@localhost:5432/yijie_api_feat126_s10?sslmode=disable")
		}},
		{name: "wrong port", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic-user:synthetic-password@127.0.0.1:5433/yijie_api_feat126_s10?sslmode=disable")
		}},
		{name: "extra query", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", validDSN+"&application_name=api")
		}},
		{name: "DSN whitespace", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", " "+validDSN)
		}},
		{name: "missing username", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://:synthetic-password@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable")
		}},
		{name: "missing password", configure: func(t *testing.T) {
			t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic-user@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable")
		}},
		{name: "host supplied through port", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PORT", "0.0.0.0:18080") }},
		{name: "zero port", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PORT", "0") }},
		{name: "leading-zero port", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PORT", "08080") }},
		{name: "out-of-range port", configure: func(t *testing.T) { t.Setenv("YIJIE_API_PORT", "65536") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValid(t)
			test.configure(t)
			_, err := LoadConfig()
			if err == nil {
				t.Fatal("expected FEAT-126 S10 runtime profile to fail closed")
			}
			if strings.Contains(err.Error(), validDSN) || strings.Contains(err.Error(), "synthetic-password") {
				t.Fatalf("configuration error exposed DSN material: %v", err)
			}
		})
	}
}

func TestFeat125RuntimeProfileSemanticsRemainCompatible(t *testing.T) {
	t.Setenv("YIJIE_ENV", "local")
	t.Setenv("YIJIE_API_POSTGRES_DSN", "postgres://synthetic.invalid/arbitrary_local_database")
	t.Setenv("YIJIE_API_SERVICE_PROFILE", ServiceProfileFeat125LocalLab)
	t.Setenv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", "false")
	t.Setenv("YIJIE_API_SECURE_TASKS_ENABLED", "false")
	t.Setenv("YIJIE_API_ACCESS_ISSUER", "")
	t.Setenv("YIJIE_API_ACCESS_JWKS_URL", "")
	t.Setenv("YIJIE_API_LOCAL_CA_PEM_PATH", "")
	t.Setenv("YIJIE_API_LOCAL_CA_SHA256", "")

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("existing FEAT-125 runtime profile semantics changed: %v", err)
	}
	if config.ServiceProfile != ServiceProfileFeat125LocalLab || config.PermissionProjectionEnabled || config.SecureTasksEnabled {
		t.Fatal("existing FEAT-125 runtime profile semantics changed")
	}
}

func TestClosedLocalLabServiceProfileClassification(t *testing.T) {
	for _, profile := range []string{ServiceProfileFeat125LocalLab, ServiceProfileFeat126S10LocalLab} {
		if !IsClosedLocalLabServiceProfile(profile) {
			t.Fatalf("expected %q to be a closed local-lab profile", profile)
		}
	}
	for _, profile := range []string{"", ServiceProfileDefault, "feat-126-s10-local", "FEAT-126-S10-LOCAL-LAB"} {
		if IsClosedLocalLabServiceProfile(profile) {
			t.Fatalf("unexpected closed local-lab profile %q", profile)
		}
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
