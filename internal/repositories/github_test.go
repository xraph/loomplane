package repositories_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		in   string
		want repositories.Repository
		err  error
	}{
		{"https://github.com/acme/mono", repositories.Repository{Owner: "acme", Name: "mono"}, nil},
		{"https://github.com/acme/mono.git", repositories.Repository{Owner: "acme", Name: "mono"}, nil},
		{"https://github.com/acme/mono/tree/main/apps", repositories.Repository{Owner: "acme", Name: "mono"}, nil},
		{"github.com/acme/mono", repositories.Repository{Owner: "acme", Name: "mono"}, nil},
		{"git@github.com:acme/mono.git", repositories.Repository{Owner: "acme", Name: "mono"}, nil},
		{"  https://www.github.com/acme/my.repo_1  ", repositories.Repository{Owner: "acme", Name: "my.repo_1"}, nil},
		{"https://gitlab.com/acme/mono", repositories.Repository{}, repositories.ErrUnsupportedHost},
		{"https://github.com/acme", repositories.Repository{}, repositories.ErrInvalidURL},
		{"https://github.com/-acme/mono", repositories.Repository{}, repositories.ErrInvalidURL},
		{"https://github.com/acme/..", repositories.Repository{}, repositories.ErrInvalidURL},
		{"https://github.com/acme/mo%20no", repositories.Repository{}, repositories.ErrInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := repositories.ParseGitHubURL(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Fatalf("repositories.ParseGitHubURL(%q) = %+v, %v; want %+v, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestValidRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"main": true, "release/1.2": true, "v1.0.0": true, testSHA: true,
		"": false, "../etc": false, "/main": false, "main?x=1": false, "a b": false,
	} {
		if got := repositories.ValidRef(ref); got != want {
			t.Fatalf("repositories.ValidRef(%q) = %v", ref, got)
		}
	}
}

type fakeGitHub struct {
	mu    sync.Mutex
	auth  []string
	tgz   []byte
	limit bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.limit {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/repos/acme/mono":
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	case "/repos/acme/mono/commits/main", "/repos/acme/mono/commits/release/1.2":
		if r.Header.Get("Accept") != "application/vnd.github.sha" {
			http.Error(w, "want the sha media type", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(testSHA))
	case "/repos/acme/mono/tarball/" + testSHA:
		// GitHub redirects tarballs to codeload.
		http.Redirect(w, r, "/codeload/acme/mono/tar.gz/"+testSHA, http.StatusFound)
	case "/codeload/acme/mono/tar.gz/" + testSHA:
		_, _ = w.Write(f.tgz)
	default:
		http.NotFound(w, r)
	}
}

func newFake(t *testing.T) (*fakeGitHub, *repositories.GitHub) {
	t.Helper()
	f := &fakeGitHub{tgz: tarball(t)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	g := repositories.NewGitHub(srv.URL)
	g.MaxFileBytes = 1024
	return f, g
}

func TestGitHub_errors(t *testing.T) {
	ctx := context.Background()
	f, g := newFake(t)
	if _, err := g.Resolve(ctx, repositories.Repository{Owner: "acme", Name: "private"}, "main", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
	if _, err := g.Resolve(ctx, repositories.Repository{Owner: "acme", Name: "mono"}, "../../etc", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("bad ref: %v", err)
	}
	if _, err := g.Fetch(ctx, repositories.Repository{Owner: "acme", Name: "mono"}, "main", "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
		t.Fatalf("fetch by branch: %v", err)
	}
	f.limit = true
	if _, err := g.Resolve(ctx, repositories.Repository{Owner: "acme", Name: "mono"}, "main", ""); !errors.Is(err, repositories.ErrRateLimited) {
		t.Fatalf("rate limited: %v", err)
	}
}
