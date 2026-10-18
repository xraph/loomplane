package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type memoryStore struct{ rows map[string]domain.Destination }

func (s *memoryStore) Create(_ context.Context, d *domain.Destination) error {
	s.rows[d.ID] = *d
	return nil
}
func (s *memoryStore) Get(_ context.Context, org, id string) (*domain.Destination, error) {
	d, ok := s.rows[id]
	if !ok || d.OrgID != org {
		return nil, ErrNotFound
	}
	return &d, nil
}
func (s *memoryStore) Update(_ context.Context, d *domain.Destination, generation int64) error {
	old, ok := s.rows[d.ID]
	if !ok || old.Generation != generation {
		return ErrConflict
	}
	s.rows[d.ID] = *d
	return nil
}

type resources struct{ conn connections.Connection }

func (r *resources) GetEnvironment(_ context.Context, org, project, env string) (*projects.Environment, error) {
	if org != "org" || project != "project" || env != "env" {
		return nil, projects.ErrEnvironmentNotFound
	}
	return &projects.Environment{ID: env, OrgID: org, ProjectID: project}, nil
}
func (r *resources) GetConnection(_ context.Context, org, id string) (*connections.Connection, error) {
	if org != "org" || (id != "conn" && id != "alias") {
		return nil, connections.ErrNotFound
	}
	c := r.conn
	c.ID = id
	return &c, nil
}

type authorizer struct {
	deny  bool
	calls []security.Permission
}

func (a *authorizer) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	a.calls = append(a.calls, perm)
	if a.deny {
		return security.ErrForbidden
	}
	return nil
}

func fixture(t *testing.T) (*Service, *resources, *memoryStore, security.Principal, domain.Destination) {
	t.Helper()
	now := time.Now().UTC()
	r := &resources{conn: connections.Connection{OrgID: "org", Cloud: connections.Kubernetes, Account: "cluster", Status: connections.StatusReady, CheckedAt: &now, Details: map[string]string{"namespace_uid": "ns-uid", "cluster_ca": "ca-fingerprint"}}}
	store := &memoryStore{rows: map[string]domain.Destination{}}
	svc := NewService(store, r, r, &authorizer{}, nil, true)
	return svc, r, store, security.Principal{OrgID: "org", UserID: "user"}, domain.Destination{Scope: domain.Scope{ProjectID: "project", EnvironmentID: "env"}, Name: "primary", ConnectionID: "conn", Ownership: domain.Imported}
}

func TestDestinationScope(t *testing.T) {
	svc, _, _, p, in := fixture(t)
	for _, tc := range []struct {
		name   string
		change func(*domain.Destination)
	}{
		{"foreign project", func(d *domain.Destination) { d.ProjectID = "foreign" }},
		{"foreign environment", func(d *domain.Destination) { d.EnvironmentID = "foreign" }},
		{"foreign connection", func(d *domain.Destination) { d.ConnectionID = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := in
			tc.change(&bad)
			if _, err := svc.CreateDestination(t.Context(), p, bad); !errors.Is(err, ErrNotFound) {
				t.Fatalf("got %v", err)
			}
		})
	}
	in.OrgID = "forged-org"
	in.ID = "forged-id"
	in.Generation = 99
	in.Capabilities = domain.CapabilitySet{Revision: "forged", Results: map[domain.Capability]domain.CapabilityResult{domain.StableDNS: {State: domain.Supported}}}
	d, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.OrgID != p.OrgID || d.ID == in.ID || d.Generation != 1 || d.Capabilities.Supports(domain.StableDNS, d.Identity.Revision(), time.Now()) {
		t.Fatalf("trusted caller metadata: %+v", d)
	}
	in.Name = "alias"
	in.ConnectionID = "alias"
	alias, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if alias.Identity.Revision() != d.Identity.Revision() {
		t.Fatal("physical aliases lost their identity")
	}
	if _, err = svc.GetDestination(t.Context(), security.Principal{OrgID: "other"}, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant get: %v", err)
	}
	// The resource interface exposes only reads; imported creation cannot mutate a provider.
}

func TestDestinationIdentityChangeQuarantines(t *testing.T) {
	svc, r, store, p, in := fixture(t)
	d, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	r.conn.Details["namespace_uid"] = "replacement"
	if _, err = svc.RefreshCapabilities(t.Context(), p, d.ID); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("got %v", err)
	}
	saved := store.rows[d.ID]
	if !saved.Quarantined || saved.Generation != 2 || saved.Capabilities.Revision != "" || saved.Identity != d.Identity {
		t.Fatalf("old authority survived: %+v", saved)
	}
	r.conn.Details["namespace_uid"] = "ns-uid"
	if _, err = svc.RefreshCapabilities(t.Context(), p, d.ID); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("quarantine cleared without re-enrollment: %v", err)
	}
}

func TestDestinationDisabledAndUnauthorized(t *testing.T) {
	svc, _, store, p, in := fixture(t)
	svc.enabled = false
	if _, err := svc.CreateDestination(t.Context(), p, in); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	svc.enabled = true
	svc.authz = &authorizer{deny: true}
	if _, err := svc.CreateDestination(t.Context(), p, in); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if len(store.rows) != 0 {
		t.Fatal("unauthorized write")
	}
}

func TestDestinationStaleConnection(t *testing.T) {
	svc, r, _, p, in := fixture(t)
	stale := time.Now().Add(-time.Hour)
	r.conn.CheckedAt = &stale
	if _, err := svc.CreateDestination(t.Context(), p, in); !errors.Is(err, ErrUnverified) {
		t.Fatalf("got %v", err)
	}
}
