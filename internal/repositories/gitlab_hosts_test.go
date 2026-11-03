package repositories_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

func TestNormalizeGitLabURL(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		allowHTTP bool
		webURL    string
		host      string
		err       error
	}{
		{"case, port and slash", "HTTPS://GitLab.Example.com:8443/", false, "https://gitlab.example.com:8443", "gitlab.example.com:8443", nil},
		{"default https port", "https://gitlab.example.com:443/", false, "https://gitlab.example.com", "gitlab.example.com", nil},
		{"default http port", "http://gitlab.example.com:80", true, "http://gitlab.example.com", "gitlab.example.com", nil},
		{"https keeps port 80", "https://gitlab.example.com:80", false, "https://gitlab.example.com:80", "gitlab.example.com:80", nil},
		{"http keeps port 443", "http://gitlab.example.com:443", true, "http://gitlab.example.com:443", "gitlab.example.com:443", nil},
		{"other port kept", "https://gitlab.example.com:8443", false, "https://gitlab.example.com:8443", "gitlab.example.com:8443", nil},
		{"no scheme", "gitlab.example.com", false, "https://gitlab.example.com", "gitlab.example.com", nil},
		{"no scheme with slash", "GitLab.Example.com/", false, "https://gitlab.example.com", "gitlab.example.com", nil},
		{"spaces", "  https://git.example.com  ", false, "https://git.example.com", "git.example.com", nil},
		{"plain http refused", "http://gitlab.example.com", false, "", "", repositories.ErrGitLabInsecure},
		{"plain http allowed", "http://GitLab.example.com:8080/", true, "http://gitlab.example.com:8080", "gitlab.example.com:8080", nil},
		{"https stays https when http is allowed", "https://gitlab.example.com", true, "https://gitlab.example.com", "gitlab.example.com", nil},
		{"sub-path", "https://example.com/gitlab", false, "", "", repositories.ErrGitLabSubpath},
		{"sub-path when http is allowed", "http://example.com/gitlab/", true, "", "", repositories.ErrGitLabSubpath},
		{"query", "https://gitlab.example.com?x=1", false, "", "", repositories.ErrInvalidURL},
		{"fragment", "https://gitlab.example.com#top", false, "", "", repositories.ErrInvalidURL},
		{"user info", "https://user:pw@gitlab.example.com", false, "", "", repositories.ErrInvalidURL},
		{"other scheme", "ftp://gitlab.example.com", true, "", "", repositories.ErrInvalidURL},
		{"empty", "", false, "", "", repositories.ErrInvalidURL},
		{"no host", "https://", false, "", "", repositories.ErrInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webURL, host, err := repositories.NormalizeGitLabURL(tt.raw, tt.allowHTTP)
			if !errors.Is(err, tt.err) || webURL != tt.webURL || host != tt.host {
				t.Fatalf("NormalizeGitLabURL(%q, %v) = %q, %q, %v; want %q, %q, %v",
					tt.raw, tt.allowHTTP, webURL, host, err, tt.webURL, tt.host, tt.err)
			}
		})
	}
	if _, _, err := repositories.NormalizeGitLabURL("https://example.com/gitlab", false); err == nil ||
		!strings.Contains(err.Error(), "own host name") {
		t.Fatalf("the sub-path error should say what to do, got %v", err)
	}
}

// hostStore is a GitLabStore that knows only hosts.
type hostStore struct {
	repositories.GitLabStore
	hosts []*repositories.GitLabHost
	err   error
}

func (s *hostStore) ListHosts(_ context.Context, orgID string) ([]*repositories.GitLabHost, error) {
	var out []*repositories.GitLabHost
	for _, h := range s.hosts {
		if h.OrgID == orgID {
			out = append(out, h)
		}
	}
	return out, s.err
}

func (s *hostStore) HostsNamed(_ context.Context, host string) ([]*repositories.GitLabHost, error) {
	var out []*repositories.GitLabHost
	for _, h := range s.hosts {
		if h.Host == host {
			out = append(out, h)
		}
	}
	return out, s.err
}

var orgAHost = &repositories.GitLabHost{ID: "glh_1", OrgID: "org-a", WebURL: "https://git.a.com", Host: "git.a.com"}

func TestGitLabRegistry_orgHostsOnlyForTheirOrg(t *testing.T) {
	reg := repositories.GitLabRegistry{Store: &hostStore{hosts: []*repositories.GitLabHost{orgAHost}}, AllowOrganizationHosts: true}
	ctx := context.Background()

	parse, ok, err := reg.PathParser(ctx, "org-a", "git.a.com")
	if err != nil || !ok {
		t.Fatalf("org-a's own host: %v, %v", ok, err)
	}
	got, err := parse("git.a.com", "grp/sub/api")
	if err != nil || got != (repositories.Repository{Host: "git.a.com", Owner: "grp/sub", Name: "api"}) {
		t.Fatalf("parsed %+v, %v", got, err)
	}
	if _, ok, err = reg.PathParser(ctx, "org-b", "git.a.com"); ok || err != nil {
		t.Fatalf("org-b must not parse org-a's host: %v, %v", ok, err)
	}
	if _, ok, err = reg.PathParser(ctx, "org-a", "GIT.A.COM"); !ok || err != nil {
		t.Fatalf("hosts compare without case: %v, %v", ok, err)
	}
	if _, ok := reg.Reader(ctx, "git.a.com"); !ok {
		t.Fatal("the reader of an organization's host should be found")
	}
	if _, ok := reg.Reader(ctx, "git.other.com"); ok {
		t.Fatal("nobody registered git.other.com")
	}
	h, ok, err := reg.Host(ctx, "org-a", "git.a.com")
	if err != nil || !ok || h.ID != "glh_1" || h.Instance {
		t.Fatalf("Host = %+v, %v, %v", h, ok, err)
	}
	if _, ok, _ = reg.Host(ctx, "org-b", "git.a.com"); ok {
		t.Fatal("Host must not cross organizations")
	}
}

func TestGitLabRegistry_orgHostsOff(t *testing.T) {
	reg := repositories.GitLabRegistry{
		Instance: []repositories.GitLabHost{{WebURL: "https://git.corp.example", Host: "git.corp.example"}},
		Store:    &hostStore{hosts: []*repositories.GitLabHost{orgAHost}},
	}
	ctx := context.Background()
	if _, ok, err := reg.PathParser(ctx, "org-a", "git.a.com"); ok || err != nil {
		t.Fatalf("organization hosts are off: %v, %v", ok, err)
	}
	if _, ok := reg.Reader(ctx, "git.a.com"); ok {
		t.Fatal("organization hosts are off, so there is no reader")
	}
	if _, ok, _ := reg.Host(ctx, "org-a", "git.a.com"); ok {
		t.Fatal("organization hosts are off, so Host doesn't find one")
	}
	if _, ok, err := reg.PathParser(ctx, "org-a", "git.corp.example"); !ok || err != nil {
		t.Fatalf("instance hosts stay: %v, %v", ok, err)
	}
	if _, ok := reg.Reader(ctx, "git.corp.example"); !ok {
		t.Fatal("instance hosts keep their reader")
	}
	h, ok, err := reg.Host(ctx, "org-b", "git.corp.example")
	if err != nil || !ok || !h.Instance || h.WebURL != "https://git.corp.example" {
		t.Fatalf("Host = %+v, %v, %v", h, ok, err)
	}
}

func TestGitLabRegistry_storeErrorIsReturned(t *testing.T) {
	boom := errors.New("database is down")
	reg := repositories.GitLabRegistry{Store: &hostStore{err: boom}, AllowOrganizationHosts: true}
	if _, ok, err := reg.PathParser(context.Background(), "org-a", "git.a.com"); ok || !errors.Is(err, boom) {
		t.Fatalf("PathParser = %v, %v; want the store's error", ok, err)
	}
	if _, ok := reg.Reader(context.Background(), "git.a.com"); ok {
		t.Fatal("a store error leaves no reader")
	}
}

func TestGitLabRegistry_nilStore(t *testing.T) {
	reg := repositories.GitLabRegistry{AllowOrganizationHosts: true}
	if _, ok, err := reg.PathParser(context.Background(), "org-a", "git.a.com"); ok || err != nil {
		t.Fatalf("no store knows no hosts: %v, %v", ok, err)
	}
}

func TestGitLabRegistry_instanceHostIsUnguarded(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + sha + `"}`))
	}))
	defer srv.Close()
	webURL, host, err := repositories.NormalizeGitLabURL(srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	reg := repositories.GitLabRegistry{Instance: []repositories.GitLabHost{{WebURL: webURL, Host: host}}}
	rd, ok := reg.Reader(context.Background(), host)
	if !ok {
		t.Fatal("no reader for the instance host")
	}
	got, err := rd.Resolve(context.Background(), repositories.Repository{Host: host, Owner: "grp", Name: "api"}, "main", "")
	if err != nil || got != sha {
		t.Fatalf("Resolve = %q, %v; an instance host on loopback must be reachable", got, err)
	}
}

func TestGitLabRegistry_orgHostIsGuarded(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"0123456789abcdef0123456789abcdef01234567"}`))
	}))
	defer srv.Close()
	webURL, host, err := repositories.NormalizeGitLabURL(srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	reg := repositories.GitLabRegistry{
		Store:                  &hostStore{hosts: []*repositories.GitLabHost{{ID: "glh_1", OrgID: "org-a", WebURL: webURL, Host: host}}},
		AllowOrganizationHosts: true,
	}
	rd, ok := reg.Reader(context.Background(), host)
	if !ok {
		t.Fatal("no reader for the organization host")
	}
	_, err = rd.Resolve(context.Background(), repositories.Repository{Host: host, Owner: "grp", Name: "api"}, "main", "")
	if !errors.Is(err, repositories.ErrPrivateAddress) {
		t.Fatalf("Resolve against a loopback organization host = %v, want ErrPrivateAddress", err)
	}
}

func TestGitLabRegistry_defaultPortIsTheSameHost(t *testing.T) {
	reg := repositories.GitLabRegistry{
		Instance:               []repositories.GitLabHost{{WebURL: "https://git.corp.example", Host: "git.corp.example"}},
		Store:                  &hostStore{hosts: []*repositories.GitLabHost{orgAHost}},
		AllowOrganizationHosts: true,
	}
	ctx := context.Background()
	for _, host := range []string{"git.a.com:443", "GIT.A.COM:443", "git.a.com"} {
		if _, ok, err := reg.PathParser(ctx, "org-a", host); !ok || err != nil {
			t.Fatalf("PathParser(%q) = %v, %v", host, ok, err)
		}
		if _, ok := reg.Reader(ctx, host); !ok {
			t.Fatalf("Reader(%q) found nothing", host)
		}
		if h, ok, err := reg.Host(ctx, "org-a", host); !ok || err != nil || h.ID != "glh_1" {
			t.Fatalf("Host(%q) = %+v, %v, %v", host, h, ok, err)
		}
	}
	if _, ok, _ := reg.PathParser(ctx, "org-a", "git.corp.example:443"); !ok {
		t.Fatal("an instance host with its default port should be found")
	}
	if _, ok := reg.Reader(ctx, "git.corp.example:443"); !ok {
		t.Fatal("an instance host's reader with its default port should be found")
	}
	if _, ok, _ := reg.PathParser(ctx, "org-a", "git.a.com:8443"); ok {
		t.Fatal("a different port is a different host")
	}
}

func TestParseURL_gitlabDefaultPort(t *testing.T) {
	r, err := repositories.ParseURL("https://gitlab.com:443/grp/api")
	if err != nil || r.Key() != "gitlab.com/grp/api" {
		t.Fatalf("ParseURL = %+v, %v; key %q", r, err, r.Key())
	}
}
