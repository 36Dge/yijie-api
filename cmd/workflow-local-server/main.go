package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/workflows/application"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/infrastructure/coze"
	workflowpostgres "github.com/36Dge/yijie-api/internal/modules/workflows/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/modules/workflows/interfaces/editorassets"
	workflowhttp "github.com/36Dge/yijie-api/internal/modules/workflows/interfaces/http"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/workflowlocal"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--check-ready" {
		if checkReady() != nil {
			fmt.Fprintln(os.Stderr, "workflow readiness unavailable")
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: workflow-local-server [--check-ready]")
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "workflow local server did not complete normally")
		os.Exit(1)
	}
}
func run() error {
	config, err := workflowlocal.LoadConfig()
	if err != nil {
		return err
	}
	var bundle *editorassets.Bundle
	if config.Editor.Enabled {
		bundle, err = editorassets.Load(config.Editor.Directory, config.Editor.ManifestSHA)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(ctx, config.DSN, 1, 5)
	if err != nil {
		cancel()
		return errors.New("workflow database unavailable")
	}
	defer pool.Close()
	repo := workflowpostgres.New(pool)
	err = repo.RequireCurrent(ctx)
	cancel()
	if err != nil {
		return err
	}
	engine := coze.New(config.CozeURL, config.Coze.Token, config.Coze.RunEpoch)
	defer engine.Close()
	service := application.New(repo, engine, config.Native.RunEpoch)
	principalContext, cancelPrincipal := context.WithTimeout(context.Background(), 20*time.Second)
	_, err = service.Execute(principalContext, domain.Request{Action: "principal"})
	cancelPrincipal()
	if err != nil {
		return errors.New("workflow principal initialization unavailable")
	}
	handler := workflowhttp.New(service, config.Native.Token, config.Native.RunEpoch, nil)
	defer handler.Close()
	server := &http.Server{Addr: config.Listen, Handler: editorassets.Gateway(handler.Routes(), bundle), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("workflow listener failed")
		}
		return nil
	case <-stopping.Done():
	}
	handler.Close()
	// No forced Close/kill fallback. A pending normal shutdown remains observable.
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(context.Background()) }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case err := <-shutdownDone:
		return err
	case <-timer.C:
		fmt.Fprintln(os.Stderr, "workflow shutdown pending; waiting for normal completion")
	}
	return <-shutdownDone
}
