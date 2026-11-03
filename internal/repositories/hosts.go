package repositories

import (
	"context"
	"strings"
)

// HostRegistry knows the hosts an organization has registered beyond the
// static ones, such as a self-managed GitLab.
type HostRegistry interface {
	// PathParser returns the parser for host as orgID may use it, and
	// whether orgID may.
	PathParser(ctx context.Context, orgID, host string) (PathParser, bool, error)
}

// Hosts reads repositories on the static hosts and on whatever Registry
// knows for an organization. A nil Registry knows no hosts.
type Hosts struct {
	Registry HostRegistry
}

// Parse reads a repository from a pasted URL.
func (h Hosts) Parse(ctx context.Context, orgID, raw string) (Repository, error) {
	var lookupErr error
	r, err := ParseURLWith(raw, func(host string) (PathParser, bool) {
		parse, ok, err := h.parser(ctx, orgID, host)
		lookupErr = err
		return parse, ok
	})
	if lookupErr != nil {
		return Repository{}, lookupErr
	}
	return r, err
}

// ParseKey reads back a stored key. A key on a host that is neither static
// nor registered for orgID is ErrUnsupportedHost.
func (h Hosts) ParseKey(ctx context.Context, orgID, key string) (Repository, error) {
	r, err := ParseKey(key)
	if err != nil {
		return Repository{}, err
	}
	if r.onGitHub() {
		return r, nil
	}
	if _, ok := staticPaths[strings.ToLower(r.Host)]; ok {
		return r, nil
	}
	_, ok, err := h.parser(ctx, orgID, r.Host)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, ErrUnsupportedHost
	}
	return r, nil
}

func (h Hosts) parser(ctx context.Context, orgID, host string) (PathParser, bool, error) {
	if h.Registry == nil {
		return nil, false, nil
	}
	return h.Registry.PathParser(ctx, orgID, host)
}
