package repositories_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

// TestGitLabPath goes through ParseURL, the only way into gitlabPath.
func TestGitLabPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want repositories.Repository
		err  error
	}{
		{"nested", "grp/sub/api", repositories.Repository{Host: "gitlab.com", Owner: "grp/sub", Name: "api"}, nil},
		{"page inside", "grp/api/-/tree/main", repositories.Repository{Host: "gitlab.com", Owner: "grp", Name: "api"}, nil},
		{"nested page inside", "grp/sub/api/-/merge_requests/3", repositories.Repository{Host: "gitlab.com", Owner: "grp/sub", Name: "api"}, nil},
		{"clone suffix", "grp/api.git", repositories.Repository{Host: "gitlab.com", Owner: "grp", Name: "api"}, nil},
		{"case kept", "Grp/API", repositories.Repository{Host: "gitlab.com", Owner: "Grp", Name: "API"}, nil},
		{"dots and underscores", "_grp/.api_v1.x", repositories.Repository{Host: "gitlab.com", Owner: "_grp", Name: ".api_v1.x"}, nil},
		{"twenty-one segments", strings.Repeat("g/", 20) + "api", repositories.Repository{Host: "gitlab.com", Owner: strings.TrimSuffix(strings.Repeat("g/", 20), "/"), Name: "api"}, nil},
		{"one segment", "api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"only a page", "grp/-/tree/main", repositories.Repository{}, repositories.ErrInvalidURL},
		{"empty", "", repositories.Repository{}, repositories.ErrInvalidURL},
		{"twenty-two segments", strings.Repeat("g/", 21) + "api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"leading dash", "grp/-api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"dot dot", "grp/..", repositories.Repository{}, repositories.ErrInvalidURL},
		{"dot", "grp/.", repositories.Repository{}, repositories.ErrInvalidURL},
		{"feed suffix", "grp/x.atom", repositories.Repository{}, repositories.ErrInvalidURL},
		{"git group", "grp.git/api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"empty segment", "grp//api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"space", "grp/my api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"bare git", "grp/.git", repositories.Repository{}, repositories.ErrInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositories.ParseURL("https://gitlab.com/" + tt.path)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Fatalf("gitlab.com/%s = %+v, %v; want %+v, %v", tt.path, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestParseURL_gitLabForms(t *testing.T) {
	for _, in := range []string{
		"https://gitlab.com/g/s/p",
		"https://GitLab.com/g/s/p/",
		"gitlab.com/g/s/p",
		"git@gitlab.com:g/s/p.git",
		"https://oauth2:tok@gitlab.com/g/s/p.git",
		"https://gitlab.com/g/s/p/-/blob/main/README.md?plain=1#L3",
	} {
		r, err := repositories.ParseURL(in)
		if err != nil || r.Key() != "gitlab.com/g/s/p" || r.URL() != "https://gitlab.com/g/s/p" {
			t.Fatalf("ParseURL(%q) = %+v, %v (key %q)", in, r, err, r.Key())
		}
	}
	if _, err := repositories.ParseGitHubURL("https://gitlab.com/g/p"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("ParseGitHubURL on a GitLab URL: %v", err)
	}
}

const testGitLabToken = "glpat-secret-token"

// fakeGitLab serves the three endpoints Resolve, Project and Projects use.
type fakeGitLab struct {
	mu       sync.Mutex
	paths    []string // escaped path and query of each request
	auth     []string
	status   int // answers every request with this status when set
	retry    string
	pages    int    // how many pages /projects has; the last has no X-Next-Page
	next     string // when set, X-Next-Page on every page, whatever its number
	pageSeen []string
	tgz      []byte     // served as the archive when set
	archive  url.Values // query of the last archive request
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.EscapedPath())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.status != 0 {
		if f.retry != "" {
			w.Header().Set("Retry-After", f.retry)
		}
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.EscapedPath() {
	case "/api/v4/projects/grp%2Fsub%2Fapi":
		_, _ = w.Write([]byte(`{"id":7,"path_with_namespace":"grp/sub/api","name":"API","default_branch":"main","visibility":"private","web_url":"https://gitlab.com/grp/sub/api","namespace":{"full_path":"grp/sub"}}`))
	case "/api/v4/projects/grp%2Fsub%2Fapi/repository/commits/main",
		"/api/v4/projects/grp%2Fsub%2Fapi/repository/commits/release%2F1.2":
		_, _ = w.Write([]byte(`{"id":"` + testSHA + `","short_id":"0123456"}`))
	case "/api/v4/projects/grp%2Fsub%2Fapi/repository/archive.tar.gz":
		f.mu.Lock()
		f.archive = r.URL.Query()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(f.tgz)
	case "/api/v4/projects":
		q := r.URL.Query()
		for k, want := range map[string]string{
			"membership": "true", "min_access_level": "20", "archived": "false",
			"simple": "true", "order_by": "path", "sort": "asc", "per_page": "100",
		} {
			if q.Get(k) != want {
				http.Error(w, "want "+k+"="+want, http.StatusBadRequest)
				return
			}
		}
		page, _ := strconv.Atoi(q.Get("page"))
		f.mu.Lock()
		f.pageSeen = append(f.pageSeen, q.Get("page"))
		f.mu.Unlock()
		if f.next != "" {
			w.Header().Set("X-Next-Page", f.next)
		} else if page < f.pages {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		} else {
			w.Header().Set("X-Next-Page", "")
		}
		_, _ = fmt.Fprintf(w, `[{"id":%d,"path_with_namespace":"grp/p%d","name":"p%d","default_branch":"main","visibility":"public","web_url":"https://gitlab.com/grp/p%d","namespace":{"full_path":"grp"}}]`, page, page, page, page)
	default:
		http.NotFound(w, r)
	}
}

func newFakeGitLab(t *testing.T) (*fakeGitLab, *repositories.GitLab) {
	t.Helper()
	f := &fakeGitLab{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, repositories.NewGitLab(srv.URL, nil)
}

var gitlabAPI = repositories.Repository{Host: "gitlab.com", Owner: "grp/sub", Name: "api"}

func TestGitLab_ResolveDefaultBranch(t *testing.T) {
	f, g := newFakeGitLab(t)
	sha, err := g.Resolve(context.Background(), gitlabAPI, "", "")
	if err != nil || sha != testSHA {
		t.Fatalf("Resolve = %q, %v", sha, err)
	}
	want := []string{
		"/api/v4/projects/grp%2Fsub%2Fapi",
		"/api/v4/projects/grp%2Fsub%2Fapi/repository/commits/main",
	}
	if strings.Join(f.paths, " ") != strings.Join(want, " ") {
		t.Fatalf("requests = %v; want %v", f.paths, want)
	}

	// A named ref skips the project lookup, and its slashes are escaped.
	f.paths = nil
	sha, err = g.Resolve(context.Background(), gitlabAPI, "release/1.2", "")
	if err != nil || sha != testSHA {
		t.Fatalf("Resolve(release/1.2) = %q, %v", sha, err)
	}
	if len(f.paths) != 1 || f.paths[0] != "/api/v4/projects/grp%2Fsub%2Fapi/repository/commits/release%2F1.2" {
		t.Fatalf("requests = %v", f.paths)
	}
}

func TestGitLab_ResolveSHAWithoutCalling(t *testing.T) {
	f, g := newFakeGitLab(t)
	sha, err := g.Resolve(context.Background(), gitlabAPI, testSHA, "tok")
	if err != nil || sha != testSHA {
		t.Fatalf("Resolve = %q, %v", sha, err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("a full SHA made requests: %v", f.paths)
	}
}

func TestGitLab_ResolveRefusesBadRefs(t *testing.T) {
	f, g := newFakeGitLab(t)
	for _, ref := range []string{"../etc", "/main", "a b", "main?x=1"} {
		if _, err := g.Resolve(context.Background(), gitlabAPI, ref, ""); !errors.Is(err, repositories.ErrNotFound) {
			t.Fatalf("Resolve(%q) = %v", ref, err)
		}
	}
	if len(f.paths) != 0 {
		t.Fatalf("a bad ref reached GitLab: %v", f.paths)
	}
}

func TestGitLab_errors(t *testing.T) {
	tests := []struct {
		status int
		retry  string
		want   error
	}{
		{http.StatusNotFound, "", repositories.ErrNotFound},
		{http.StatusForbidden, "", repositories.ErrNotFound},
		{http.StatusUnauthorized, "", repositories.ErrNotFound},
		{http.StatusTooManyRequests, "30", repositories.ErrRateLimited},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			f, g := newFakeGitLab(t)
			f.status, f.retry = tt.status, tt.retry
			_, err := g.Resolve(context.Background(), gitlabAPI, "main", testGitLabToken)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Resolve = %v; want %v", err, tt.want)
			}
			if tt.retry != "" && !strings.Contains(err.Error(), tt.retry) {
				t.Fatalf("error %q drops Retry-After %s", err, tt.retry)
			}
			if strings.Contains(err.Error(), testGitLabToken) {
				t.Fatalf("error %q holds the token", err)
			}
		})
	}

	f, g := newFakeGitLab(t)
	f.status = http.StatusBadGateway
	_, err := g.Resolve(context.Background(), gitlabAPI, "main", "")
	if err == nil || errors.Is(err, repositories.ErrNotFound) || errors.Is(err, repositories.ErrRateLimited) || !strings.Contains(err.Error(), "502") {
		t.Fatalf("a 502 = %v; want a plain error naming the status", err)
	}
}

func TestGitLab_Project(t *testing.T) {
	_, g := newFakeGitLab(t)
	p, err := g.Project(context.Background(), gitlabAPI, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 7 || p.PathWithNamespace != "grp/sub/api" || p.DefaultBranch != "main" || p.Visibility != "private" ||
		p.WebURL != "https://gitlab.com/grp/sub/api" || p.Namespace.FullPath != "grp/sub" || p.Name != "API" {
		t.Fatalf("Project = %+v", p)
	}
}

func TestGitLab_ProjectNotFound(t *testing.T) {
	_, g := newFakeGitLab(t)
	_, err := g.Project(context.Background(), repositories.Repository{Host: "gitlab.com", Owner: "grp", Name: "missing"}, "")
	if !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Project of a missing project = %v", err)
	}
}

func TestGitLab_ProjectsPagesAndCaps(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.pages = 3
	got, err := g.Projects(context.Background(), "tok")
	if err != nil || len(got) != 3 || got[2].PathWithNamespace != "grp/p3" {
		t.Fatalf("Projects over 3 pages = %+v, %v", got, err)
	}
	if strings.Join(f.pageSeen, ",") != "1,2,3" {
		t.Fatalf("pages asked for = %v", f.pageSeen)
	}

	// X-Next-Page never runs out: stop after 10 requests.
	f, g = newFakeGitLab(t)
	f.pages = 11
	got, err = g.Projects(context.Background(), "tok")
	if err != nil || len(got) != 10 {
		t.Fatalf("Projects over 11 pages = %d projects, %v", len(got), err)
	}
	if len(f.pageSeen) != 10 {
		t.Fatalf("made %d requests; want exactly 10", len(f.pageSeen))
	}

	// X-Next-Page that points at the same page, or back, must not loop.
	for _, next := range []string{"1", "0", "abc"} {
		f, g = newFakeGitLab(t)
		f.next = next
		got, err = g.Projects(context.Background(), "tok")
		if err != nil || len(f.pageSeen) > 10 || len(f.pageSeen) != 1 || len(got) != 1 {
			t.Fatalf("X-Next-Page %q: %d requests, %d projects, %v; want 1 request", next, len(f.pageSeen), len(got), err)
		}
	}
	f, g = newFakeGitLab(t)
	f.next = "2"
	f.pages = 1 // page 2 answers X-Next-Page: 2 again
	_, err = g.Projects(context.Background(), "tok")
	if err != nil || len(f.pageSeen) != 2 {
		t.Fatalf("X-Next-Page 2 repeated: %d requests, %v; want 2", len(f.pageSeen), err)
	}

	f, g = newFakeGitLab(t)
	f.status = http.StatusUnauthorized
	if _, err := g.Projects(context.Background(), "bad"); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Projects with a bad token = %v", err)
	}
}

func TestGitLab_bearerOnlyWithToken(t *testing.T) {
	f, g := newFakeGitLab(t)
	ctx := context.Background()
	if _, err := g.Resolve(ctx, gitlabAPI, "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resolve(ctx, gitlabAPI, "main", testGitLabToken); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Project(ctx, gitlabAPI, testGitLabToken); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Projects(ctx, testGitLabToken); err != nil {
		t.Fatal(err)
	}
	want := []string{"", "Bearer " + testGitLabToken, "Bearer " + testGitLabToken, "Bearer " + testGitLabToken}
	if strings.Join(f.auth, "|") != strings.Join(want, "|") {
		t.Fatalf("Authorization headers = %q; want %q", f.auth, want)
	}
}

// gitlabArchive is a project archive as GitLab serves it: gzip, one top
// directory named project-ref-sha, and a node_modules entry nothing may keep.
func gitlabArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	top := "api-" + testSHA + "-" + testSHA + "/"
	for _, e := range []struct{ name, body string }{
		{"package.json", `{"name":"api"}`},
		{"src/main.go", "package main\n"},
		{"node_modules/x/index.js", "never written"},
	} {
		hdr := &tar.Header{Name: top + e.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(e.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGitLab_FetchExtractsArchive(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.tgz = gitlabArchive(t)
	dest := t.TempDir()
	st, err := g.Fetch(context.Background(), gitlabAPI, testSHA, testGitLabToken, dest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if st.Files != 2 || st.Skipped != 1 {
		t.Fatalf("stats %+v; want 2 files and 1 skipped", st)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "src", "main.go")); err != nil || string(got) != "package main\n" {
		t.Fatalf("src/main.go = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "package.json")); err != nil {
		t.Fatalf("package.json: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules was written")
	}
	if f.archive.Get("sha") != testSHA || f.archive.Get("include_lfs_blobs") != "false" {
		t.Fatalf("archive query %v; want sha=%s and include_lfs_blobs=false", f.archive, testSHA)
	}
	if got := f.auth[len(f.auth)-1]; got != "Bearer "+testGitLabToken {
		t.Fatalf("Authorization %q", got)
	}
}

func TestGitLab_FetchRefusesOversized(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.tgz = gitlabArchive(t)
	g.MaxArchiveBytes = 10
	if _, err := g.Fetch(context.Background(), gitlabAPI, testSHA, "", t.TempDir()); !errors.Is(err, repositories.ErrTooLarge) {
		t.Fatalf("Fetch over the archive limit: %v", err)
	}
}

func TestGitLab_FetchRefusesPartialSHA(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.tgz = gitlabArchive(t)
	for _, sha := range []string{"main", "0123456", ""} {
		if _, err := g.Fetch(context.Background(), gitlabAPI, sha, "", t.TempDir()); err == nil {
			t.Fatalf("Fetch(%q) succeeded", sha)
		}
		if _, err := g.Download(context.Background(), gitlabAPI, sha, "", &bytes.Buffer{}); err == nil {
			t.Fatalf("Download(%q) succeeded", sha)
		}
	}
	if len(f.paths) != 0 {
		t.Fatalf("a partial SHA reached GitLab: %v", f.paths)
	}
}

func TestGitLab_FetchErrorsNameNoToken(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.status = http.StatusInternalServerError
	_, err := g.Fetch(context.Background(), gitlabAPI, testSHA, testGitLabToken, t.TempDir())
	if err == nil || strings.Contains(err.Error(), testGitLabToken) {
		t.Fatalf("Fetch error %v", err)
	}
	f.status = http.StatusNotFound
	if _, err = g.Fetch(context.Background(), gitlabAPI, testSHA, "", t.TempDir()); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Fetch of a missing project: %v", err)
	}
}

func TestGitLab_DownloadWritesTheArchive(t *testing.T) {
	f, g := newFakeGitLab(t)
	f.tgz = gitlabArchive(t)
	var buf bytes.Buffer
	n, err := g.Download(context.Background(), gitlabAPI, testSHA, "", &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n != int64(len(f.tgz)) || !bytes.Equal(buf.Bytes(), f.tgz) {
		t.Fatalf("Download wrote %d bytes; the archive is %d", n, len(f.tgz))
	}
	if f.archive.Get("sha") != testSHA || f.archive.Get("include_lfs_blobs") != "false" {
		t.Fatalf("archive query %v", f.archive)
	}

	g.MaxArchiveBytes = 10
	if _, err := g.Download(context.Background(), gitlabAPI, testSHA, "", &bytes.Buffer{}); !errors.Is(err, repositories.ErrTooLarge) {
		t.Fatalf("Download over the archive limit: %v", err)
	}
}
