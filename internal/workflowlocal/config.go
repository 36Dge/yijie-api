package workflowlocal

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const Profile = "feat-153-workflow-local"

var hexToken = regexp.MustCompile(`^[0-9a-f]{64}$`)
var epochPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Credential struct {
	SchemaVersion int    `json:"schema_version"`
	RunEpoch      string `json:"run_epoch"`
	Token         string `json:"token"`
}
type Config struct {
	Native, Coze         Credential
	CozeURL, DSN, Listen string
	Editor               EditorConfig
}

type EditorConfig struct {
	Enabled                bool
	Directory, ManifestSHA string
}

func ReadCredential(path string) (Credential, error) {
	var result Credential
	invalid := errors.New("workflow credential file is invalid")
	data, err := readOwnerFile(path, 1024)
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

func readOwnerFile(path string, limit int64) ([]byte, error) {
	invalid := errors.New("workflow private file is invalid")
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

func LoadConfig() (Config, error) { return ConfigFrom(os.Getenv) }
func ConfigFrom(lookup func(string) string) (Config, error) {
	invalid := errors.New("workflow local configuration is invalid")
	if lookup("YIJIE_ENV") != "local" || lookup("YIJIE_LOCAL_PROFILE") != "demo_fast" || lookup("YIJIE_WORKFLOW_ENABLED") != "true" || lookup("YIJIE_API_SERVICE_PROFILE") != Profile {
		return Config{}, invalid
	}
	native, err := ReadCredential(lookup("YIJIE_WORKFLOW_CREDENTIAL_FILE"))
	if err != nil {
		return Config{}, err
	}
	coze, err := ReadCredential(lookup("YIJIE_WORKFLOW_COZE_CREDENTIAL_FILE"))
	if err != nil {
		return Config{}, err
	}
	if native.RunEpoch != coze.RunEpoch || subtle.ConstantTimeCompare([]byte(native.Token), []byte(coze.Token)) == 1 {
		return Config{}, invalid
	}
	cozeURL := lookup("YIJIE_WORKFLOW_COZE_URL")
	if cozeURL == "" {
		cozeURL = "http://coze-workflow:18889"
	}
	if cozeURL != "http://coze-workflow:18889" {
		return Config{}, invalid
	}
	dsn := lookup("YIJIE_WORKFLOW_POSTGRES_DSN")
	if path := lookup("YIJIE_WORKFLOW_POSTGRES_DSN_FILE"); path != "" {
		if dsn != "" {
			return Config{}, invalid
		}
		data, err := readOwnerFile(path, 4096)
		if err != nil {
			return Config{}, invalid
		}
		dsn = strings.TrimSpace(string(data))
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" || u.Host != "workflow-postgres:5432" || u.Path != "/yijie_workflow_local" || u.RawQuery != "sslmode=disable" || u.Fragment != "" || u.User == nil || u.User.Username() == "" {
		return Config{}, invalid
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return Config{}, invalid
	}
	listen := "127.0.0.1:18888"
	switch lookup("YIJIE_WORKFLOW_NETWORK_SCOPE") {
	case "", "host":
	case "container":
		listen = ":18888"
	default:
		return Config{}, invalid
	}
	editor, err := editorConfig(lookup)
	if err != nil {
		return Config{}, invalid
	}
	return Config{Native: native, Coze: coze, CozeURL: cozeURL, DSN: dsn, Listen: listen, Editor: editor}, nil
}

func editorConfig(lookup func(string) string) (EditorConfig, error) {
	value := EditorConfig{Directory: lookup("YIJIE_WORKFLOW_EDITOR_BUNDLE_DIR"), ManifestSHA: lookup("YIJIE_WORKFLOW_EDITOR_MANIFEST_SHA256")}
	switch lookup("YIJIE_WORKFLOW_EDITOR_ENABLED") {
	case "", "false":
		if value.Directory == "" && value.ManifestSHA == "" {
			return value, nil
		}
	case "true":
		if value.Directory == "/opt/yijie/workflow-editor" && hexToken.MatchString(value.ManifestSHA) {
			value.Enabled = true
			return value, nil
		}
	}
	return EditorConfig{}, errors.New("workflow editor configuration is invalid")
}
