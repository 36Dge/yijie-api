package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsInvalidAuthorityWithoutLeakingDSN(t *testing.T) {
	secretDSN := "postgres://feat126:do-not-log@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable"
	lookup := func(name string) string {
		if name == "YIJIE_API_POSTGRES_DSN" {
			return secretDSN
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--profile", "production", "--run-id", "not-a-run"}, lookup, &stdout, &stderr)
	if exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("invalid authority exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), secretDSN) || strings.Contains(strings.ToLower(stderr.String()), "do-not-log") {
		t.Fatalf("verifier leaked DSN material: %q", stderr.String())
	}
}
