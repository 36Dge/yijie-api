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
	"github.com/36Dge/yijie-api/internal/interfaces/publicapi"
	authorizationapplication "github.com/36Dge/yijie-api/internal/modules/authorization/application"
	authorizationpostgres "github.com/36Dge/yijie-api/internal/modules/authorization/infrastructure/postgres"
	identityapplication "github.com/36Dge/yijie-api/internal/modules/identity/application"
	identitypostgres "github.com/36Dge/yijie-api/internal/modules/identity/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	taskpostgres "github.com/36Dge/yijie-api/internal/modules/tasks/infrastructure/postgres"
	tenancyapplication "github.com/36Dge/yijie-api/internal/modules/tenancy/application"
	tenancypostgres "github.com/36Dge/yijie-api/internal/modules/tenancy/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/platform/authn"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/observability"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := app.LoadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	var accessHTTPClient *http.Client
	if config.PermissionProjectionEnabled && config.ServiceProfile == app.ServiceProfileFeat125LocalLab {
		accessHTTPClient, err = authn.NewLocalLabPinnedCAHTTPClient(config.LocalCAPEMPath, config.LocalCASHA256)
		if err != nil {
			logger.Error("invalid local CA configuration", "error", err)
			os.Exit(1)
		}
		defer accessHTTPClient.CloseIdleConnections()
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
	var accessHandler *publicapi.AccessHandler
	var secureTaskHandler *publicapi.SecureTaskHandler
	var accessKeys *authn.RemoteKeySet
	var cancelAccessKeys context.CancelFunc
	if err == nil && config.PermissionProjectionEnabled {
		accessKeysContext, cancel := context.WithCancel(context.Background())
		cancelAccessKeys = cancel
		accessKeys, err = authn.NewRemoteKeySet(accessKeysContext, config.AccessJWKSURL, accessHTTPClient)
		if err == nil {
			var verifier *authn.Verifier
			verifier, err = authn.NewVerifier(config.AccessIssuer, accessKeys)
			if err == nil {
				identityService := identityapplication.New(identitypostgres.New(pool))
				tenancyService := tenancyapplication.New(tenancypostgres.New(pool))
				authorizationService := authorizationapplication.New(authorizationpostgres.New(pool))
				accessHandler = publicapi.NewAccessHandler(
					verifier,
					identityService,
					tenancyService,
					authorizationService,
					observability.NewAccessMetrics(),
				)
				if config.SecureTasksEnabled {
					secureTaskHandler = publicapi.NewSecureTaskHandler(
						accessHandler,
						tenancyService,
						authorizationService,
						application.NewSecure(taskpostgres.New(pool)),
					)
				}
			}
		}
		if err != nil {
			cancelAccessKeys()
			cancelAccessKeys = nil
		}
	}
	cancelStartup()
	if err != nil {
		if accessKeys != nil {
			closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
			_ = accessKeys.Close(closeContext)
			cancelClose()
		}
		if cancelAccessKeys != nil {
			cancelAccessKeys()
		}
		if pool != nil {
			pool.Close()
		}
		logger.Error("startup check failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if accessKeys != nil {
		defer func() {
			closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
			defer cancelClose()
			if err := accessKeys.Close(closeContext); err != nil {
				logger.Error("close access verification keys", "error", err)
			}
			if cancelAccessKeys != nil {
				cancelAccessKeys()
			}
		}()
	}

	taskService := application.New(taskpostgres.New(pool))
	handlerOptions := make([]app.HandlerOption, 0, 1)
	if accessHandler != nil {
		handlerOptions = append(handlerOptions, app.WithAccessEndpoints(accessHandler))
	}
	if secureTaskHandler != nil {
		handlerOptions = append(handlerOptions, app.WithSecureTaskEndpoints(secureTaskHandler))
	}
	server := &http.Server{
		Addr:              app.ServerAddress(config),
		Handler:           app.NewHandler(config, pool, taskService, handlerOptions...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"starting yijie-api",
			"addr", server.Addr,
			"environment", config.Environment,
			"service_profile", config.ServiceProfile,
		)
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
