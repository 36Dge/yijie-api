package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/36Dge/yijie-api/internal/interfaces/publicapi"
	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	taskhttp "github.com/36Dge/yijie-api/internal/modules/tasks/interfaces/http"
)

const (
	ServiceName                   = "yijie-api"
	ServiceProfileDefault         = "default"
	ServiceProfileFeat125LocalLab = "feat-125-local-lab"
	Feat125LocalLabIssuer         = "https://localhost:8443/realms/yijie-local"
	Feat125LocalLabJWKSURL        = "https://localhost:8443/realms/yijie-local/protocol/openid-connect/certs"
)

type Config struct {
	Environment                 string
	ServiceProfile              string
	Port                        string
	PostgresDSN                 string
	RedisURL                    string
	DatabaseMinConns            int32
	DatabaseMaxConns            int32
	PermissionProjectionEnabled bool
	SecureTasksEnabled          bool
	AccessIssuer                string
	AccessJWKSURL               string
	LocalCAPEMPath              string
	LocalCASHA256               string
}

type Database interface {
	Ping(context.Context) error
}

type AccessEndpoints interface {
	ListMyTenants(http.ResponseWriter, *http.Request)
	GetMyCapabilities(http.ResponseWriter, *http.Request)
}

type SecureTaskEndpoints interface {
	CreateTask(http.ResponseWriter, *http.Request)
	GetTask(http.ResponseWriter, *http.Request)
}

type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	access      AccessEndpoints
	secureTasks SecureTaskEndpoints
}

func WithSecureTaskEndpoints(tasks SecureTaskEndpoints) HandlerOption {
	return func(options *handlerOptions) {
		options.secureTasks = tasks
	}
}

func WithAccessEndpoints(access AccessEndpoints) HandlerOption {
	return func(options *handlerOptions) {
		options.access = access
	}
}

func LoadConfig() (Config, error) {
	minConns, err := int32Env("YIJIE_API_DB_MIN_CONNS", 1)
	if err != nil {
		return Config{}, err
	}
	maxConns, err := int32Env("YIJIE_API_DB_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	if minConns < 0 || maxConns < 1 || minConns > maxConns {
		return Config{}, errors.New("database connection limits are invalid")
	}
	projectionEnabled, err := boolEnv("YIJIE_API_PERMISSION_PROJECTION_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	secureTasksEnabled, err := boolEnv("YIJIE_API_SECURE_TASKS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}

	environment := env("YIJIE_ENV", "local")
	serviceProfile := env("YIJIE_API_SERVICE_PROFILE", ServiceProfileDefault)
	if serviceProfile != ServiceProfileDefault && serviceProfile != ServiceProfileFeat125LocalLab {
		return Config{}, errors.New("YIJIE_API_SERVICE_PROFILE is invalid")
	}
	if serviceProfile == ServiceProfileFeat125LocalLab && environment != "local" && environment != "nonproduction" {
		return Config{}, errors.New("feat-125-local-lab service profile requires a local or nonproduction environment")
	}

	config := Config{
		Environment:                 environment,
		ServiceProfile:              serviceProfile,
		Port:                        env("YIJIE_API_PORT", "8080"),
		PostgresDSN:                 strings.TrimSpace(os.Getenv("YIJIE_API_POSTGRES_DSN")),
		RedisURL:                    strings.TrimSpace(os.Getenv("YIJIE_API_REDIS_URL")),
		DatabaseMinConns:            minConns,
		DatabaseMaxConns:            maxConns,
		PermissionProjectionEnabled: projectionEnabled,
		SecureTasksEnabled:          secureTasksEnabled,
		AccessIssuer:                strings.TrimSpace(os.Getenv("YIJIE_API_ACCESS_ISSUER")),
		AccessJWKSURL:               strings.TrimSpace(os.Getenv("YIJIE_API_ACCESS_JWKS_URL")),
		LocalCAPEMPath:              strings.TrimSpace(os.Getenv("YIJIE_API_LOCAL_CA_PEM_PATH")),
		LocalCASHA256:               strings.TrimSpace(os.Getenv("YIJIE_API_LOCAL_CA_SHA256")),
	}
	if config.PostgresDSN == "" {
		return Config{}, errors.New("YIJIE_API_POSTGRES_DSN is required")
	}
	if config.PermissionProjectionEnabled && (config.AccessIssuer == "" || config.AccessJWKSURL == "") {
		return Config{}, errors.New("enabled permission projection requires YIJIE_API_ACCESS_ISSUER and YIJIE_API_ACCESS_JWKS_URL")
	}
	if config.SecureTasksEnabled && (!config.PermissionProjectionEnabled || config.ServiceProfile != ServiceProfileFeat125LocalLab) {
		return Config{}, errors.New("secure tasks require enabled permission projection in the feat-125-local-lab service profile")
	}
	localCAConfigured := config.LocalCAPEMPath != "" || config.LocalCASHA256 != ""
	if serviceProfile != ServiceProfileFeat125LocalLab && localCAConfigured {
		return Config{}, errors.New("local CA configuration requires the feat-125-local-lab service profile")
	}
	if serviceProfile == ServiceProfileFeat125LocalLab && config.PermissionProjectionEnabled {
		if config.AccessIssuer != Feat125LocalLabIssuer || config.AccessJWKSURL != Feat125LocalLabJWKSURL {
			return Config{}, errors.New("feat-125-local-lab permission projection requires the exact local issuer and JWKS URL")
		}
		if config.LocalCAPEMPath == "" || !isLowerHexSHA256(config.LocalCASHA256) {
			return Config{}, errors.New("feat-125-local-lab permission projection requires a local CA PEM path and lowercase SHA-256 pin")
		}
	} else if localCAConfigured {
		return Config{}, errors.New("local CA configuration requires enabled feat-125-local-lab permission projection")
	}
	return config, nil
}

func NewHandler(config Config, database Database, tasks *application.Service, options ...HandlerOption) http.Handler {
	settings := handlerOptions{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		status := serviceStatus(r.Context(), config, database)
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if database == nil || database.Ping(ctx) != nil {
			writeJSON(w, http.StatusServiceUnavailable, publicapi.Readiness{Status: publicapi.NotReady})
			return
		}
		writeJSON(w, http.StatusOK, publicapi.Readiness{Status: publicapi.Ready})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, serviceStatus(r.Context(), config, database))
	})
	if settings.access != nil {
		mux.HandleFunc("GET /v1/me/tenants", settings.access.ListMyTenants)
		mux.HandleFunc("GET /v1/me/capabilities", settings.access.GetMyCapabilities)
	}
	if config.SecureTasksEnabled && settings.secureTasks != nil {
		mux.HandleFunc("POST /v2/tasks", settings.secureTasks.CreateTask)
		mux.HandleFunc("GET /v2/tasks/{task_id}", settings.secureTasks.GetTask)
	}

	if config.ServiceProfile != ServiceProfileFeat125LocalLab {
		taskHandler := taskhttp.New(tasks)
		mux.HandleFunc("POST /v1/tasks", taskHandler.Create)
		mux.HandleFunc("GET /v1/tasks/{task_id}", taskHandler.Get)
	}
	return mux
}

func ServerAddress(config Config) string {
	if config.ServiceProfile == ServiceProfileFeat125LocalLab {
		return net.JoinHostPort("127.0.0.1", config.Port)
	}
	return ":" + config.Port
}

func serviceStatus(ctx context.Context, config Config, database Database) publicapi.ServiceStatus {
	status := publicapi.Ok
	databaseStatus := "connected"
	checkContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if database == nil || database.Ping(checkContext) != nil {
		status = publicapi.Degraded
		databaseStatus = "unavailable"
	}

	cacheStatus := "not_configured"
	if config.RedisURL != "" {
		cacheStatus = "configured"
	}
	return publicapi.ServiceStatus{
		Service:     publicapi.YijieApi,
		Status:      status,
		Environment: config.Environment,
		Database:    databaseStatus,
		Cache:       cacheStatus,
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func int32Env(key string, fallback int32) (int32, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return int32(parsed), nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
