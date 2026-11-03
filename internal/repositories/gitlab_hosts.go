package repositories

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Errors reading the address of a GitLab host.
var (
	// ErrGitLabSubpath means the GitLab is served under a path.
	ErrGitLabSubpath = errors.New("Loomplane can't use a GitLab served under a path yet; give it its own host name")
	// ErrGitLabInsecure means the address is plain http.
	ErrGitLabInsecure = errors.New("a GitLab host has to be served over https")
)

// NormalizeGitLabURL reads the address of a GitLab instance: its web URL
// with a lowercased host, the port if there is one and no trailing slash,
// and that host. A missing scheme is https. Plain http is ErrGitLabInsecure
// unless allowHTTP, which is for hosts the operator configured. An address
// with a path is ErrGitLabSubpath.
func NormalizeGitLabURL(raw string, allowHTTP bool) (webURL, host string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", ErrInvalidURL
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "", "", ErrInvalidURL
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !allowHTTP {
			return "", "", ErrGitLabInsecure
		}
	default:
		return "", "", ErrInvalidURL
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", "", ErrInvalidURL
	}
	if strings.Trim(u.Path, "/") != "" {
		return "", "", ErrGitLabSubpath
	}
	scheme := strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Host)
	// A scheme's default port names the same host as no port.
	if (scheme == "https" && u.Port() == "443") || (scheme == "http" && u.Port() == "80") {
		host = strings.TrimSuffix(host, ":"+u.Port())
	}
	return scheme + "://" + host, host, nil
}

// canonicalGitLabHost lowercases a host and drops a default port, so
// git.example.com:443 and git.example.com are one host.
func canonicalGitLabHost(host string) string {
	host = strings.ToLower(host)
	for _, port := range []string{":443", ":80"} {
		if strings.HasSuffix(host, port) {
			return strings.TrimSuffix(host, port)
		}
	}
	return host
}

// orgClientTimeout bounds a connection to an organization's host up to its
// response headers. A body is bounded by the request: a download keeps
// downloadTimeout, which a client-wide timeout would cut short.
const orgClientTimeout = 30 * time.Second

// errOrgRedirect means an organization's host answered with a redirect
// Loomplane won't follow.
var errOrgRedirect = errors.New("an organization's GitLab host redirected somewhere Loomplane won't follow")

// orgRedirect follows a redirect only to https on the same host. The
// request carries a bearer token, so a redirect to plain http would send it
// in the clear and one to another host would hand it over.
func orgRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" || canonicalGitLabHost(req.URL.Host) != canonicalGitLabHost(via[0].URL.Host) {
		return errOrgRedirect
	}
	return nil
}

var orgClient = sync.OnceValue(func() *http.Client {
	return &http.Client{
		CheckRedirect: orgRedirect,
		Transport: &http.Transport{
			DialContext:           PublicDialer(),
			TLSHandshakeTimeout:   orgClientTimeout,
			ResponseHeaderTimeout: orgClientTimeout,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		},
	}
})

// GitLabRegistry knows the GitLab hosts Loomplane reads besides gitlab.com's
// static one: the operator's, and the ones organizations register.
type GitLabRegistry struct {
	// Instance are the operator's hosts. They are trusted, so their
	// readers may reach private addresses and use plain http.
	Instance []GitLabHost
	Store    GitLabStore
	// AllowOrganizationHosts lets organizations register hosts of their own.
	// Their readers connect only to public addresses.
	AllowOrganizationHosts bool
}

var _ HostRegistry = (*GitLabRegistry)(nil)

func (g GitLabRegistry) instance(host string) (GitLabHost, bool) {
	host = canonicalGitLabHost(host)
	for _, h := range g.Instance {
		if canonicalGitLabHost(h.Host) == host {
			h.Instance = true
			return h, true
		}
	}
	return GitLabHost{}, false
}

// Host returns host as orgID may use it: an instance host, or one orgID
// registered.
func (g GitLabRegistry) Host(ctx context.Context, orgID, host string) (GitLabHost, bool, error) {
	host = canonicalGitLabHost(host)
	if h, ok := g.instance(host); ok {
		return h, true, nil
	}
	if !g.AllowOrganizationHosts || g.Store == nil {
		return GitLabHost{}, false, nil
	}
	hosts, err := g.Store.ListHosts(ctx, orgID)
	if err != nil {
		return GitLabHost{}, false, err
	}
	for _, h := range hosts {
		if canonicalGitLabHost(h.Host) == host {
			return *h, true, nil
		}
	}
	return GitLabHost{}, false, nil
}

// PathParser implements HostRegistry.
func (g GitLabRegistry) PathParser(ctx context.Context, orgID, host string) (PathParser, bool, error) {
	_, ok, err := g.Host(ctx, orgID, host)
	if err != nil || !ok {
		return nil, false, err
	}
	return gitlabPath, true, nil
}

// Reader returns the reader of host: any instance host, or one any
// organization registered. Which organization may use it is for the caller
// to have checked, with PathParser or Host. A store error is no reader.
func (g GitLabRegistry) Reader(ctx context.Context, host string) (Reader, bool) {
	host = canonicalGitLabHost(host)
	if h, ok := g.instance(host); ok {
		return NewGitLab(h.WebURL, nil), true
	}
	if !g.AllowOrganizationHosts || g.Store == nil {
		return nil, false
	}
	hosts, err := g.Store.HostsNamed(ctx, host)
	if err != nil || len(hosts) == 0 {
		return nil, false
	}
	return NewGitLab(hosts[0].WebURL, orgClient()), true
}
