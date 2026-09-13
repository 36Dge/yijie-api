// Package editorassets serves a verified deployment artifact, never browser-selected files.
package editorassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"

	"github.com/36Dge/yijie-api/contracts"
)

const manifestLimit = 262144
const assetLimit = 16777216
const bundleLimit = 67108864

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var assetPath = regexp.MustCompile(`^(index\.html|assets/(?:[A-Za-z0-9_-][A-Za-z0-9._-]*/)*[A-Za-z0-9_-][A-Za-z0-9._-]*\.(?:js|css|svg|png|jpg|jpeg|webp|woff|woff2|ttf|ico))$`)
var mediaTypes = map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".ico": "image/x-icon"}

// These private structs implement config/workflow-editor-assets.schema.json.
// They are deployment metadata, not an editable workflow or business wire DTO.
type manifest struct {
	SchemaVersion    int         `json:"schema_version"`
	BasePath         string      `json:"base_path"`
	SourceRepository string      `json:"source_repository"`
	SourceCommit     string      `json:"source_commit"`
	SourceDigest     string      `json:"source_digest"`
	ContractsLockSHA string      `json:"contracts_lock_sha256"`
	Files            []assetFile `json:"files"`
}
type assetFile struct {
	Path        string `json:"path"`
	SHA         string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type"`
}
type asset struct {
	data        []byte
	contentType string
}
type Bundle struct{ assets map[string]asset }

func Load(directory, manifestSHA string) (*Bundle, error) {
	invalid := errors.New("workflow editor bundle is unavailable or invalid")
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !hex64.MatchString(manifestSHA) {
		return nil, invalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, invalid
	}
	defer root.Close()
	raw, err := readRegular(root, "manifest.json", manifestLimit)
	if err != nil || digest(raw) != manifestSHA {
		return nil, invalid
	}
	var meta manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&meta) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, invalid
	}
	if meta.SchemaVersion != 1 || meta.BasePath != "/editor/" || meta.SourceRepository != "yijie-coze" || !hex40.MatchString(meta.SourceCommit) || !hex64.MatchString(meta.SourceDigest) || meta.ContractsLockSHA != digest(contracts.WorkflowSourceLock) || len(meta.Files) < 1 || len(meta.Files) > 512 {
		return nil, invalid
	}
	bundle := &Bundle{assets: make(map[string]asset, len(meta.Files))}
	var total int64
	for _, file := range meta.Files {
		if len(file.Path) > 240 || !assetPath.MatchString(file.Path) || file.Bytes < 1 || file.Bytes > assetLimit || !hex64.MatchString(file.SHA) || mediaTypes[path.Ext(file.Path)] != file.ContentType {
			return nil, invalid
		}
		if _, exists := bundle.assets[file.Path]; exists {
			return nil, invalid
		}
		total += file.Bytes
		if total > bundleLimit {
			return nil, invalid
		}
		data, err := readRegular(root, file.Path, file.Bytes)
		if err != nil || int64(len(data)) != file.Bytes || digest(data) != file.SHA {
			return nil, invalid
		}
		bundle.assets[file.Path] = asset{data: data, contentType: file.ContentType}
	}
	if _, exists := bundle.assets["index.html"]; !exists {
		return nil, invalid
	}
	return bundle, nil
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	invalid := errors.New("editor artifact file is invalid")
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		info, err := root.Lstat(strings.Join(parts[:i], "/"))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, invalid
		}
	}
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, invalid
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !same(before, opened) {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, invalid
	}
	after, err := file.Stat()
	if err != nil || !same(opened, after) || int64(len(data)) != after.Size() {
		return nil, invalid
	}
	current, err := root.Lstat(name)
	if err != nil || !same(after, current) {
		return nil, invalid
	}
	return data, nil
}

func same(a, b os.FileInfo) bool {
	return b.Mode().IsRegular() && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
