package repositories_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

// fakeRegistry knows git.example.com for one organization.
type fakeRegistry struct{ asked []string }

func (f *fakeRegistry) PathParser(_ context.Context, orgID, host string) (repositories.PathParser, bool, error) {
	f.asked = append(f.asked, orgID+" "+host)
	if orgID != "org_a" || host != "git.example.com" {
		return nil, false, nil
	}
	return func(host, path string) (repositories.Repository, error) {
		i := strings.LastIndex(path, "/")
		return repositories.Repository{Host: host, Owner: path[:i], Name: path[i+1:]}, nil
	}, true, nil
}

func TestHosts_ParseKeyRefusesUnregisteredHost(t *testing.T) {
	ctx := context.Background()
	reg := &fakeRegistry{}
	hosts := repositories.Hosts{Registry: reg}

	for _, key := range []string{"acme/api", "bitbucket.org/acme/api"} {
		if _, err := hosts.ParseKey(ctx, "org_a", key); err != nil {
			t.Fatalf("static key %q: %v", key, err)
		}
	}
	if len(reg.asked) != 0 {
		t.Fatalf("the registry was asked about a static host: %v", reg.asked)
	}

	if r, err := hosts.ParseKey(ctx, "org_a", "git.example.com/g/s/p"); err != nil || r.Key() != "git.example.com/g/s/p" {
		t.Fatalf("a registered host: %+v, %v", r, err)
	}
	if _, err := hosts.ParseKey(ctx, "org_b", "git.example.com/g/s/p"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("another organization's host: %v", err)
	}
	if _, err := (repositories.Hosts{}).ParseKey(ctx, "org_a", "git.example.com/grp/api"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("nil Registry: %v", err)
	}
	if _, err := (repositories.Hosts{}).ParseKey(ctx, "org_a", "gitlab/team/api"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a bare host name with nil Registry: %v", err)
	}
	if _, err := hosts.ParseKey(ctx, "org_a", "bitbucket.org/acme"); !errors.Is(err, repositories.ErrInvalidURL) {
		t.Fatalf("a malformed key: %v", err)
	}
}

func TestHosts_Parse(t *testing.T) {
	ctx := context.Background()
	reg := &fakeRegistry{}
	hosts := repositories.Hosts{Registry: reg}

	r, err := hosts.Parse(ctx, "org_a", "https://git.example.com/g/s/p")
	if err != nil || r != (repositories.Repository{Host: "git.example.com", Owner: "g/s", Name: "p"}) {
		t.Fatalf("a registered host: %+v, %v", r, err)
	}
	if _, err := hosts.Parse(ctx, "org_b", "https://git.example.com/g/s/p"); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("another organization's host: %v", err)
	}
	reg.asked = nil
	for _, raw := range []string{"github.com/acme/api", "https://bitbucket.org/acme/api"} {
		if _, err := hosts.Parse(ctx, "org_a", raw); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	if len(reg.asked) != 0 {
		t.Fatalf("the registry was asked about a static host: %v", reg.asked)
	}
}

// downRegistry fails every lookup.
type downRegistry struct{ err error }

func (d downRegistry) PathParser(context.Context, string, string) (repositories.PathParser, bool, error) {
	return nil, false, d.err
}

func TestHosts_aRegistryErrorComesBackAsItself(t *testing.T) {
	ctx := context.Background()
	down := errors.New("registry down")
	hosts := repositories.Hosts{Registry: downRegistry{err: down}}
	if _, err := hosts.Parse(ctx, "org_a", "https://git.example.com/g/p"); !errors.Is(err, down) {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := hosts.ParseKey(ctx, "org_a", "git.example.com/g/p"); !errors.Is(err, down) {
		t.Fatalf("ParseKey: %v", err)
	}
	// A static host never asks, so a registry that is down doesn't matter.
	if _, err := hosts.Parse(ctx, "org_a", "bitbucket.org/acme/api"); err != nil {
		t.Fatalf("a static host with the registry down: %v", err)
	}
}
