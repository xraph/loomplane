package repositories_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

func TestParseURL(t *testing.T) {
	gh := func(owner, name string) repositories.Repository {
		return repositories.Repository{Owner: owner, Name: name}
	}
	bb := repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}
	tests := []struct {
		in   string
		want repositories.Repository
		err  error
	}{
		{"https://github.com/Acme/API.git", gh("Acme", "API"), nil},
		{"github.com/acme/api", gh("acme", "api"), nil},
		{"git@github.com:acme/api.git", gh("acme", "api"), nil},
		{"https://bitbucket.org/Acme/API", bb, nil},
		{"bitbucket.org/acme/api.git", bb, nil},
		{"git@bitbucket.org:acme/api.git", bb, nil},
		{"https://rex@bitbucket.org/acme/api.git", bb, nil},
		{"https://bitbucket.org/acme/api/src/main/README.md", bb, nil},
		{"bitbucket.org/acme", repositories.Repository{}, repositories.ErrInvalidURL},
		{"bitbucket.org/-bad/api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"gitlab.example.com/a/b", repositories.Repository{}, repositories.ErrUnsupportedHost},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := repositories.ParseURL(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Fatalf("ParseURL(%q) = %+v, %v; want %+v, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestParseGitHubURL_refusesOtherHosts(t *testing.T) {
	if _, err := repositories.ParseGitHubURL("https://bitbucket.org/acme/api"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a Bitbucket URL: %v", err)
	}
}

func TestRepositoryKey(t *testing.T) {
	gh := repositories.Repository{Owner: "acme", Name: "api"}
	bb := repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}
	if gh.Key() != "acme/api" || bb.Key() != "bitbucket.org/acme/api" {
		t.Fatalf("keys %q and %q", gh.Key(), bb.Key())
	}
	if gh.URL() != "https://github.com/acme/api" || bb.URL() != "https://bitbucket.org/acme/api" {
		t.Fatalf("URLs %q and %q", gh.URL(), bb.URL())
	}
}

func TestKey_gitHubStaysBare(t *testing.T) {
	for _, host := range []string{"", "github.com", "GitHub.com"} {
		r := repositories.Repository{Host: host, Owner: "acme", Name: "api"}
		if r.Key() != "acme/api" || r.URL() != "https://github.com/acme/api" {
			t.Fatalf("host %q: key %q, URL %q", host, r.Key(), r.URL())
		}
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		in   string
		want repositories.Repository
		err  error
	}{
		{"acme/api", repositories.Repository{Owner: "acme", Name: "api"}, nil},
		{"bitbucket.org/acme/api", repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}, nil},
		{"bitbucket.org/acme", repositories.Repository{}, repositories.ErrInvalidURL},
		{"gitlab:8443/api", repositories.Repository{}, repositories.ErrInvalidURL},
		{"git.example.com/api", repositories.Repository{}, repositories.ErrInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := repositories.ParseKey(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Fatalf("ParseKey(%q) = %+v, %v; want %+v, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestParseKey_roundTrips(t *testing.T) {
	for _, k := range []string{"acme/api", "gitlab.com/grp/sub/api", "gitlab.example.com:8443/team/api", "bitbucket.org/w/r", "gitlab:8443/team/api", "gitlab/team/api"} {
		r, err := repositories.ParseKey(k)
		if err != nil || r.Key() != k {
			t.Fatalf("ParseKey(%q) = %+v, %v; Key() = %q", k, r, err, r.Key())
		}
		if github := k == "acme/api"; github != (r.Host == "") {
			t.Fatalf("ParseKey(%q) has host %q", k, r.Host)
		}
	}
	// GitHub written with its host reads back bare.
	if r, err := repositories.ParseKey("github.com/acme/api"); err != nil || r.Host != "" || r.Key() != "acme/api" {
		t.Fatalf("ParseKey(github.com/acme/api) = %+v, %v", r, err)
	}
}

func TestParseKey_refusesMalformed(t *testing.T) {
	for _, k := range []string{"", "acme", "/a/b", "gitlab.com/onlyone", "a//b", "acme/..", "gitlab.com/a/../b"} {
		if _, err := repositories.ParseKey(k); !errors.Is(err, repositories.ErrInvalidURL) {
			t.Fatalf("ParseKey(%q): %v", k, err)
		}
	}
}

func TestParseURLWith_handsPathToHostParser(t *testing.T) {
	var gotHost, gotPath string
	parser := func(host, path string) (repositories.Repository, error) {
		gotHost, gotPath = host, path
		owner, name, ok := strings.Cut(strings.SplitN(path, "/-/", 2)[0], "/")
		if !ok {
			return repositories.Repository{}, repositories.ErrInvalidURL
		}
		return repositories.Repository{Host: host, Owner: owner, Name: name}, nil
	}
	lookup := func(host string) (repositories.PathParser, bool) {
		return parser, host == "git.example.com"
	}
	r, err := repositories.ParseURLWith("https://Git.Example.com/a/b/-/tree/x", lookup)
	if err != nil || r.Key() != "git.example.com/a/b" {
		t.Fatalf("ParseURLWith = %+v, %v", r, err)
	}
	if gotHost != "git.example.com" || gotPath != "a/b/-/tree/x" {
		t.Fatalf("parser got (%q, %q)", gotHost, gotPath)
	}
	if _, err := repositories.ParseURLWith("https://other.example.com/a/b", lookup); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a host lookup doesn't know: %v", err)
	}
	if _, err := repositories.ParseURLWith("https://git.example.com/a/b", nil); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("nil lookup: %v", err)
	}
}

func TestErrorsNameNoSingleHost(t *testing.T) {
	for _, err := range []error{repositories.ErrUnsupportedHost, repositories.ErrInvalidURL} {
		if strings.Contains(err.Error(), "GitHub repository") || strings.Contains(err.Error(), "only GitHub") {
			t.Fatalf("%q names a single host", err)
		}
	}
}

func TestParseKey_gitHubWrittenWithItsHost(t *testing.T) {
	for _, k := range []string{"github.com/a/b/c", "github.com/a", "www.github.com/-bad/api"} {
		if _, err := repositories.ParseKey(k); !errors.Is(err, repositories.ErrInvalidURL) {
			t.Fatalf("ParseKey(%q): %v", k, err)
		}
	}
	r, err := repositories.ParseKey("www.github.com/acme/api")
	if err != nil || r.Host != "" || r.Key() != "acme/api" {
		t.Fatalf("ParseKey(www.github.com/acme/api) = %+v, %v", r, err)
	}
}
