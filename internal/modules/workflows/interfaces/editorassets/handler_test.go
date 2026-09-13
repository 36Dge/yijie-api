package editorassets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/36Dge/yijie-api/contracts"
	workflowhttp "github.com/36Dge/yijie-api/internal/modules/workflows/interfaces/http"
)

func normalBundle(t *testing.T) (*Bundle, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"index.html": `<!doctype html><html><head><script type="module" src="/editor/assets/editor.js"></script></head><body>普通编辑器资源</body></html>`, "assets/editor.js": `export const ready = true;`, "assets/editor.css": `body { color: #222; }`}
	meta := manifest{SchemaVersion: 1, BasePath: "/editor/", SourceRepository: "yijie-coze", SourceCommit: strings.Repeat("a", 40), SourceDigest: strings.Repeat("b", 64), ContractsLockSHA: digest(contracts.WorkflowSourceLock)}
	for _, name := range []string{"index.html", "assets/editor.js", "assets/editor.css"} {
		data := []byte(files[name])
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		meta.Files = append(meta.Files, assetFile{Path: name, SHA: digest(data), Bytes: int64(len(data)), ContentType: mediaTypes[filepath.Ext(name)]})
	}
	raw, err := json.Marshal(meta)
	if err != nil || os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0644) != nil {
		t.Fatal("normal manifest creation failed")
	}
	bundle, err := Load(dir, digest(raw))
	if err != nil {
		t.Fatal("normal pinned bundle did not load")
	}
	return bundle, files
}

func TestNormalPinnedEditorAssetsLeaveExistingAPIGate(t *testing.T) {
	bundle, files := normalBundle(t)
	api := workflowhttp.New(nil, "normal-unit-token", "normal-unit-epoch", nil)
	defer api.Close()
	gateway := Gateway(api.Routes(), bundle)
	for _, route := range []string{"/editor/", "/editor/assets/editor.js", "/editor/assets/editor.css"} {
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		name := strings.TrimPrefix(route, "/editor/")
		if name == "" {
			name = "index.html"
		}
		if response.Code != 200 || response.Body.String() != files[name] || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Security-Policy") != ContentSecurityPolicy || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("normal asset response changed bytes or its browser boundary")
		}
		if response.Header().Get("X-Frame-Options") != "" || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("static handler added an incompatible embedding or API CORS policy")
		}
	}
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/workflow-local/status", nil))
	if response.Code != 401 {
		t.Fatal("enabling public editor assets bypassed the existing API machine gate")
	}
}

func TestNormalEditorMetadataAndMissingAssetResponses(t *testing.T) {
	bundle, _ := normalBundle(t)
	response := httptest.NewRecorder()
	bundle.ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/editor/assets/editor.js", nil))
	if response.Code != 200 || response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
		t.Fatal("normal HEAD did not retain asset metadata")
	}
	for _, route := range []string{"/editor/manifest.json", "/editor/assets/missing.js", "/editor/assets/"} {
		response = httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != 404 || response.Body.Len() != 0 {
			t.Fatal("unlisted asset produced a manifest, listing or fallback document")
		}
	}
}

func TestEditorDisabledRetainsOriginalHandler(t *testing.T) {
	called := false
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(401) })
	response := httptest.NewRecorder()
	Gateway(api, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/editor/", nil))
	if !called || response.Code != 401 {
		t.Fatal("disabled editor changed the original handler behavior")
	}
}
