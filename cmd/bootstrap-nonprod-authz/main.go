package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/nonprodbootstrap"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bootstrap-nonprod-authz", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "path to a synthetic nonproduction bootstrap manifest, or - for stdin")
	profile := flags.String("profile", "", "explicit bootstrap execution profile")
	if err := flags.Parse(args); err != nil || *inputPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: bootstrap-nonprod-authz --input <manifest.json|-> [--profile feat-125-local-lab|feat-126-s10-local-lab]")
		return 2
	}
	environment := getenv("YIJIE_ENV")
	if environment != nonprodbootstrap.Environment {
		fmt.Fprintln(stderr, "bootstrap requires YIJIE_ENV=nonproduction")
		return 2
	}
	expectedIssuer := getenv("YIJIE_API_ACCESS_ISSUER")
	if expectedIssuer == "" {
		fmt.Fprintln(stderr, "YIJIE_API_ACCESS_ISSUER is required")
		return 2
	}
	dsn := getenv("YIJIE_API_POSTGRES_DSN")
	if dsn == "" {
		fmt.Fprintln(stderr, "YIJIE_API_POSTGRES_DSN is required")
		return 2
	}
	if err := nonprodbootstrap.ValidateExecutionProfile(*profile, environment, expectedIssuer, dsn); err != nil {
		fmt.Fprintln(stderr, "bootstrap execution profile is invalid")
		return 2
	}

	reader, closeInput, err := openInput(*inputPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "open bootstrap input: failed")
		return 1
	}
	defer closeInput()
	manifest, err := nonprodbootstrap.DecodeManifest(reader)
	if err != nil || manifest.Validate(environment, expectedIssuer) != nil ||
		nonprodbootstrap.ValidateExecutionManifest(*profile, manifest) != nil {
		fmt.Fprintln(stderr, "bootstrap input is invalid")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.RequireCurrent(ctx, dsn); err != nil {
		fmt.Fprintf(stderr, "bootstrap requires database migration version %d\n", migrations.LatestVersion)
		return 1
	}
	pool, err := database.Open(ctx, dsn, 0, 2)
	if err != nil {
		fmt.Fprintln(stderr, "open bootstrap database: failed")
		return 1
	}
	defer pool.Close()
	if err := database.RequireSchema(ctx, pool); err != nil {
		fmt.Fprintln(stderr, "bootstrap database schema is not current")
		return 1
	}

	result, err := nonprodbootstrap.New(pool, environment, expectedIssuer).Run(ctx, manifest)
	if err != nil {
		if errors.Is(err, nonprodbootstrap.ErrOutcomeUnknown) {
			fmt.Fprintf(stderr, "bootstrap commit outcome is unknown; reconcile by request_id=%s before retrying\n", result.RequestID)
		} else if errors.Is(err, nonprodbootstrap.ErrAuditUnavailable) {
			fmt.Fprintf(stderr, "bootstrap failed closed and failure audit is unavailable; request_id=%s\n", result.RequestID)
		} else if errors.Is(err, nonprodbootstrap.ErrConflict) {
			fmt.Fprintf(stderr, "bootstrap rejected existing state conflict; request_id=%s\n", result.RequestID)
		} else {
			fmt.Fprintf(stderr, "bootstrap failed closed; request_id=%s\n", result.RequestID)
		}
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "encode bootstrap result: failed")
		return 1
	}
	return 0
}

func openInput(path string, stdin io.Reader) (io.Reader, func(), error) {
	if path == "-" {
		return stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { _ = file.Close() }, nil
}
