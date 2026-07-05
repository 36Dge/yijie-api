package app

import (
	"encoding/json"
	"net/http"
	"os"
)

const ServiceName = "yijie-api"

type Config struct {
	Environment string `json:"environment"`
	Port        string `json:"port"`
	PostgresDSN string `json:"postgres_dsn,omitempty"`
	RedisURL    string `json:"redis_url,omitempty"`
}

type Status struct {
	Service     string `json:"service"`
	Status      string `json:"status"`
	Environment string `json:"environment"`
	Database    string `json:"database"`
	Cache       string `json:"cache"`
}

func LoadConfig() Config {
	return Config{
		Environment: env("YIJIE_ENV", "local"),
		Port:        env("YIJIE_API_PORT", "8080"),
		PostgresDSN: os.Getenv("YIJIE_API_POSTGRES_DSN"),
		RedisURL:    os.Getenv("YIJIE_API_REDIS_URL"),
	}
}

func NewHandler(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", jsonHandler(Status{
		Service:     ServiceName,
		Status:      "ok",
		Environment: config.Environment,
		Database:    "postgresql-placeholder",
		Cache:       "redis-placeholder",
	}))
	mux.HandleFunc("/readyz", jsonHandler(map[string]string{"status": "ready"}))
	mux.HandleFunc("/v1/status", jsonHandler(Status{
		Service:     ServiceName,
		Status:      "ok",
		Environment: config.Environment,
		Database:    "postgresql-placeholder",
		Cache:       "redis-placeholder",
	}))
	return mux
}

func jsonHandler(payload any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
