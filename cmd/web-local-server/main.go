package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/36Dge/yijie-api/internal/app"
	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/ownedcredential"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
)

func run() error {
	config, err := app.LoadLocalWebConfig()
	if err != nil {
		return err
	}
	raw, err := ownedcredential.ReadFile(os.Getenv("YIJIE_WEB_LOCAL_DSN_FILE"), 4096)
	if err != nil {
		return err
	}
	dsn := strings.TrimSpace(string(raw))
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" || u.Host != "127.0.0.1:5432" || u.Path != "/yijie_api" {
		return errors.New("local web database unavailable")
	}
	startup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(startup, dsn, 1, 5)
	if err != nil {
		return errors.New("local web database unavailable")
	}
	defer pool.Close()
	if err = migrations.RequireCurrent(startup, dsn); err != nil {
		return err
	}
	handler, internal, credential, err := app.NewLocalBuiltinWeb(startup, config, pool)
	if err != nil {
		return err
	}
	tlsConfig, err := webwire.ManagedIdentity(config.CertificatePath, config.KeyPath, config.CAPath, webwire.ExecutorIdentity, false)
	if err != nil {
		return err
	}
	client := webwire.ManagedClient(tlsConfig)
	defer client.CloseIdleConnections()
	stop, signalStop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer signalStop()
	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if pool.Ping(r.Context()) != nil {
			w.WriteHeader(503)
			return
		}
		check, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		request, _ := http.NewRequestWithContext(check, "GET", config.ExecutorOrigin+"/readyz", nil)
		response, e := client.Do(request)
		if e != nil {
			w.WriteHeader(503)
			return
		}
		response.Body.Close()
		if response.StatusCode != 204 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ready", "run_epoch": credential.RunEpoch, "audience": "builtin-web-retrieval/1"})
	})
	mux.HandleFunc("POST /internal/local/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get(wire.LocalEpochHeader) != credential.RunEpoch || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+credential.Token)) != 1 {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(202)
		signalStop()
	})
	server := &http.Server{Addr: strings.TrimPrefix(wire.LocalAPIOrigin, "http://"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 55 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 2)
	go func() { done <- server.ListenAndServe() }()
	go func() { done <- internal.ListenAndServeTLS("", "") }()
	select {
	case <-stop.Done():
	case err = <-done:
	}
	// Owned normal shutdown; never force Close or abandon child/service state.
	_ = server.Shutdown(context.Background())
	_ = internal.Shutdown(context.Background())
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if run() != nil {
		slog.Error("local web service unavailable or requires explicit migration")
		os.Exit(1)
	}
}
