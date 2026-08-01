package pin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Generator struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Command string `json:"command"`
}

type Lock struct {
	SchemaVersion   int       `json:"schema_version"`
	ContractVersion string    `json:"contract_version"`
	Repository      string    `json:"repository"`
	FullCommit      string    `json:"full_commit"`
	SourcePath      string    `json:"source_path"`
	SourceSHA256    string    `json:"source_sha256"`
	Generator       Generator `json:"generator"`
	GeneratedPath   string    `json:"generated_path"`
	GeneratedSHA256 string    `json:"generated_sha256"`
}

func Load(path string) (Lock, error) {
	file, err := os.Open(path)
	if err != nil {
		return Lock{}, fmt.Errorf("open contract lock: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var lock Lock
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, fmt.Errorf("decode contract lock: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Lock{}, err
	}
	if err := lock.Validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

func (lock Lock) Validate() error {
	if lock.SchemaVersion != 1 || lock.ContractVersion == "" || lock.Repository == "" ||
		len(lock.FullCommit) != 40 || lock.Generator.Module == "" || lock.Generator.Version == "" ||
		lock.Generator.Command == "" {
		return errors.New("contract lock metadata is incomplete")
	}
	commit, err := hex.DecodeString(lock.FullCommit)
	if err != nil || len(commit) != 20 {
		return errors.New("contract lock full commit is not a 40-character hexadecimal SHA")
	}
	for _, digest := range []string{lock.SourceSHA256, lock.GeneratedSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("contract lock SHA-256 digest is invalid")
		}
	}
	if _, err := safeRelativePath(lock.SourcePath); err != nil {
		return fmt.Errorf("invalid contract source path: %w", err)
	}
	if _, err := safeRelativePath(lock.GeneratedPath); err != nil {
		return fmt.Errorf("invalid generated path: %w", err)
	}
	return nil
}

func VerifySource(ctx context.Context, lock Lock, contractsRoot string) error {
	head, err := gitOutput(ctx, contractsRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != lock.FullCommit {
		return fmt.Errorf("contracts HEAD is %s, expected %s", head, lock.FullCommit)
	}
	status, err := gitOutput(ctx, contractsRoot, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("contracts checkout has tracked changes")
	}
	sourcePath, err := rootedPath(contractsRoot, lock.SourcePath)
	if err != nil {
		return err
	}
	return verifyDigest(sourcePath, lock.SourceSHA256)
}

func VerifyGenerated(lock Lock, repositoryRoot string) error {
	generatedPath, err := rootedPath(repositoryRoot, lock.GeneratedPath)
	if err != nil {
		return err
	}
	return verifyDigest(generatedPath, lock.GeneratedSHA256)
}

func VerifyGenerator(ctx context.Context, lock Lock, repositoryRoot string) error {
	command := exec.CommandContext(ctx, "go", "list", "-m", "-f={{.Version}}", lock.Generator.Module)
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect generator module: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if version != lock.Generator.Version {
		return fmt.Errorf("generator version is %s, expected %s", version, lock.Generator.Version)
	}
	return nil
}

func verifyDigest(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open pinned file: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash pinned file: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("SHA-256 for %s is %s, expected %s", path, actual, expected)
	}
	return nil
}

func gitOutput(ctx context.Context, root string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func rootedPath(root, relative string) (string, error) {
	clean, err := safeRelativePath(relative)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, clean), nil
}

func safeRelativePath(path string) (string, error) {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay within its repository")
	}
	return clean, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("contract lock contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing contract lock data: %w", err)
	}
	return nil
}
