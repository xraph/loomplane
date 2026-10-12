package repositories_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/repositories"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		in   string
		want repositories.Repository
		err  error
	}{
		{"https://github.com/acme/mono", repositories.Repository{"acme", "mono"}, nil},
		{"https://github.com/acme/mono.git", repositories.Repository{"acme", "mono"}, nil},
		{"https://github.com/acme/mono/tree/main/apps", repositories.Repository{"acme", "mono"}, nil},
		{"github.com/acme/mono", repositories.Repository{"acme", "mono"}, nil},
		{"git@github.com:acme/mono.git", repositories.Repository{"acme", "mono"}, nil},
		{"  https://www.github.com/acme/my.repo_1  ", repositories.Repository{"acme", "my.repo_1"}, nil},
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

// tarball is the examples/polyglot checkout as GitHub would serve it, with
// entries no checkout may write mixed in.
func tarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(hdr *tar.Header, body []byte) {
		t.Helper()
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	write(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": testSHA}}, nil)
	write(&tar.Header{Name: "acme-mono-0123456/", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	root := "../../examples/polyglot"
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		write(&tar.Header{Name: "acme-mono-0123456/" + filepath.ToSlash(rel), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}, body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evil := []byte("never written")
	for _, name := range []string{
		"acme-mono-0123456/../../escape.txt",
		"/etc/cron.d/x",
		"acme-mono-0123456/node_modules/left-pad/package.json",
		"acme-mono-0123456/apps/web/.next/server.js",
	} {
		write(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(evil))}, evil)
	}
	write(&tar.Header{Name: "acme-mono-0123456/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, nil)
	big := make([]byte, 2048)
	write(&tar.Header{Name: "acme-mono-0123456/huge.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))}, big)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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

func TestGitHub_resolveAndFetchExtractsSafely(t *testing.T) {
	ctx := context.Background()
	f, g := newFake(t)
	repo := repositories.Repository{"acme", "mono"}

	sha, err := g.Resolve(ctx, repo, "", "ghs_token")
	if err != nil || sha != testSHA {
		t.Fatalf("Resolve default branch = %q, %v", sha, err)
	}
	if sha, err = g.Resolve(ctx, repo, "release/1.2", ""); err != nil || sha != testSHA {
		t.Fatalf("Resolve release/1.2 = %q, %v", sha, err)
	}

	dest := t.TempDir()
	st, err := g.Fetch(ctx, repo, sha, "ghs_token", dest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if st.Skipped != 5 { // escape, absolute, node_modules, .next, huge
		t.Fatalf("stats %+v", st)
	}
	for _, never := range []string{"link", "huge.bin", "node_modules", "apps/web/.next"} {
		if _, err = os.Lstat(filepath.Join(dest, never)); !os.IsNotExist(err) {
			t.Fatalf("%s was written", never)
		}
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dest)), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("an entry escaped the checkout")
	}
	if f.auth[0] != "Bearer ghs_token" {
		t.Fatalf("authorization %q", f.auth[0])
	}

	report, err := discovery.Analyze(ctx, os.DirFS(dest))
	if err != nil {
		t.Fatal(err)
	}
	if sum := graph.Build(report).Summarize(); sum.Services != 7 || sum.Packages != 3 || sum.Resources != 2 {
		t.Fatalf("analysis of the fetched checkout: %+v", sum)
	}
}

func TestGitHub_limits(t *testing.T) {
	ctx := context.Background()
	repo := repositories.Repository{"acme", "mono"}
	tests := map[string]func(g *repositories.GitHub){
		"archive": func(g *repositories.GitHub) { g.MaxArchiveBytes = 512 },
		"total":   func(g *repositories.GitHub) { g.MaxTotalBytes = 1024 },
	}
	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			_, g := newFake(t)
			limit(g)
			if _, err := g.Fetch(ctx, repo, testSHA, "", t.TempDir()); !errors.Is(err, repositories.ErrTooLarge) {
				t.Fatalf("Fetch over the %s limit: %v", name, err)
			}
		})
	}
}

func TestGitHub_errors(t *testing.T) {
	ctx := context.Background()
	f, g := newFake(t)
	if _, err := g.Resolve(ctx, repositories.Repository{"acme", "private"}, "main", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
	if _, err := g.Resolve(ctx, repositories.Repository{"acme", "mono"}, "../../etc", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("bad ref: %v", err)
	}
	if _, err := g.Fetch(ctx, repositories.Repository{"acme", "mono"}, "main", "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
		t.Fatalf("fetch by branch: %v", err)
	}
	f.limit = true
	if _, err := g.Resolve(ctx, repositories.Repository{"acme", "mono"}, "main", ""); !errors.Is(err, repositories.ErrRateLimited) {
		t.Fatalf("rate limited: %v", err)
	}
}
