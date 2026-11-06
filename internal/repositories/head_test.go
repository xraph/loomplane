package repositories_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/repositories"
)

// hostFake answers each request URI (escaped path and query) from routes,
// with a status of its own when the body starts with a three-digit code
// and a space, and remembers what it was asked and with which token.
type hostFake struct {
	mu     sync.Mutex
	routes map[string]string
	asked  []string
	auth   []string
}

func (f *hostFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked = append(f.asked, r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	body, ok := f.routes[r.URL.RequestURI()]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if len(body) >= 4 && body[3] == ' ' && body[0] >= '1' && body[0] <= '5' {
		code := int(body[0]-'0')*100 + int(body[1]-'0')*10 + int(body[2]-'0')
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body[4:]))
		return
	}
	_, _ = w.Write([]byte(body))
}

func (f *hostFake) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

var mono = repositories.Repository{Owner: "acme", Name: "mono"}

func githubFake(t *testing.T, routes map[string]string) (*hostFake, *repositories.GitHub) {
	t.Helper()
	f := &hostFake{routes: routes}
	return f, repositories.NewGitHub(f.serve(t))
}

func TestGitHub_HeadCommitReadsTheDefaultBranch(t *testing.T) {
	f, g := githubFake(t, map[string]string{
		"/repos/acme/mono": `{"default_branch":"main"}`,
		"/repos/acme/mono/commits?per_page=1&sha=main": `[{"sha":"` + testSHA + `","commit":{"message":"Fix the login\n\nLonger body.","author":{"name":"Ada","date":"2026-11-01T10:00:00Z"},"committer":{"date":"2026-11-02T09:30:00+01:00"}}}]`,
	})
	c, err := g.HeadCommit(t.Context(), mono, "", "tok")
	if err != nil {
		t.Fatal(err)
	}
	want := repositories.Commit{
		SHA: testSHA, Ref: "main", Message: "Fix the login\n\nLonger body.", Author: "Ada",
		Date: time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC), URL: "https://github.com/acme/mono/commit/" + testSHA,
	}
	if c != want {
		t.Fatalf("HeadCommit = %+v\nwant %+v", c, want)
	}
	for _, a := range f.auth {
		if a != "Bearer tok" {
			t.Fatalf("authorization %q", a)
		}
	}
}

func TestGitHub_HeadCommitEscapesATrackedBranch(t *testing.T) {
	_, g := githubFake(t, map[string]string{
		"/repos/acme/mono/commits?per_page=1&sha=release%2F1.2": `[{"sha":"` + testSHA + `","commit":{"author":{"name":"Ada","date":"2026-11-01T10:00:00Z"}}}]`,
	})
	g.WebURL = "https://github.example.com/"
	c, err := g.HeadCommit(t.Context(), mono, "release/1.2", "")
	if err != nil {
		t.Fatal(err)
	}
	// Without a committer date the author's stands in.
	if c.Ref != "release/1.2" || !c.Date.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) ||
		c.URL != "https://github.example.com/acme/mono/commit/"+testSHA {
		t.Fatalf("HeadCommit = %+v", c)
	}
	if got := g.RepositoryURL(mono); got != "https://github.example.com/acme/mono" {
		t.Fatalf("RepositoryURL = %q", got)
	}
}

func TestGitHub_HeadCommitRefusesBadRefsAndAnswers(t *testing.T) {
	_, g := githubFake(t, map[string]string{
		"/repos/acme/mono/commits?per_page=1&sha=empty": `[]`,
		"/repos/acme/mono/commits?per_page=1&sha=odd":   `[{"sha":"nope"}]`,
	})
	for ref, want := range map[string]error{"../x": repositories.ErrNotFound, "empty": repositories.ErrNotFound, "gone": repositories.ErrNotFound} {
		if _, err := g.HeadCommit(t.Context(), mono, ref, ""); !errors.Is(err, want) {
			t.Errorf("HeadCommit(%q) = %v, want %v", ref, err, want)
		}
	}
	if _, err := g.HeadCommit(t.Context(), mono, "odd", ""); err == nil || errors.Is(err, repositories.ErrNotFound) {
		t.Errorf("a malformed SHA: %v", err)
	}
}

func TestGitHub_HeadCommitCutsALongMessage(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes
	_, g := githubFake(t, map[string]string{
		"/repos/acme/mono/commits?per_page=1&sha=main": `[{"sha":"` + testSHA + `","commit":{"message":"` + long + `"}}]`,
	})
	c, err := g.HeadCommit(t.Context(), mono, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Message) > 4<<10+len("…") || !utf8.ValidString(c.Message) || !strings.HasSuffix(c.Message, "…") {
		t.Fatalf("message of %d bytes, valid %v", len(c.Message), utf8.ValidString(c.Message))
	}
}

func TestGitHub_LatestReleaseIsThePublishedRelease(t *testing.T) {
	f, g := githubFake(t, map[string]string{
		"/repos/acme/mono/releases/latest": `{"tag_name":"v1.4.0","name":"Spring","published_at":"2026-10-30T12:00:00Z"}`,
	})
	r, err := g.LatestRelease(t.Context(), mono, "")
	if err != nil {
		t.Fatal(err)
	}
	want := repositories.Release{
		Name: "Spring", Tag: "v1.4.0", Kind: repositories.ReleaseKindRelease,
		PublishedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), URL: "https://github.com/acme/mono/releases/tag/v1.4.0",
	}
	if r == nil || *r != want {
		t.Fatalf("LatestRelease = %+v", r)
	}
	if len(f.asked) != 1 {
		t.Fatalf("asked %v; a release needs no tags", f.asked)
	}
}

func TestGitHub_LatestReleaseFallsBackToTheNewestTag(t *testing.T) {
	_, g := githubFake(t, map[string]string{
		"/repos/acme/mono/tags?per_page=1":           `[{"name":"v0.9.1","commit":{"sha":"` + testSHA + `"}}]`,
		"/repos/acme/mono/git/commits/" + testSHA:    `{"committer":{"date":"2026-09-01T08:00:00Z"}}`,
		"/repos/other/mono/tags?per_page=1":          `[{"name":"weird tag?","commit":{"sha":"` + testSHA + `"}}]`,
		"/repos/empty/mono/tags?per_page=1":          `[]`,
		"/repos/broken/mono/releases/latest":         `502 down`,
		"/repos/undated/mono/tags?per_page=1":        `[{"name":"v1","commit":{"sha":"` + testSHA + `"}}]`,
		"/repos/undated/mono/git/commits/" + testSHA: `500 no`,
	})
	r, err := g.LatestRelease(t.Context(), mono, "")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Kind != repositories.ReleaseKindTag || r.Tag != "v0.9.1" || r.Name != "v0.9.1" ||
		r.URL != "https://github.com/acme/mono/tree/v0.9.1" || !r.PublishedAt.Equal(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("tag = %+v", r)
	}
	// A tag that can't be a link points at the list.
	r, err = g.LatestRelease(t.Context(), repositories.Repository{Owner: "other", Name: "mono"}, "")
	if err != nil || r.URL != "https://github.com/other/mono/tags" {
		t.Fatalf("odd tag = %+v, %v", r, err)
	}
	// A commit date that can't be read leaves the tag undated.
	r, err = g.LatestRelease(t.Context(), repositories.Repository{Owner: "undated", Name: "mono"}, "")
	if err != nil || r == nil || !r.PublishedAt.IsZero() {
		t.Fatalf("undated tag = %+v, %v", r, err)
	}
	if r, err = g.LatestRelease(t.Context(), repositories.Repository{Owner: "empty", Name: "mono"}, ""); r != nil || err != nil {
		t.Fatalf("no tags = %+v, %v", r, err)
	}
	if _, err = g.LatestRelease(t.Context(), repositories.Repository{Owner: "broken", Name: "mono"}, ""); err == nil {
		t.Fatal("a failing host isn't an error")
	}
	if _, err = g.LatestRelease(t.Context(), repositories.Repository{Owner: "gone", Name: "mono"}, ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a repository that isn't there: %v", err)
	}
}

func gitlabFake(t *testing.T, routes map[string]string) *repositories.GitLab {
	t.Helper()
	f := &hostFake{routes: routes}
	return repositories.NewGitLab(f.serve(t), nil)
}

const glProject = "/api/v4/projects/grp%2Fsub%2Fapi"

func TestGitLab_HeadCommit(t *testing.T) {
	g := gitlabFake(t, map[string]string{
		glProject:                                `{"default_branch":"main"}`,
		glProject + "/repository/commits/main":   `{"id":"` + testSHA + `","message":"Ship it\n","author_name":"Grace","authored_date":"2026-10-01T00:00:00Z","committed_date":"2026-10-02T00:00:00Z"}`,
		glProject + "/repository/commits/v1%2Fx": `{"id":"` + testSHA + `","author_name":"Grace","authored_date":"2026-10-01T00:00:00Z"}`,
	})
	c, err := g.HeadCommit(t.Context(), gitlabAPI, "", "")
	if err != nil {
		t.Fatal(err)
	}
	base := g.BaseURL + "/grp/sub/api"
	want := repositories.Commit{
		SHA: testSHA, Ref: "main", Message: "Ship it", Author: "Grace",
		Date: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), URL: base + "/-/commit/" + testSHA,
	}
	if c != want {
		t.Fatalf("HeadCommit = %+v\nwant %+v", c, want)
	}
	if c, err = g.HeadCommit(t.Context(), gitlabAPI, "v1/x", ""); err != nil || !c.Date.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("a tracked ref = %+v, %v", c, err)
	}
	if got := g.RepositoryURL(gitlabAPI); got != base {
		t.Fatalf("RepositoryURL = %q", got)
	}
}

func TestGitLab_LatestRelease(t *testing.T) {
	releases := glProject + "/releases?order_by=released_at&sort=desc&per_page=1"
	tags := glProject + "/repository/tags?order_by=updated&sort=desc&per_page=1"
	tests := []struct {
		name   string
		routes map[string]string
		want   *repositories.Release
		err    bool
	}{
		{"a release", map[string]string{releases: `[{"name":"","tag_name":"v2.0","released_at":"2026-10-03T00:00:00Z"}]`},
			&repositories.Release{Name: "v2.0", Tag: "v2.0", Kind: "release", PublishedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), URL: "/grp/sub/api/-/releases/v2.0"}, false},
		{"no release, a tag", map[string]string{releases: `[]`, tags: `[{"name":"v1/rc","commit":{"committed_date":"2026-09-03T00:00:00Z"}}]`},
			&repositories.Release{Name: "v1/rc", Tag: "v1/rc", Kind: "tag", PublishedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), URL: "/grp/sub/api/-/tags/v1%2Frc"}, false},
		{"releases turned off", map[string]string{releases: `403 off`, tags: `[{"name":"v1"}]`},
			&repositories.Release{Name: "v1", Tag: "v1", Kind: "tag", URL: "/grp/sub/api/-/tags/v1"}, false},
		{"neither", map[string]string{releases: `[]`, tags: `[]`}, nil, false},
		{"a failing host", map[string]string{releases: `500 down`}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gitlabFake(t, tt.routes)
			got, err := g.LatestRelease(t.Context(), gitlabAPI, "")
			if (err != nil) != tt.err {
				t.Fatalf("err = %v", err)
			}
			if tt.want != nil {
				w := *tt.want
				w.URL = g.BaseURL + w.URL
				tt.want = &w
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("LatestRelease = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestGitLab_SelfManagedLinksKeepTheInstanceAddress(t *testing.T) {
	g := repositories.NewGitLab("https://git.example.com:8443/", nil)
	repo := repositories.Repository{Host: "git.example.com:8443", Owner: "grp", Name: "api"}
	if got := g.RepositoryURL(repo); got != "https://git.example.com:8443/grp/api" {
		t.Fatalf("RepositoryURL = %q", got)
	}
	if got := g.CommitURL(repo, testSHA); got != "https://git.example.com:8443/grp/api/-/commit/"+testSHA {
		t.Fatalf("CommitURL = %q", got)
	}
}

func bitbucketFake(t *testing.T, routes map[string]string) *repositories.Bitbucket {
	t.Helper()
	f := &hostFake{routes: routes}
	return repositories.NewBitbucket(f.serve(t), "https://bitbucket.example.com")
}

var bbAPI = repositories.Repository{Host: repositories.HostBitbucket, Owner: "acme", Name: "api"}

func TestBitbucket_HeadCommit(t *testing.T) {
	b := bitbucketFake(t, map[string]string{
		"/repositories/acme/api":                     `{"mainbranch":{"name":"trunk"}}`,
		"/repositories/acme/api/refs/branches/trunk": `{"target":{"hash":"` + trunkSHA + `"}}`,
		"/repositories/acme/api/commit/" + trunkSHA:  `{"hash":"` + trunkSHA + `","message":"Tidy\n","date":"2026-10-04T00:00:00Z","author":{"raw":"Lin <lin@example.com>"}}`,
		"/repositories/acme/api/refs/tags/v1.2":      `{"target":{"hash":"` + tagSHA + `"}}`,
		"/repositories/acme/api/commit/" + tagSHA:    `{"hash":"` + tagSHA + `","message":"Tag","date":"2026-10-05T00:00:00Z","author":{"raw":"Lin <lin@example.com>","user":{"display_name":"Lin Q"}}}`,
	})
	c, err := b.HeadCommit(t.Context(), bbAPI, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := repositories.Commit{
		SHA: trunkSHA, Ref: "trunk", Message: "Tidy", Author: "Lin",
		Date: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), URL: "https://bitbucket.example.com/acme/api/commits/" + trunkSHA,
	}
	if c != want {
		t.Fatalf("HeadCommit = %+v\nwant %+v", c, want)
	}
	// A tracked tag resolves after the branches, and a Bitbucket user's
	// name beats the raw author, whose email never comes back.
	if c, err = b.HeadCommit(t.Context(), bbAPI, "v1.2", ""); err != nil || c.SHA != tagSHA || c.Author != "Lin Q" {
		t.Fatalf("a tag = %+v, %v", c, err)
	}
	if _, err = b.HeadCommit(t.Context(), bbAPI, "nowhere", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a missing ref: %v", err)
	}
}

func TestBitbucket_LatestReleaseIsTheNewestTag(t *testing.T) {
	b := bitbucketFake(t, map[string]string{
		"/repositories/acme/api/refs/tags?sort=-target.date&pagelen=1":   `{"values":[{"name":"v3.1","target":{"date":"2026-10-06T00:00:00Z"}}]}`,
		"/repositories/acme/empty/refs/tags?sort=-target.date&pagelen=1": `{"values":[]}`,
	})
	r, err := b.LatestRelease(t.Context(), bbAPI, "")
	if err != nil {
		t.Fatal(err)
	}
	want := repositories.Release{
		Name: "v3.1", Tag: "v3.1", Kind: repositories.ReleaseKindTag,
		PublishedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), URL: "https://bitbucket.example.com/acme/api/src/v3.1",
	}
	if r == nil || *r != want {
		t.Fatalf("LatestRelease = %+v", r)
	}
	if r, err = b.LatestRelease(t.Context(), repositories.Repository{Host: repositories.HostBitbucket, Owner: "acme", Name: "empty"}, ""); r != nil || err != nil {
		t.Fatalf("no tags = %+v, %v", r, err)
	}
	if _, err = b.LatestRelease(t.Context(), repositories.Repository{Host: repositories.HostBitbucket, Owner: "acme", Name: "gone"}, ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a missing repository: %v", err)
	}
}

func TestReaders_HeadCommitAndReleaseGoThroughTheHost(t *testing.T) {
	_, g := githubFake(t, map[string]string{
		"/repos/acme/mono/commits?per_page=1&sha=main": `[{"sha":"` + testSHA + `"}]`,
		"/repos/acme/mono/releases/latest":             `{"tag_name":"v1"}`,
	})
	r := repositories.Readers{
		Static: map[string]repositories.Reader{"github.com": g, "plain.example.com": &fakeReader{name: "plain"}},
		Lookup: func(_ context.Context, host string) (repositories.Reader, bool) {
			if host == "git.example.com:8443" {
				return repositories.NewGitLab("https://git.example.com:8443", nil), true
			}
			return nil, false
		},
	}
	ctx := t.Context()
	if c, err := r.HeadCommit(ctx, mono, "main", ""); err != nil || c.SHA != testSHA {
		t.Fatalf("HeadCommit = %+v, %v", c, err)
	}
	if rel, err := r.LatestRelease(ctx, mono, ""); err != nil || rel.Tag != "v1" {
		t.Fatalf("LatestRelease = %+v, %v", rel, err)
	}
	// A reader that can't say where a repository is, and a host nobody
	// reads, are both unsupported.
	plain := repositories.Repository{Host: "plain.example.com", Owner: "a", Name: "b"}
	if _, err := r.HeadCommit(ctx, plain, "", ""); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a reader without state: %v", err)
	}
	nobody := repositories.Repository{Host: "nobody.example.com", Owner: "a", Name: "b"}
	if _, err := r.LatestRelease(ctx, nobody, ""); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a host nobody reads: %v", err)
	}
	for repo, want := range map[repositories.Repository]string{
		mono: "https://github.com/acme/mono",
		{Host: "git.example.com:8443", Owner: "grp", Name: "api"}: "https://git.example.com:8443/grp/api",
		nobody: "https://nobody.example.com/a/b",
	} {
		if got := r.RepositoryURL(ctx, repo); got != want {
			t.Errorf("RepositoryURL(%s) = %q, want %q", repo.Key(), got, want)
		}
	}
}
