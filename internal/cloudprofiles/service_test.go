package cloudprofiles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

type projectLookup struct{}

func (projectLookup) GetProject(_ context.Context, org, id string) (*projects.Project, error) {
	if org != "org_1" || id != "proj_1" {
		return nil, projects.ErrNotFound
	}
	return &projects.Project{ID: id, OrgID: org}, nil
}
func (projectLookup) GetEnvironment(_ context.Context, org, project, id string) (*projects.Environment, error) {
	if org != "org_1" || project != "proj_1" || id != "env_1" {
		return nil, projects.ErrEnvironmentNotFound
	}
	return &projects.Environment{ID: id, OrgID: org, ProjectID: project}, nil
}

type connectionLookup struct{ cloud, status string }

func (c connectionLookup) All(context.Context, string) ([]*connections.Connection, error) {
	return []*connections.Connection{{ID: "conn_1", OrgID: "org_1", Cloud: c.cloud, Status: c.status}}, nil
}

type authz struct {
	err        error
	permission security.Permission
	resource   string
}

func (a *authz) Require(_ context.Context, _ security.Principal, perm security.Permission, resource string) error {
	a.permission, a.resource = perm, resource
	return a.err
}

type memoryStore struct {
	Store
	profile   *Profile
	target    *Target
	protected bool
	reads     int
	// granted is a project that sees the profile through a grant rather
	// than as its creator.
	granted string
}

func (m *memoryStore) GetProfile(_ context.Context, org, project, id string) (*Profile, error) {
	m.reads++
	if m.profile == nil || m.profile.OrgID != org || (m.profile.ProjectID != project && (m.granted == "" || m.granted != project)) || m.profile.ID != id {
		return nil, ErrNotFound
	}
	c := *m.profile
	return &c, nil
}
func (m *memoryStore) CreateProfile(_ context.Context, p *Profile) error {
	c := *p
	m.profile = &c
	return nil
}
func (m *memoryStore) UpdateProfile(_ context.Context, p *Profile, revision int64) error {
	if m.profile.Revision != revision {
		return ErrConflict
	}
	c := *p
	m.profile = &c
	return nil
}
func (m *memoryStore) GetTarget(_ context.Context, org, project, cloud, id string) (*Target, error) {
	if m.target == nil || m.target.OrgID != org || m.target.ProjectID != project || m.target.CloudID != cloud || m.target.ID != id {
		return nil, ErrNotFound
	}
	c := *m.target
	return &c, nil
}
func (m *memoryStore) CreateTarget(_ context.Context, target *Target) error {
	if m.target != nil && m.target.Name == target.Name {
		return ErrExists
	}
	c := *target
	m.target = &c
	return nil
}
func (m *memoryStore) UpdateTarget(_ context.Context, target *Target, revision int64) error {
	if m.target.Revision != revision {
		return ErrConflict
	}
	c := *target
	m.target = &c
	return nil
}
func (m *memoryStore) DeleteProfile(context.Context, string, string, string) error {
	if m.protected {
		return ErrInUse
	}
	m.profile = nil
	return nil
}
func (m *memoryStore) DeleteTarget(context.Context, string, string, string, string) error {
	if m.protected {
		return ErrInUse
	}
	m.target = nil
	return nil
}
func fixture() (*Service, *memoryStore, *authz) {
	st := &memoryStore{profile: &Profile{ID: "cloud_1", OrgID: "org_1", ProjectID: "proj_1", Name: "Production", Revision: 1}}
	a := &authz{}
	s := NewService(Deps{Store: st, Projects: projectLookup{}, Connections: connectionLookup{cloud: connections.GCP, status: connections.StatusReady}, Providers: providers.Registry(), Authz: a})
	return s, st, a
}

func TestServiceScopeAndPermissions(t *testing.T) {
	s, st, a := fixture()
	p := security.Principal{OrgID: "org_1"}
	if _, err := s.Get(t.Context(), p, "proj_other", "cloud_1"); !errors.Is(err, projects.ErrNotFound) {
		t.Fatalf("cross-project: %v", err)
	}
	if _, err := s.Get(t.Context(), security.Principal{OrgID: "org_other"}, "proj_1", "cloud_1"); !errors.Is(err, projects.ErrNotFound) {
		t.Fatalf("cross-organization: %v", err)
	}
	if st.reads != 0 {
		t.Fatal("read before project scope validation")
	}
	if _, err := s.Get(t.Context(), p, "proj_1", "cloud_other"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	denied := errors.New("denied")
	a.err = denied
	if _, err := s.Create(t.Context(), p, "proj_1", CreateInput{Name: "Cloud"}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if a.permission != security.OrganizationCloudManage || a.resource != "org_1" {
		t.Fatalf("manage check: %+v", a)
	}
	a.err = nil
	if _, err := s.Get(t.Context(), p, "proj_1", "cloud_1"); err != nil {
		t.Fatal(err)
	}
	if a.permission != security.CloudAccountRead {
		t.Fatal(a.permission)
	}
}
func TestServiceTargetsValidatePlacement(t *testing.T) {
	for _, tc := range []struct{ name, provider, connection, cloud, status string }{
		{"mismatch", "vercel", "conn_1", connections.GCP, connections.StatusReady},
		{"missing connection", "cloud-run", "conn_other", connections.GCP, connections.StatusReady},
		{"incomplete connection", "cloud-run", "conn_1", connections.GCP, connections.StatusIncomplete},
		{"unknown provider", "unknown", "conn_1", connections.GCP, connections.StatusReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, _ := fixture()
			s.connections = connectionLookup{cloud: tc.cloud, status: tc.status}
			_, err := s.CreateTarget(t.Context(), security.Principal{OrgID: "org_1"}, "proj_1", "cloud_1", CreateTargetInput{Name: "primary", Provider: tc.provider, ConnectionID: tc.connection})
			if !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if st.target != nil {
				t.Fatal("invalid placement persisted")
			}
		})
	}
	s, _, _ := fixture()
	p := security.Principal{OrgID: "org_1"}
	in := CreateTargetInput{Name: "primary", DisplayName: "Primary", Provider: "cloud-run", ConnectionID: "conn_1", Region: "us-central1"}
	target, err := s.CreateTarget(t.Context(), p, "proj_1", "cloud_1", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTarget(t.Context(), p, "proj_1", "cloud_1", in); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate slug: %v", err)
	}
	updated, err := s.UpdateTarget(t.Context(), p, "proj_1", "cloud_1", target.ID, UpdateTargetInput{DisplayName: "New name", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.DisplayName != "New name" || updated.Provider != target.Provider || updated.ConnectionID != target.ConnectionID || updated.Region != target.Region || updated.Name != target.Name {
		t.Fatalf("metadata edit changed placement: %+v", updated)
	}
	if _, err = s.UpdateTarget(t.Context(), p, "proj_1", "cloud_1", target.ID, UpdateTargetInput{DisplayName: "Stale", Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale target revision: %v", err)
	}
}
func TestServiceRevisionsAndProtectedDeletion(t *testing.T) {
	s, st, _ := fixture()
	p := security.Principal{OrgID: "org_1"}
	updated, err := s.Update(t.Context(), p, "proj_1", "cloud_1", UpdateInput{Name: "Renamed", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatal(updated)
	}
	if _, err = s.Update(t.Context(), p, "proj_1", "cloud_1", UpdateInput{Name: "Stale", Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	st.protected = true
	if err = s.Delete(t.Context(), p, "proj_1", "cloud_1"); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	st.target = &Target{ID: "target_1", OrgID: "org_1", ProjectID: "proj_1", CloudID: "cloud_1"}
	if err = s.DeleteTarget(t.Context(), p, "proj_1", "cloud_1", "target_1"); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
}

type auditRecorder struct{ events []projects.AuditEvent }

func (a *auditRecorder) Record(_ context.Context, event projects.AuditEvent) {
	a.events = append(a.events, event)
}
func TestServiceCreatesAndAuditsScopedCloud(t *testing.T) {
	s, st, _ := fixture()
	audit := &auditRecorder{}
	s.audit = audit
	p := security.Principal{OrgID: "org_1", UserID: "usr_1"}
	cloud, err := s.Create(t.Context(), p, "proj_1", CreateInput{Name: "  Production  "})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Name != "Production" || cloud.Revision != 1 || cloud.OrgID != p.OrgID || cloud.ProjectID != "proj_1" || cloud.ID == "" || cloud.CreatedAt.IsZero() || st.profile.ID != cloud.ID {
		t.Fatalf("cloud: %+v", cloud)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "cloud.create" || audit.events[0].ProjectID != "proj_1" || audit.events[0].Principal.OrgID != p.OrgID {
		t.Fatalf("audit: %+v", audit.events)
	}
	for _, value := range []string{"", "   "} {
		if _, err = s.Create(t.Context(), p, "proj_1", CreateInput{Name: value}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid name %q: %v", value, err)
		}
	}
}
func TestServiceTargetsRefusePausedProvider(t *testing.T) {
	s, st, _ := fixture()
	s.providers = providers.Registry("cloud-run")
	_, err := s.CreateTarget(t.Context(), security.Principal{OrgID: "org_1"}, "proj_1", "cloud_1", CreateTargetInput{Name: "primary", Provider: "cloud-run", ConnectionID: "conn_1"})
	if !errors.Is(err, ErrInvalid) || st.target != nil {
		t.Fatalf("paused provider accepted: %v", err)
	}
}

type usageStore struct {
	*memoryStore
}

func (u usageStore) CloudUsage(_ context.Context, org, project, cloud string) (*Usage, error) {
	return &Usage{CloudID: cloud, Environments: []EnvironmentUsage{{ProjectID: project, EnvironmentID: org}}}, nil
}

func TestServiceUsageScopesLikeGet(t *testing.T) {
	_, st, a := fixture()
	s := NewService(Deps{Store: usageStore{memoryStore: st}, Projects: projectLookup{}, Authz: a})
	p := security.Principal{OrgID: "org_1"}
	if _, err := s.Usage(t.Context(), p, "proj_other", "cloud_1"); !errors.Is(err, projects.ErrNotFound) {
		t.Fatalf("cross-project: %v", err)
	}
	if st.reads != 0 {
		t.Fatal("read before project scope validation")
	}
	if _, err := s.Usage(t.Context(), p, "proj_1", "cloud_other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a Cloud the project can't see: %v", err)
	}
	u, err := s.Usage(t.Context(), p, "proj_1", "cloud_1")
	if err != nil || u.CloudID != "cloud_1" || u.Environments[0].ProjectID != "proj_1" {
		t.Fatalf("usage: %+v %v", u, err)
	}
	if a.permission != security.CloudAccountRead {
		t.Fatal(a.permission)
	}
	denied := errors.New("denied")
	a.err = denied
	if _, err = s.Usage(t.Context(), p, "", "cloud_1"); !errors.Is(err, denied) || a.permission != security.OrganizationCloudManage {
		t.Fatalf("organization view needs Cloud management: %v %v", err, a.permission)
	}
}

// grantingStore records project grants on top of the memory store.
type grantingStore struct {
	*memoryStore
	grants int
}

func (g *grantingStore) SetProjectGrant(context.Context, string, string, string, []string, bool) (int64, error) {
	g.grants++
	return int64(g.grants), nil
}
func (g *grantingStore) GetProjectGrant(context.Context, string, string, string) (*ProjectGrant, error) {
	return nil, ErrNotFound
}

func TestPlatformOwnedCloudIsReadOnly(t *testing.T) {
	s, st, _ := fixture()
	st.profile = &Profile{ID: "cloud_1", OrgID: "org_1", Name: "Loomplane Cloud", PlatformOwned: true, Revision: 1}
	st.target = &Target{ID: "target_1", OrgID: "org_1", CloudID: "cloud_1", Name: "eu-west", DisplayName: "EU West", Runtime: "platform", Provider: "platform", Region: "eu-west", Revision: 1}
	st.granted = "proj_1"
	g := &grantingStore{memoryStore: st}
	s.store = g
	p := security.Principal{OrgID: "org_1", UserID: "user_1"}
	ctx := t.Context()

	if ErrPlatformOwned.Error() != "Loomplane Cloud is run by this instance and can't be changed here." {
		t.Fatalf("message = %q", ErrPlatformOwned.Error())
	}
	for _, project := range []string{"", "proj_1"} {
		checks := map[string]error{}
		_, checks["rename"] = s.Update(ctx, p, project, "cloud_1", UpdateInput{Name: "Mine now", Revision: 1})
		_, checks["default placement"] = s.SetDefaultPlacement(ctx, p, project, "cloud_1", SetDefaultPlacementInput{DefaultPlacement: &DefaultPlacement{Provider: "cloud-run", ConnectionID: "conn_1"}, Revision: 1})
		checks["delete"] = s.Delete(ctx, p, project, "cloud_1")
		_, checks["add target"] = s.CreateTarget(ctx, p, project, "cloud_1", CreateTargetInput{Name: "extra", Provider: "cloud-run", ConnectionID: "conn_1"})
		_, checks["update target"] = s.UpdateTarget(ctx, p, project, "cloud_1", "target_1", UpdateTargetInput{DisplayName: "Renamed", Revision: 1})
		checks["delete target"] = s.DeleteTarget(ctx, p, project, "cloud_1", "target_1")
		for name, err := range checks {
			if !errors.Is(err, ErrPlatformOwned) {
				t.Errorf("project %q %s: err = %v, want ErrPlatformOwned", project, name, err)
			}
		}
	}
	org := map[string]error{}
	_, org["save location"] = s.SaveLocation(ctx, p, "cloud_1", LocationInput{Name: "x", Provider: "cloud-run", ConnectionID: "conn_1"})
	_, org["verify location"] = s.VerifyLocation(ctx, p, "cloud_1", "location_1", LocationVerificationInput{})
	org["delete location"] = s.DeleteLocation(ctx, p, "cloud_1", "location_1", 1, 1)
	_, org["location policy"] = s.SaveLocationPolicy(ctx, p, "cloud_1", LocationPolicy{Ranking: "latency"})
	for name, err := range org {
		if !errors.Is(err, ErrPlatformOwned) {
			t.Errorf("%s: err = %v, want ErrPlatformOwned", name, err)
		}
	}
	if st.profile == nil || st.profile.Name != "Loomplane Cloud" || st.profile.Revision != 1 || st.profile.DefaultPlacement != nil {
		t.Fatalf("profile changed: %+v", st.profile)
	}
	if st.target == nil || st.target.Name != "eu-west" || st.target.DisplayName != "EU West" || st.target.Revision != 1 {
		t.Fatalf("target changed: %+v", st.target)
	}

	// A workspace uses the platform Cloud through a grant, so granting stays open.
	grant, err := s.GrantProject(ctx, p, "cloud_1", "proj_1", nil)
	if err != nil || grant.Revision != 1 || g.grants != 1 {
		t.Fatalf("GrantProject = %+v, %v", grant, err)
	}
	// Reads stay open too.
	if _, err = s.Get(ctx, p, "proj_1", "cloud_1"); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRefusesUserPlatformTargets(t *testing.T) {
	s, st, _ := fixture()
	_, err := s.CreateTarget(t.Context(), security.Principal{OrgID: "org_1"}, "proj_1", "cloud_1", CreateTargetInput{Name: "sneaky", Runtime: "platform", Provider: "platform", Region: "eu-west"})
	if !errors.Is(err, ErrInvalid) || st.target != nil {
		t.Fatalf("user platform target: %v %+v", err, st.target)
	}
}

func TestServiceReservesPlatformCloudNames(t *testing.T) {
	s, st, _ := fixture()
	p := security.Principal{OrgID: "org_1"}
	for _, reserved := range []string{PlatformCloudName, " loomplane cloud ", PlatformCloudFallbackName} {
		if _, err := s.Create(t.Context(), p, "proj_1", CreateInput{Name: reserved}); !errors.Is(err, ErrExists) {
			t.Fatalf("create %q: %v, want ErrExists", reserved, err)
		}
		if _, err := s.Update(t.Context(), p, "proj_1", "cloud_1", UpdateInput{Name: reserved, Revision: 1}); !errors.Is(err, ErrExists) {
			t.Fatalf("rename to %q: %v, want ErrExists", reserved, err)
		}
	}
	if st.profile.Name != "Production" {
		t.Fatalf("profile renamed to %q", st.profile.Name)
	}
	if _, err := s.Create(t.Context(), p, "proj_1", CreateInput{Name: "Loomplane Cloud staging"}); err != nil {
		t.Fatalf("a longer name is fine: %v", err)
	}
}

// Accepting a target change rechecks the locked target. A Loomplane Cloud
// target takes its own branch: it is never changed through a sync, even
// when nothing about it moved since the review.
func TestSyncTargetCheckRefusesPlatformTarget(t *testing.T) {
	managed := Target{ID: "t", Runtime: "managed", Provider: "vercel", ConnectionID: "a", Region: "iad1", Revision: 2}
	if err := checkSyncTarget(managed, 2, &managed); err != nil {
		t.Fatalf("unchanged managed target: %v", err)
	}
	moved := managed
	moved.ConnectionID = "b"
	if err := checkSyncTarget(moved, 2, &managed); !errors.Is(err, ErrConflict) {
		t.Fatalf("moved managed target: %v", err)
	}
	platform := Target{ID: "p", Runtime: "platform", Provider: "platform", Region: "fra", Revision: 1}
	if err := checkSyncTarget(platform, 1, &platform); !errors.Is(err, ErrPlatformOwned) {
		t.Fatalf("platform target: %v", err)
	}
}

// A workspace's own Cloud never takes a Loomplane Cloud target or default,
// and the refusal says why instead of blaming a missing connection.
func TestServiceRefusesPlatformProviderOnUserCloud(t *testing.T) {
	s, st, _ := fixture()
	p := security.Principal{OrgID: "org_1"}
	_, err := s.CreateTarget(t.Context(), p, "proj_1", "cloud_1", CreateTargetInput{Name: "fra", Provider: "platform", ConnectionID: "conn_1", Region: "fra"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Loomplane Cloud targets are created by this instance") || st.target != nil {
		t.Fatalf("platform provider target: %v %+v", err, st.target)
	}
	_, err = s.SetDefaultPlacement(t.Context(), p, "proj_1", "cloud_1", SetDefaultPlacementInput{Revision: 1, DefaultPlacement: &DefaultPlacement{Provider: "platform", ConnectionID: "conn_1"}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Loomplane Cloud targets are created by this instance") {
		t.Fatalf("platform default: %v", err)
	}
}
