package workflowlocal

import (
	"crypto/subtle"
	"errors"
	"github.com/36Dge/yijie-api/internal/platform/ownedcredential"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const Profile = "feat-153-workflow-local"

var hexToken = regexp.MustCompile(`^[0-9a-f]{64}$`)
var epochPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Credential = ownedcredential.Credential
type Config struct {
	Native, Coze         Credential
	CozeURL, DSN, Listen string
	Editor               EditorConfig
}

type EditorConfig struct {
	Enabled                bool
	Directory, ManifestSHA string
}

func ReadCredential(path string) (Credential, error) { return ownedcredential.Read(path) }
func readOwnerFile(path string, limit int64) ([]byte, error) {
	return ownedcredential.ReadFile(path, limit)
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
