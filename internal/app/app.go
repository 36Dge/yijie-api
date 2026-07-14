package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/36Dge/yijie-api/internal/interfaces/publicapi"
	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	taskhttp "github.com/36Dge/yijie-api/internal/modules/tasks/interfaces/http"
)

const ServiceName = "yijie-api"

type Config struct {
	Environment      string
	Port             string
	PostgresDSN      string
	RedisURL         string
	DatabaseMinConns int32
	DatabaseMaxConns int32
}

type Database interface {
	Ping(context.Context) error
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

	config := Config{
		Environment:      env("YIJIE_ENV", "local"),
		Port:             env("YIJIE_API_PORT", "8080"),
		PostgresDSN:      strings.TrimSpace(os.Getenv("YIJIE_API_POSTGRES_DSN")),
		RedisURL:         strings.TrimSpace(os.Getenv("YIJIE_API_REDIS_URL")),
		DatabaseMinConns: minConns,
		DatabaseMaxConns: maxConns,
	}
	if config.PostgresDSN == "" {
		return Config{}, errors.New("YIJIE_API_POSTGRES_DSN is required")
	}
	return config, nil
}

func NewHandler(config Config, database Database, tasks *application.Service) http.Handler {
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

	taskHandler := taskhttp.New(tasks)
	mux.HandleFunc("POST /v1/tasks", taskHandler.Create)
	mux.HandleFunc("GET /v1/tasks/{task_id}", taskHandler.Get)
	return mux
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
