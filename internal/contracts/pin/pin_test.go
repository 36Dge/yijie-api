package pin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownFieldsAndUnsafePaths(t *testing.T) {
	tests := []string{
		`{"schema_version":1,"unknown":true}`,
		`{"schema_version":1,"contract_version":"0.3.0-candidate","repository":"https://example.test/contracts.git","full_commit":"9999999999999999999999999999999999999999","source_path":"../public.yaml","source_sha256":"` + strings.Repeat("0", 64) + `","generator":{"module":"example.test/generator","version":"v1.0.0","command":"generator"},"generated_path":"generated.go","generated_sha256":"` + strings.Repeat("1", 64) + `"}`,
	}
	for index, contents := range tests {
		path := filepath.Join(t.TempDir(), "lock.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write test lock: %v", err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("case %d: expected invalid lock", index)
		}
	}
}

func TestValidateRejectsNonHexadecimalFullCommit(t *testing.T) {
	lock := Lock{
		SchemaVersion:   1,
		ContractVersion: "0.3.0-candidate",
		Repository:      "https://example.invalid/contracts.git",
		FullCommit:      strings.Repeat("z", 40),
		SourcePath:      "openapi/public/public.yaml",
		SourceSHA256:    strings.Repeat("0", 64),
		Generator: Generator{
			Module:  "example.invalid/generator",
			Version: "v1.0.0",
			Command: "generator",
		},
		GeneratedPath:   "generated.go",
		GeneratedSHA256: strings.Repeat("1", 64),
	}
	if err := lock.Validate(); err == nil {
		t.Fatal("expected non-hexadecimal full commit to be rejected")
	}
}

func TestVerifyGeneratedChecksDigest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "generated.go")
	if err := os.WriteFile(path, []byte("generated"), 0o600); err != nil {
		t.Fatalf("write generated file: %v", err)
	}
	lock := Lock{GeneratedPath: "generated.go", GeneratedSHA256: "e0cb800a5ccda4cb1b2ad7990de082aaa1e40e771898c0bcb28fcb23c261e422"}
	if err := VerifyGenerated(lock, root); err != nil {
		t.Fatalf("verify matching digest: %v", err)
	}
	lock.GeneratedSHA256 = strings.Repeat("0", 64)
	if err := VerifyGenerated(lock, root); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestVerifySourceRequiresExactCleanCommitAndDigest(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "openapi", "public", "public.yaml")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	source := []byte("openapi: 3.1.0\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Contract Pin Test")
	runGit(t, root, "config", "user.email", "contract-pin@example.invalid")
	runGit(t, root, "config", "commit.gpgSign", "false")
	runGit(t, root, "add", "openapi/public/public.yaml")
	runGit(t, root, "commit", "-m", "test contract source")
	commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	digest := sha256.Sum256(source)
	lock := Lock{
		SchemaVersion:   1,
		ContractVersion: "0.3.0-candidate",
		Repository:      "https://example.invalid/contracts.git",
		FullCommit:      commit,
		SourcePath:      "openapi/public/public.yaml",
		SourceSHA256:    hex.EncodeToString(digest[:]),
		Generator: Generator{
			Module:  "example.invalid/generator",
			Version: "v1.0.0",
			Command: "generator",
		},
		GeneratedPath:   "generated.go",
		GeneratedSHA256: strings.Repeat("0", 64),
	}
	if err := VerifySource(context.Background(), lock, root); err != nil {
		t.Fatalf("verify exact clean contract source: %v", err)
	}

	if err := os.WriteFile(sourcePath, []byte("openapi: 3.0.0\n"), 0o600); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	if err := VerifySource(context.Background(), lock, root); err == nil {
		t.Fatal("expected dirty contract source to be rejected")
	}
	runGit(t, root, "restore", "openapi/public/public.yaml")
	runGit(t, root, "commit", "--allow-empty", "-m", "new contract commit")
	if err := VerifySource(context.Background(), lock, root); err == nil {
		t.Fatal("expected a different contracts HEAD to be rejected")
	}
}

func runGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}
