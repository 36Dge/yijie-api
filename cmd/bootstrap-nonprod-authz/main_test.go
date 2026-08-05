package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/36Dge/yijie-api/internal/platform/nonprodbootstrap"
)

func TestLocalLabProfileFailsBeforeOpeningInputOrDatabase(t *testing.T) {
	secretDSN := "postgres://feat125:do-not-log-this@127.0.0.1:5432/wrong_database?sslmode=disable"
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": nonprodbootstrap.LocalLabIssuer,
		"YIJIE_API_POSTGRES_DSN":  secretDSN,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--profile", nonprodbootstrap.LocalLabProfile, "--input", "definitely-does-not-exist.json"},
		func(key string) string { return environment[key] },
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 2 || stderr.String() != "bootstrap execution profile is invalid\n" {
		t.Fatalf("expected early profile rejection, exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), secretDSN) || strings.Contains(stderr.String(), secretDSN) ||
		strings.Contains(stdout.String(), "do-not-log-this") || strings.Contains(stderr.String(), "do-not-log-this") {
		t.Fatal("bootstrap output leaked DSN material")
	}
}

func TestFeat126S10ProfileFailsBeforeOpeningInputOrDatabase(t *testing.T) {
	secretDSN := "postgres://feat126:do-not-log-this@127.0.0.1:5432/wrong_database?sslmode=disable"
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": nonprodbootstrap.LocalLabIssuer,
		"YIJIE_API_POSTGRES_DSN":  secretDSN,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--profile", nonprodbootstrap.Feat126S10LocalLabProfile, "--input", "definitely-does-not-exist.json"},
		func(key string) string { return environment[key] },
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 2 || stderr.String() != "bootstrap execution profile is invalid\n" {
		t.Fatalf("expected early FEAT-126 profile rejection, exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), secretDSN) || strings.Contains(stderr.String(), secretDSN) ||
		strings.Contains(stdout.String(), "do-not-log-this") || strings.Contains(stderr.String(), "do-not-log-this") {
		t.Fatal("bootstrap output leaked FEAT-126 DSN material")
	}
}

func TestFeat126S10ManifestFailsBeforeDatabaseAccess(t *testing.T) {
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": nonprodbootstrap.LocalLabIssuer,
		"YIJIE_API_POSTGRES_DSN":  "postgres://feat126:do-not-log-this@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable",
	}
	manifest := `{"schema_version":1,"data_classification":"synthetic_only","issuer":"https://localhost:8443/realms/yijie-local","subject":"12500000-0000-4000-8000-000000000003","user_id":"12500000-0000-4000-8000-000000000003","tenant_id":"12500000-0000-4000-8000-100000000001","tenant_name":"Synthetic FEAT-125 Tenant A","role":"tenant_owner","actor_id":"nonprod:feat-125-g3-local"}`
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--profile", nonprodbootstrap.Feat126S10LocalLabProfile, "--input", "-"},
		func(key string) string { return environment[key] },
		strings.NewReader(manifest),
		&bytes.Buffer{},
		&stderr,
	)
	if exitCode != 2 || stderr.String() != "bootstrap input is invalid\n" {
		t.Fatalf("expected manifest rejection before database access, exit=%d stderr=%q", exitCode, stderr.String())
	}
	if strings.Contains(stderr.String(), "do-not-log-this") {
		t.Fatal("bootstrap manifest rejection leaked DSN material")
	}
}

func TestGenericBootstrapRemainsOutsideExplicitProfile(t *testing.T) {
	environment := map[string]string{
		"YIJIE_ENV":               nonprodbootstrap.Environment,
		"YIJIE_API_ACCESS_ISSUER": "https://generic.example.invalid/",
		"YIJIE_API_POSTGRES_DSN":  "postgres://generic:secret@database.example.invalid/generic",
	}
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--input", "definitely-does-not-exist.json"},
		func(key string) string { return environment[key] },
		strings.NewReader(""),
		&bytes.Buffer{},
		&stderr,
	)
	if exitCode != 1 || stderr.String() != "open bootstrap input: failed\n" {
		t.Fatalf("generic profile should reach existing input handling, exit=%d stderr=%q", exitCode, stderr.String())
	}
}
