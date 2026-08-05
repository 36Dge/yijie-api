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

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verify-nonprod-authz", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit bootstrap execution profile")
	expectedState := flags.String("expect", "", "expected closed synthetic state")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		*profile != nonprodbootstrap.Feat126S10LocalLabProfile ||
		(*expectedState != nonprodbootstrap.ExecutionStateEmpty && *expectedState != nonprodbootstrap.ExecutionStateComplete) {
		fmt.Fprintln(stderr, "usage: verify-nonprod-authz --profile feat-126-s10-local-lab --expect empty|complete")
		return 2
	}

	environment := getenv("YIJIE_ENV")
	expectedIssuer := getenv("YIJIE_API_ACCESS_ISSUER")
	dsn := getenv("YIJIE_API_POSTGRES_DSN")
	if err := nonprodbootstrap.ValidateExecutionProfile(*profile, environment, expectedIssuer, dsn); err != nil {
		fmt.Fprintln(stderr, "bootstrap verification execution profile is invalid")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.RequireCurrent(ctx, dsn); err != nil {
		fmt.Fprintf(stderr, "bootstrap verification requires database migration version %d\n", migrations.LatestVersion)
		return 1
	}
	pool, err := database.Open(ctx, dsn, 0, 2)
	if err != nil {
		fmt.Fprintln(stderr, "open bootstrap verification database: failed")
		return 1
	}
	defer pool.Close()
	if err := database.RequireSchema(ctx, pool); err != nil {
		fmt.Fprintln(stderr, "bootstrap verification database schema is not current")
		return 1
	}

	result, err := nonprodbootstrap.VerifyExecutionState(ctx, pool, *profile, *expectedState)
	if err != nil {
		fmt.Fprintln(stderr, "bootstrap verification state is invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "encode bootstrap verification result: failed")
		return 1
	}
	return 0
}
