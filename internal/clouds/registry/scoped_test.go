package registry

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// scopedRegistry is a registry fixture. bearer answers every probe with a
// Bearer challenge; otherwise probes succeed anonymously. It records the
// scope of every token it mints and every upload it was asked to cancel.
func scopedRegistry(t *testing.T, bearer bool) (*httptest.Server, *[]string, *int) {
	t.Helper()
	var mu sync.Mutex
	scopes, cancels := []string{}, 0
	var url string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			scopes = append(scopes, r.URL.Query().Get("scope"))
			_, _ = io.WriteString(w, `{"token":"minted-scoped-token"}`)
		case bearer:
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+url+`/token",service="platform"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodPost:
			w.Header().Set("Location", r.URL.Path+"probe")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodDelete:
			cancels++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	url = s.URL
	t.Cleanup(s.Close)
	return s, &scopes, &cancels
}

func TestScopedOnlyRefusesTokenOnlyCredentials(t *testing.T) {
	s, _, _ := scopedRegistry(t, true)
	cfg := &Config{Endpoint: s.URL, Repository: "loomplane/ws-org_a", Token: "platform-wide-token", ScopedOnly: true}
	raw, err := New(cfg).DockerAuth(t.Context(), "loomplane/ws-org_a/loomplane/web", "pull")
	if !errors.Is(err, ErrInvalid) || strings.Contains(string(raw), "platform-wide-token") {
		t.Fatalf("token-only: %q %v", raw, err)
	}
}

func TestScopedOnlyRefusesAnonymousRegistries(t *testing.T) {
	s, _, cancels := scopedRegistry(t, false)
	cfg := &Config{Endpoint: s.URL, Repository: "loomplane/ws-org_a", Username: "platform", Password: "secret", ScopedOnly: true}
	for _, actions := range []string{"pull", "pull,push"} {
		if raw, err := New(cfg).DockerAuth(t.Context(), "loomplane/ws-org_a/loomplane/web", actions); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s from an anonymous registry: %q %v", actions, raw, err)
		}
	}
	if *cancels != 1 {
		t.Fatalf("the anonymous push probe was cancelled %d times", *cancels)
	}
	// The same registry still serves an organization's own connection.
	cfg.ScopedOnly = false
	if _, err := New(cfg).DockerAuth(t.Context(), "loomplane/ws-org_a/loomplane/web", "pull"); err != nil {
		t.Fatal(err)
	}
}

func TestScopedOnlyMintsWorkspaceScopedTokens(t *testing.T) {
	s, scopes, _ := scopedRegistry(t, true)
	cfg := &Config{Endpoint: s.URL, Repository: "loomplane/ws-org_a", Username: "platform", Password: "secret", Token: "platform-wide-token", ScopedOnly: true}
	raw, err := New(cfg).DockerAuth(t.Context(), "loomplane/ws-org_a/loomplane/web", "pull,push")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "minted-scoped-token") || strings.Contains(string(raw), "platform-wide-token") || strings.Contains(string(raw), "secret") {
		t.Fatalf("auth %q", raw)
	}
	if len(*scopes) != 1 || (*scopes)[0] != "repository:loomplane/ws-org_a/loomplane/web:pull,push" {
		t.Fatalf("scopes %q", *scopes)
	}
	if _, err = New(cfg).DockerAuth(t.Context(), "loomplane/ws-org_b/loomplane/web", "pull"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another workspace's repository: %v", err)
	}
}
