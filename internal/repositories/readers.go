package repositories

import (
	"context"
	"io"
	"strings"
)

// Reader reads one Git host's repositories. *GitHub is one.
type Reader interface {
	// Resolve returns the commit ref names, or the default branch's head
	// when ref is empty.
	Resolve(ctx context.Context, repo Repository, ref, token string) (string, error)
	// Fetch extracts the repository at commit sha into dest.
	Fetch(ctx context.Context, repo Repository, sha, token, dest string) (Stats, error)
	// Download writes the repository's tarball at commit sha to w.
	Download(ctx context.Context, repo Repository, sha, token string, w io.Writer) (int64, error)
}

// TokenSource gives a short-lived token that reads one repository for one
// organization, or "" to read it anonymously.
type TokenSource interface {
	Token(ctx context.Context, orgID string, repo Repository) (string, error)
}

var (
	_ Reader      = (*GitHub)(nil)
	_ Reader      = Readers{}
	_ TokenSource = (*Connections)(nil)
	_ TokenSource = Tokens{}
)

// Readers picks a Reader by the repository's host. It is itself a Reader.
type Readers struct {
	// Static maps host names to readers. GitHub is "github.com".
	Static map[string]Reader
	// Lookup finds the reader of a host Static doesn't have, such as a
	// self-managed one an organization has registered. It may be nil.
	Lookup func(ctx context.Context, host string) (Reader, bool)
}

// For returns the reader of host, where "" means github.com. A host
// nobody reads is ErrUnsupportedHost.
func (r Readers) For(ctx context.Context, host string) (Reader, error) {
	if rd, ok := byHost(r.Static, host); ok {
		return rd, nil
	}
	if r.Lookup != nil {
		if rd, ok := r.Lookup(ctx, host); ok {
			return rd, nil
		}
	}
	return nil, ErrUnsupportedHost
}

// Resolve reads through the repository's host.
func (r Readers) Resolve(ctx context.Context, repo Repository, ref, token string) (string, error) {
	rd, err := r.For(ctx, repo.Host)
	if err != nil {
		return "", err
	}
	return rd.Resolve(ctx, repo, ref, token)
}

// Fetch reads through the repository's host.
func (r Readers) Fetch(ctx context.Context, repo Repository, sha, token, dest string) (Stats, error) {
	rd, err := r.For(ctx, repo.Host)
	if err != nil {
		return Stats{}, err
	}
	return rd.Fetch(ctx, repo, sha, token, dest)
}

// Download reads through the repository's host.
func (r Readers) Download(ctx context.Context, repo Repository, sha, token string, w io.Writer) (int64, error) {
	rd, err := r.For(ctx, repo.Host)
	if err != nil {
		return 0, err
	}
	return rd.Download(ctx, repo, sha, token, w)
}

// Tokens picks a TokenSource by the repository's host. It is itself one.
type Tokens struct {
	// Static maps host names to token sources. GitHub is "github.com".
	Static map[string]TokenSource
	// Lookup finds the source of a host Static doesn't have. It may be nil.
	Lookup func(ctx context.Context, host string) (TokenSource, bool)
}

// Token asks the repository's host. A host with no token source reads
// anonymously, which is "" and no error.
func (t Tokens) Token(ctx context.Context, orgID string, repo Repository) (string, error) {
	src, ok := byHost(t.Static, repo.Host)
	if !ok && t.Lookup != nil {
		src, ok = t.Lookup(ctx, repo.Host)
	}
	if !ok {
		return "", nil
	}
	return src.Token(ctx, orgID, repo)
}

// byHost finds host in m without regard to case, where "" is github.com.
func byHost[T any](m map[string]T, host string) (T, bool) {
	host = hostName(host)
	for k, v := range m {
		if strings.EqualFold(hostName(k), host) {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func hostName(host string) string {
	if host == HostGitHub {
		return githubDomain
	}
	return host
}
