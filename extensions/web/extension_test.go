package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, dir, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	New(dir, "/api/", "/authsome/").Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody))
	return rec
}

func TestHandler_servesTheBuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html":    "<div id=root></div>",
		"assets/app.js": "console.log(1)",
		"favicon.svg":   "<svg/>",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, method, path string
		status             int
		body, cache        string
	}{
		{"root", http.MethodGet, "/", http.StatusOK, "id=root", "no-cache"},
		{"client route", http.MethodGet, "/projects/proj_1", http.StatusOK, "id=root", "no-cache"},
		{"asset", http.MethodGet, "/assets/app.js", http.StatusOK, "console.log", "immutable"},
		{"file", http.MethodGet, "/favicon.svg", http.StatusOK, "<svg/>", "no-cache"},
		// net/http refuses a path containing ".." before any file is opened.
		{"escape attempt", http.MethodGet, "/../../etc/passwd", http.StatusBadRequest, "invalid URL path", ""},
		{"api path", http.MethodGet, "/api/v1/nothing", http.StatusNotFound, "not found", ""},
		{"authsome path", http.MethodPost, "/authsome/v1/nothing", http.StatusNotFound, "not found", ""},
		{"write", http.MethodPost, "/projects", http.StatusMethodNotAllowed, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, dir, tt.method, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status %d, want %d", rec.Code, tt.status)
			}
			if !strings.Contains(rec.Body.String(), tt.body) {
				t.Fatalf("body %q, want it to contain %q", rec.Body.String(), tt.body)
			}
			if tt.cache != "" && !strings.Contains(rec.Header().Get("Cache-Control"), tt.cache) {
				t.Fatalf("Cache-Control %q, want %q", rec.Header().Get("Cache-Control"), tt.cache)
			}
		})
	}
}

func TestHandler_withoutABuild(t *testing.T) {
	rec := serve(t, filepath.Join(t.TempDir(), "missing"), http.MethodGet, "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "isn't built") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
