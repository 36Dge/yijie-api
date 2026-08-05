package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/36Dge/yijie-api/internal/platform/nonprodbootstrap"
)

func TestVerificationRejectsGenericAndUnknownStateBeforeDatabase(t *testing.T) {
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": nonprodbootstrap.LocalLabIssuer,
		"YIJIE_API_POSTGRES_DSN":  validFeat126DSN,
	}
	for _, args := range [][]string{
		{"--profile", "", "--expect", nonprodbootstrap.ExecutionStateEmpty},
		{"--profile", nonprodbootstrap.LocalLabProfile, "--expect", nonprodbootstrap.ExecutionStateEmpty},
		{"--profile", nonprodbootstrap.Feat126S10LocalLabProfile, "--expect", "partial"},
	} {
		var stderr bytes.Buffer
		exitCode := run(args, func(key string) string { return environment[key] }, &bytes.Buffer{}, &stderr)
		if exitCode != 2 || !strings.HasPrefix(stderr.String(), "usage: verify-nonprod-authz") {
			t.Fatalf("expected closed usage rejection, args=%q exit=%d stderr=%q", args, exitCode, stderr.String())
		}
	}
}

const validFeat126DSN = "postgres://feat126:do-not-log-this@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable"

func TestVerificationRejectsProfileBeforeDatabaseAndRedactsDSN(t *testing.T) {
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": nonprodbootstrap.LocalLabIssuer,
		"YIJIE_API_POSTGRES_DSN":  "postgres://feat126:do-not-log-this@127.0.0.1:5432/wrong_database?sslmode=disable",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--profile", nonprodbootstrap.Feat126S10LocalLabProfile, "--expect", nonprodbootstrap.ExecutionStateEmpty},
		func(key string) string { return environment[key] },
		&stdout,
		&stderr,
	)
	if exitCode != 2 || stderr.String() != "bootstrap verification execution profile is invalid\n" {
		t.Fatalf("expected early profile rejection, exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "do-not-log-this") || strings.Contains(stderr.String(), "do-not-log-this") {
		t.Fatal("bootstrap verification leaked DSN material")
	}
}
