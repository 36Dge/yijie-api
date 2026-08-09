package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/tasks/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/nonprodbootstrap"
	"github.com/google/uuid"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verify-feat126-s10-e2e", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit FEAT-126 local profile")
	runID := flags.String("run-id", "", "canonical run identifier")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *profile != postgres.FEAT126S10Profile || !canonicalRunID(*runID) {
		fmt.Fprintln(stderr, "usage: verify-feat126-s10-e2e --profile feat-126-s10-local-lab --run-id <canonical-uuid>")
		return 2
	}
	environment := getenv("YIJIE_ENV")
	issuer := getenv("YIJIE_API_ACCESS_ISSUER")
	dsn := getenv("YIJIE_API_POSTGRES_DSN")
	if err := nonprodbootstrap.ValidateExecutionProfile(*profile, environment, issuer, dsn); err != nil {
		fmt.Fprintln(stderr, "e2e verifier execution profile is invalid")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.RequireCurrent(ctx, dsn); err != nil {
		fmt.Fprintln(stderr, "e2e verifier database migration is not current")
		return 1
	}
	pool, err := database.Open(ctx, dsn, 0, 2)
	if err != nil {
		fmt.Fprintln(stderr, "e2e verifier database is unavailable")
		return 1
	}
	defer pool.Close()
	if err := database.RequireSchema(ctx, pool); err != nil {
		fmt.Fprintln(stderr, "e2e verifier database schema is not current")
		return 1
	}
	projection, err := postgres.VerifyE2E(ctx, pool, *profile)
	if err != nil {
		fmt.Fprintln(stderr, "e2e verifier failed closed")
		return 1
	}
	projection.RunID = *runID
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(projection); err != nil {
		fmt.Fprintln(stderr, "e2e verifier output failed")
		return 1
	}
	return 0
}

func canonicalRunID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value && parsed.Version() == 4
}
