package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBatchRejectsAnythingExceptExactFeat126ShapeBeforeStorage(t *testing.T) {
	getenv := func(key string) string {
		values := map[string]string{
			"YIJIE_ENV":               "nonproduction",
			"YIJIE_API_ACCESS_ISSUER": "https://localhost:8443/realms/yijie-local",
			"YIJIE_API_POSTGRES_DSN":  "postgres://user:secret@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable",
		}
		return values[key]
	}
	for _, args := range [][]string{
		{"--profile", "feat-125-local-lab"},
		{"--profile", "feat-126-s10-local-lab", "--input", "one"},
		{"--profile", "feat-126-s10-local-lab", "--input", "one", "--input", "two", "--input", "three", "--input", "four", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, getenv, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("expected usage failure before storage: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

func TestBatchRejectsWrongDatabaseBeforeOpeningInputs(t *testing.T) {
	getenv := func(key string) string {
		values := map[string]string{
			"YIJIE_ENV":               "nonproduction",
			"YIJIE_API_ACCESS_ISSUER": "https://localhost:8443/realms/yijie-local",
			"YIJIE_API_POSTGRES_DSN":  "postgres://user:do-not-print@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable",
		}
		return values[key]
	}
	args := []string{"--profile", "feat-126-s10-local-lab", "--input", "missing-a", "--input", "missing-b", "--input", "missing-c", "--input", "missing-d"}
	var stdout, stderr bytes.Buffer
	if code := run(args, getenv, &stdout, &stderr); code != 2 {
		t.Fatalf("expected profile failure, got code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "do-not-print") || strings.Contains(stderr.String(), "missing-a") {
		t.Fatalf("profile failure leaked DSN or input: %q", stderr.String())
	}
}
