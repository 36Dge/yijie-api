package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/36Dge/yijie-api/internal/app"
	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	taskpostgres "github.com/36Dge/yijie-api/internal/modules/tasks/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := app.LoadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(
		startupContext,
		config.PostgresDSN,
		config.DatabaseMinConns,
		config.DatabaseMaxConns,
	)
	if err == nil {
		err = migrations.RequireCurrent(startupContext, config.PostgresDSN)
	}
	if err == nil {
		err = database.RequireSchema(startupContext, pool)
	}
	cancelStartup()
	if err != nil {
		logger.Error("database startup check failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	taskService := application.New(taskpostgres.New(pool))
	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           app.NewHandler(config, pool, taskService),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("starting yijie-api", "addr", server.Addr, "environment", config.Environment)
		serverErrors <- server.ListenAndServe()
	}()

	stopContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-stopContext.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
		return
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
}
