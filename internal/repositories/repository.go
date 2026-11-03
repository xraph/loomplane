package repositories

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Errors reading a repository from what someone typed or stored. Each is
// permanent: retrying won't help.
var (
	ErrUnsupportedHost = errors.New("Loomplane can't read repositories from that host yet; for a self-managed GitLab, add its host on Integrations first") //nolint:staticcheck // ST1005: a sentence people read, and Loomplane is a name
	ErrInvalidURL      = errors.New("that isn't a repository URL Loomplane can read")
)

// The hosts Loomplane knows without being told. HostGitHub is empty so
// every repository stored before hosts existed is still a GitHub one.
const (
	HostGitHub    = ""
	HostBitbucket = "bitbucket.org"
	HostGitLab    = "gitlab.com"

	githubDomain = "github.com"
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

	// Bitbucket slugs are lowercase. Loomplane folds case before it checks.
	workspacePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,61}$`)
	bitbucketRepoPattern = regexp.MustCompile(`^[a-z0-9._-]{1,128}$`)

	// scpURL is the address a clone button gives for SSH: git@host:path.
	scpURL = regexp.MustCompile(`^git@([^:/\s]+):(.+)$`)
)

// Repository is a repository on a Git host.
type Repository struct {
	// Host is the host's name, or HostGitHub ("") for github.com.
	Host string `json:"host,omitempty"`
	// Owner is a GitHub owner, a Bitbucket workspace, or the group path on
	// hosts that nest groups.
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// FullName is "owner/name".
func (r Repository) FullName() string { return r.Owner + "/" + r.Name }

// onGitHub reports whether r is on github.com, however its host is spelled.
func (r Repository) onGitHub() bool {
	return r.Host == HostGitHub || strings.EqualFold(r.Host, githubDomain)
}

// Key is what a source stores: "owner/name" for GitHub, which is what it
// has always been, and "host/owner/name" for anything else.
func (r Repository) Key() string {
	if r.onGitHub() {
		return r.FullName()
	}
	return r.Host + "/" + r.FullName()
}

// URL is the repository's page on its host.
func (r Repository) URL() string {
	host := r.Host
	if r.onGitHub() {
		host = githubDomain
	}
	return "https://" + host + "/" + r.FullName()
}

// PathParser reads a repository from the part of a URL after its host,
// which has no leading slash, query or fragment. host is the lowercased
// host as written, port included.
type PathParser func(host, path string) (Repository, error)

// staticPaths are the hosts that need no registration.
var staticPaths = map[string]PathParser{
	githubDomain:     githubPath,
	"www.github.com": githubPath,
	HostBitbucket:    bitbucketPath,
	HostGitLab:       gitlabPath,
}

// ParseURL reads a repository from the URLs people paste, on a host
// Loomplane knows statically: https://github.com/o/r, github.com/o/r,
// git@github.com:o/r.git, and the same for Bitbucket, including a clone URL
// with a user name in it and the address of a page inside the repository.
func ParseURL(raw string) (Repository, error) { return ParseURLWith(raw, nil) }

// ParseURLWith is ParseURL that asks lookup, which may be nil, for the
// parser of a host that isn't static.
func ParseURLWith(raw string, lookup func(host string) (PathParser, bool)) (Repository, error) {
	s := strings.TrimSpace(raw)
	if m := scpURL.FindStringSubmatch(s); m != nil {
		s = "https://" + m[1] + "/" + m[2]
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Repository{}, ErrInvalidURL
	}
	host := strings.ToLower(u.Host)
	parse, ok := staticPaths[strings.ToLower(u.Hostname())]
	if !ok && lookup != nil {
		parse, ok = lookup(host)
	}
	if !ok {
		return Repository{}, ErrUnsupportedHost
	}
	return parse(host, strings.Trim(u.Path, "/"))
}

// ParseGitHubURL is ParseURL that refuses any host but GitHub.
func ParseGitHubURL(raw string) (Repository, error) {
	r, err := ParseURL(raw)
	if err != nil {
		return Repository{}, err
	}
	if !r.onGitHub() {
		return Repository{}, ErrUnsupportedHost
	}
	return r, nil
}

// ParseKey reads back a repository's Key. Two segments, the first with
// neither a dot nor a colon, are GitHub's owner/name. Anything else is
// host/owner/.../name with at least two segments after the host, and the
// host may carry a port. It doesn't check that the host is one Loomplane
// reads: Hosts.ParseKey does.
func ParseKey(key string) (Repository, error) {
	parts := strings.Split(key, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return Repository{}, ErrInvalidURL
		}
	}
	if len(parts) == 2 && !strings.ContainsAny(parts[0], ".:") {
		return githubPath(HostGitHub, key)
	}
	host := strings.ToLower(parts[0])
	if len(parts) < 3 {
		return Repository{}, ErrInvalidURL
	}
	if host == githubDomain || host == "www."+githubDomain {
		// GitHub written with its host reads back bare, under GitHub's rules.
		if len(parts) != 3 {
			return Repository{}, ErrInvalidURL
		}
		return githubPath(HostGitHub, parts[1]+"/"+parts[2])
	}
	return Repository{
		Host:  host,
		Owner: strings.Join(parts[1:len(parts)-1], "/"),
		Name:  parts[len(parts)-1],
	}, nil
}

// githubPath keeps the first two segments, case as typed.
func githubPath(_, path string) (Repository, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return Repository{}, ErrInvalidURL
	}
	r := Repository{Host: HostGitHub, Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
	if !ownerPattern.MatchString(r.Owner) || !repoPattern.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return Repository{}, ErrInvalidURL
	}
	return r, nil
}

// bitbucketPath keeps the first two segments, which drops a page path such
// as /src/main/, and folds case, since Bitbucket's slugs are lowercase.
func bitbucketPath(_, path string) (Repository, error) {
	parts := strings.Split(strings.ToLower(path), "/")
	if len(parts) < 2 {
		return Repository{}, ErrInvalidURL
	}
	r := Repository{Host: HostBitbucket, Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
	if !workspacePattern.MatchString(r.Owner) || !bitbucketRepoPattern.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return Repository{}, ErrInvalidURL
	}
	return r, nil
}
