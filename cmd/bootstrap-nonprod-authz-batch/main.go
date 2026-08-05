package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/nonprodbootstrap"
)

type inputPaths []string

func (paths *inputPaths) String() string { return "reviewed manifest paths" }
func (paths *inputPaths) Set(value string) error {
	*paths = append(*paths, value)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bootstrap-nonprod-authz-batch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit bootstrap execution profile")
	var paths inputPaths
	flags.Var(&paths, "input", "reviewed manifest path; repeat exactly four times")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || len(paths) != 4 ||
		*profile != nonprodbootstrap.Feat126S10LocalLabProfile {
		fmt.Fprintln(stderr, "usage: bootstrap-nonprod-authz-batch --profile feat-126-s10-local-lab --input <a> --input <b> --input <c> --input <d>")
		return 2
	}

	environment := getenv("YIJIE_ENV")
	issuer := getenv("YIJIE_API_ACCESS_ISSUER")
	dsn := getenv("YIJIE_API_POSTGRES_DSN")
	if err := nonprodbootstrap.ValidateExecutionProfile(*profile, environment, issuer, dsn); err != nil {
		fmt.Fprintln(stderr, "bootstrap batch execution profile is invalid")
		return 2
	}

	manifests := make([]nonprodbootstrap.Manifest, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, "open bootstrap batch input: failed")
			return 1
		}
		manifest, decodeErr := nonprodbootstrap.DecodeManifest(file)
		closeErr := file.Close()
		if decodeErr != nil || closeErr != nil {
			fmt.Fprintln(stderr, "bootstrap batch input is invalid")
			return 2
		}
		manifests = append(manifests, manifest)
	}
	if err := nonprodbootstrap.ValidateExecutionMatrix(*profile, manifests); err != nil {
		fmt.Fprintln(stderr, "bootstrap batch matrix is invalid")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.RequireCurrent(ctx, dsn); err != nil {
		fmt.Fprintf(stderr, "bootstrap batch requires database migration version %d\n", migrations.LatestVersion)
		return 1
	}
	pool, err := database.Open(ctx, dsn, 0, 2)
	if err != nil {
		fmt.Fprintln(stderr, "open bootstrap batch database: failed")
		return 1
	}
	defer pool.Close()
	if err := database.RequireSchema(ctx, pool); err != nil {
		fmt.Fprintln(stderr, "bootstrap batch database schema is not current")
		return 1
	}

	results, err := nonprodbootstrap.New(pool, environment, issuer).RunClosedMatrix(ctx, manifests)
	if err != nil {
		fmt.Fprintln(stderr, "bootstrap batch failed closed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	for _, result := range results {
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintln(stderr, "encode bootstrap batch result: failed")
			return 1
		}
	}
	return 0
}
