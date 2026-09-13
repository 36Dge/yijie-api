package editorassets

import (
	"net/http"
	"strconv"
	"strings"
)

const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors http://localhost:1420 tauri://localhost"

func Gateway(api http.Handler, bundle *Bundle) http.Handler {
	if bundle == nil {
		return api
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/editor" || strings.HasPrefix(r.URL.Path, "/editor/") {
			bundle.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

func (bundle *Bundle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", ContentSecurityPolicy)
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path || !strings.HasPrefix(r.URL.Path, "/editor/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/editor/")
	if name == "" {
		name = "index.html"
	}
	value, exists := bundle.assets[name]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", value.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(value.data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(value.data)
	}
}
