// Package ownedcredential reads the existing Native-owned local service credential.
package ownedcredential

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var hexToken = regexp.MustCompile(`^[0-9a-f]{64}$`)
var epochPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Credential struct {
	SchemaVersion int    `json:"schema_version"`
	RunEpoch      string `json:"run_epoch"`
	Token         string `json:"token"`
}

func (Credential) String() string     { return "NativeCredential([redacted])" }
func (c Credential) GoString() string { return c.String() }
func Read(path string) (Credential, error) {
	var result Credential
	invalid := errors.New("local service credential file is invalid")
	data, err := ReadFile(path, 1024)
	if err != nil {
		return result, invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.SchemaVersion != 1 || !epochPattern.MatchString(result.RunEpoch) || !hexToken.MatchString(result.Token) {
		return Credential{}, invalid
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Credential{}, invalid
	}
	return result, nil
}

func ReadFile(path string, limit int64) ([]byte, error) {
	invalid := errors.New("local service private file is invalid")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, invalid
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 && before.Mode().Perm() != 0400 || before.Size() < 1 || before.Size() > limit {
		return nil, invalid
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || owner.Nlink != 1 {
		return nil, invalid
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, invalid
	}
	f := os.NewFile(uintptr(fd), "workflow-credential")
	if f == nil {
		_ = syscall.Close(fd)
		return nil, invalid
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !stableCredentialInfo(before, after) {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, invalid
	}
	readInfo, err := f.Stat()
	if err != nil || !stableCredentialInfo(after, readInfo) || int64(len(data)) != readInfo.Size() {
		return nil, invalid
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !stableCredentialInfo(readInfo, pathInfo) {
		return nil, invalid
	}
	return data, nil
}

func stableCredentialInfo(a, b os.FileInfo) bool {
	if !b.Mode().IsRegular() || !os.SameFile(a, b) || a.Size() != b.Size() || a.Mode() != b.Mode() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	return xok && yok && y.Nlink == 1 && int(y.Uid) == os.Geteuid() && x.Uid == y.Uid && x.Gid == y.Gid
}
