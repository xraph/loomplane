package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

func TestHandler_assetLinks(t *testing.T) {
	fingerprint := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	handler := New(t.TempDir(), "/api/", "/.well-known/").WithAndroidApp("com.example.app", []string{fingerprint}).Handler()
	get := func(method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, AssetLinksPath, http.NoBody))
		return rec
	}

	rec := get(http.MethodGet)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var statements []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Namespace    string   `json:"namespace"`
			PackageName  string   `json:"package_name"`
			Fingerprints []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statements); err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 {
		t.Fatalf("statements = %+v", statements)
	}
	s := statements[0]
	if !slices.Equal(s.Relation, []string{"delegate_permission/common.handle_all_urls", "delegate_permission/common.get_login_creds"}) ||
		s.Target.Namespace != "android_app" || s.Target.PackageName != "com.example.app" || !slices.Equal(s.Target.Fingerprints, []string{fingerprint}) {
		t.Fatalf("statement = %+v", s)
	}

	if rec := get(http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST got %d", rec.Code)
	}
	// The rest of /.well-known/ still belongs to the API.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/other", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other well-known path got %d", rec.Code)
	}
}

func TestHandler_noAssetLinksUnlessAsked(t *testing.T) {
	rec := httptest.NewRecorder()
	New(t.TempDir(), "/.well-known/").Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, AssetLinksPath, http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}
