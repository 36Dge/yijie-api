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
