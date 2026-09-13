package workflowlocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalDedicatedRuntimeConfig(t *testing.T) {
	dir := t.TempDir()
	epoch := "15300000-0000-4000-8000-000000000001"
	paths := []string{filepath.Join(dir, "native.json"), filepath.Join(dir, "engine.json")}
	for i, p := range paths {
		token := strings.Repeat("a", 64)
		if i == 1 {
			token = strings.Repeat("b", 64)
		}
		data, _ := json.Marshal(Credential{SchemaVersion: 1, RunEpoch: epoch, Token: token})
		if os.WriteFile(p, data, 0600) != nil {
			t.Fatal("create normal credential fixture")
		}
	}
	env := map[string]string{"YIJIE_ENV": "local", "YIJIE_LOCAL_PROFILE": "demo_fast", "YIJIE_WORKFLOW_ENABLED": "true", "YIJIE_API_SERVICE_PROFILE": Profile, "YIJIE_WORKFLOW_CREDENTIAL_FILE": paths[0], "YIJIE_WORKFLOW_COZE_CREDENTIAL_FILE": paths[1], "YIJIE_WORKFLOW_POSTGRES_DSN": "postgres://synthetic:synthetic@workflow-postgres:5432/yijie_workflow_local?sslmode=disable"}
	config, err := ConfigFrom(func(k string) string { return env[k] })
	if err != nil || config.Listen != "127.0.0.1:18888" || config.Native.RunEpoch != epoch || config.CozeURL != "http://coze-workflow:18889" {
		t.Fatal("valid local config rejected")
	}
	env["YIJIE_WORKFLOW_NETWORK_SCOPE"] = "container"
	config, err = ConfigFrom(func(k string) string { return env[k] })
	if err != nil || config.Listen != ":18888" {
		t.Fatal("dedicated container listener rejected")
	}
	dsn := env["YIJIE_WORKFLOW_POSTGRES_DSN"]
	dsnFile := filepath.Join(dir, "postgres-dsn.txt")
	if os.WriteFile(dsnFile, []byte(dsn+"\n"), 0600) != nil {
		t.Fatal("create normal private DSN file")
	}
	delete(env, "YIJIE_WORKFLOW_POSTGRES_DSN")
	env["YIJIE_WORKFLOW_POSTGRES_DSN_FILE"] = dsnFile
	t.Setenv("YIJIE_WORKFLOW_POSTGRES_DSN", "")
	config, err = ConfigFrom(func(k string) string { return env[k] })
	if err != nil || config.DSN != dsn {
		t.Fatal("normal private DSN file rejected")
	}
	if os.Getenv("YIJIE_WORKFLOW_POSTGRES_DSN") != "" {
		t.Fatal("private DSN must not be copied into environment")
	}
	env["YIJIE_WORKFLOW_POSTGRES_DSN"] = dsn
	if _, err := ConfigFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("ambiguous DSN configuration must be resolved explicitly")
	}
}
func TestDisabledConfigurationDoesNotReadCredentialFiles(t *testing.T) {
	if _, err := ConfigFrom(func(string) string { return "" }); err == nil {
		t.Fatal("disabled profile must reject startup")
	}
}

func TestNormalEditorArtifactConfiguration(t *testing.T) {
	disabled, err := editorConfig(func(string) string { return "" })
	if err != nil || disabled.Enabled {
		t.Fatal("editor must be disabled without deployment inputs")
	}
	values := map[string]string{"YIJIE_WORKFLOW_EDITOR_ENABLED": "true", "YIJIE_WORKFLOW_EDITOR_BUNDLE_DIR": "/opt/yijie/workflow-editor", "YIJIE_WORKFLOW_EDITOR_MANIFEST_SHA256": strings.Repeat("a", 64)}
	enabled, err := editorConfig(func(key string) string { return values[key] })
	if err != nil || !enabled.Enabled || enabled.Directory != values["YIJIE_WORKFLOW_EDITOR_BUNDLE_DIR"] || enabled.ManifestSHA != values["YIJIE_WORKFLOW_EDITOR_MANIFEST_SHA256"] {
		t.Fatal("normal pinned editor deployment inputs were rejected")
	}
}
