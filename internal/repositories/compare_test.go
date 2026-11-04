package repositories_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/repositories"
)

const (
	baseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var acmeAPI = repositories.Repository{Owner: "acme", Name: "api"}

func compareClient(t *testing.T, h http.HandlerFunc) (*repositories.GitHub, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return repositories.NewGitHub(srv.URL), &calls
}

func TestCompareParsesCommitsAndFiles(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/repos/acme/api/compare/" + baseSHA + "..." + headSHA; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"status":"ahead","ahead_by":2,"behind_by":0,"total_commits":2,
			"commits":[
				{"sha":"c1","commit":{"message":"first\n\nbody","author":{"name":"Ada","date":"2026-11-01T10:00:00Z"}}},
				{"sha":"c2","commit":{"message":"second","author":{"name":"Bo","date":"2026-11-02T10:00:00Z"}}}
			],
			"files":[
				{"filename":"db/migrate/1.rb","status":"added","additions":10,"deletions":1,"patch":"@@ +x"},
				{"filename":"logo.png","status":"modified","additions":0,"deletions":0}
			]}`))
	})
	got, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, "tok")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got.Status != "ahead" || got.AheadBy != 2 || got.BehindBy != 0 || got.TotalCommits != 2 {
		t.Fatalf("summary = %+v", got)
	}
	if len(got.Commits) != 2 || got.Commits[0].SHA != "c1" || got.Commits[0].Message != "first\n\nbody" ||
		got.Commits[0].Author != "Ada" || !got.Commits[0].Date.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("commits = %+v", got.Commits)
	}
	if len(got.Files) != 2 || got.Files[0].Filename != "db/migrate/1.rb" || got.Files[0].Status != "added" ||
		got.Files[0].Additions != 10 || got.Files[0].Deletions != 1 || got.Files[0].Patch != "@@ +x" || got.Files[1].Patch != "" {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestCompareRefusesNonSHARefs(t *testing.T) {
	gh, calls := compareClient(t, func(http.ResponseWriter, *http.Request) {})
	for _, c := range [][2]string{{"main", headSHA}, {baseSHA, "main"}, {baseSHA[:39], headSHA}} {
		_, err := gh.Compare(context.Background(), acmeAPI, c[0], c[1], "")
		if !errors.Is(err, repositories.ErrNotACommit) {
			t.Errorf("Compare(%q, %q) err = %v, want ErrNotACommit", c[0], c[1], err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests made", calls.Load())
	}
}

// GitLab and Bitbucket have no compare yet. They must say so, never return
// an empty diff that reads as "no change".
func TestCompareRefusesOtherHosts(t *testing.T) {
	gh, calls := compareClient(t, func(http.ResponseWriter, *http.Request) {})
	for _, host := range []string{repositories.HostGitLab, repositories.HostBitbucket, "git.example.com"} {
		repo := repositories.Repository{Host: host, Owner: "acme", Name: "api"}
		got, err := gh.Compare(context.Background(), repo, baseSHA, headSHA, "")
		if !errors.Is(err, repositories.ErrUnsupportedHost) || got != nil {
			t.Errorf("host %q: got %+v, %v; want nil, ErrUnsupportedHost", host, got, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests made", calls.Load())
	}
	// github.com spelled out is still GitHub.
	gh2, _ := compareClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"identical"}`)) })
	if _, err := gh2.Compare(context.Background(), repositories.Repository{Host: "github.com", Owner: "acme", Name: "api"}, baseSHA, headSHA, ""); err != nil {
		t.Fatalf("github.com host: %v", err)
	}
}

func TestCompareNotFound(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	got, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, "")
	if !errors.Is(err, repositories.ErrNotFound) || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompareRateLimited(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	got, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, "")
	if !errors.Is(err, repositories.ErrRateLimited) || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompareServerErrorIsAnError(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	got, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, "")
	if err == nil || got != nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("got %+v, %v; want an error naming 502", got, err)
	}
}

func TestCompareBoundsTheBody(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ahead","files":[{"filename":"a","patch":"`))
		chunk := []byte(strings.Repeat("x", 1<<20))
		for range 17 {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}]}`))
	})
	got, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, "")
	if err == nil || got != nil {
		t.Fatalf("got %+v, %v; want an error and no partial parse", got, err)
	}
}

func TestCompareWithoutTokenSendsNoAuthorization(t *testing.T) {
	gh, _ := compareClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization sent: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"identical"}`))
	})
	if _, err := gh.Compare(context.Background(), acmeAPI, baseSHA, headSHA, ""); err != nil {
		t.Fatal(err)
	}
}
