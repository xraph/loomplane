package environments

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

const (
	org     = "aorg_test"
	project = "proj_shop"
	webSvc  = "svc:node:apps/web"
	apiSvc  = "svc:go:services/api"
)

func v(scope, env, service, key, value string) *Variable {
	pid := project
	if scope == ScopeOrganization {
		pid = ""
	}
	return &Variable{ID: scope + "/" + env + "/" + service + "/" + key, Scope: scope, ProjectID: pid, Environment: env, Service: service, Key: key, Value: value}
}

func TestResolve_theMostSpecificLayerWins(t *testing.T) {
	candidates := []*Variable{
		v(ScopeOrganization, "", "", "LOG_LEVEL", "info"),
		v(ScopeOrganization, "production", "", "LOG_LEVEL", "warn"),
		v(ScopeProject, "", "", "LOG_LEVEL", "debug"),
		v(ScopeOrganization, "", "", "REGION", "eu-west-1"),
		v(ScopeProject, "production", "", "API_URL", "https://api.example.com"),
		v(ScopeService, "", webSvc, "API_URL", "http://api.internal"),
		v(ScopeService, "production", webSvc, "PORT", "3000"),
		v(ScopeService, "", apiSvc, "PORT", "8080"),
	}
	tests := []struct {
		name    string
		service string
		want    map[string]string
	}{
		{"project as a whole", "", map[string]string{
			"LOG_LEVEL": "debug", "REGION": "eu-west-1", "API_URL": "https://api.example.com",
		}},
		{"web", webSvc, map[string]string{
			"LOG_LEVEL": "debug", "REGION": "eu-west-1", "API_URL": "http://api.internal", "PORT": "3000",
		}},
		{"api", apiSvc, map[string]string{
			"LOG_LEVEL": "debug", "REGION": "eu-west-1", "API_URL": "https://api.example.com", "PORT": "8080",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(candidates, tt.service)
			if len(got) != len(tt.want) {
				t.Fatalf("resolved %+v", got)
			}
			for _, rv := range got {
				if tt.want[rv.Key] != rv.Variable.Value {
					t.Fatalf("%s = %q, want %q", rv.Key, rv.Variable.Value, tt.want[rv.Key])
				}
			}
		})
	}
	// LOG_LEVEL's project value shadows both organization ones, the
	// production one first.
	for _, rv := range resolve(candidates, "") {
		if rv.Key != "LOG_LEVEL" {
			continue
		}
		if len(rv.Overrides) != 2 || rv.Overrides[0].Value != "warn" || rv.Overrides[1].Value != "info" {
			t.Fatalf("overrides %+v", rv.Overrides)
		}
	}
}

// memStore is an in-memory Store for these tests. PgStore is covered
// against Postgres in tests/integration.
type memStore struct {
	mu   sync.Mutex
	vars map[string]*Variable
}

func (m *memStore) CreateVariable(_ context.Context, nv *Variable) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.vars {
		if x.OrgID == nv.OrgID && x.ProjectID == nv.ProjectID && x.Service == nv.Service && x.Environment == nv.Environment && x.Key == nv.Key {
			return ErrExists
		}
	}
	c := *nv
	m.vars[nv.ID] = &c
	return nil
}

func (m *memStore) GetVariable(_ context.Context, orgID, id string) (*Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.vars[id]
	if !ok || x.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *x
	return &c, nil
}

func (m *memStore) UpdateVariable(_ context.Context, nv *Variable) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.vars[nv.ID]
	if !ok || x.OrgID != nv.OrgID {
		return ErrNotFound
	}
	x.Value, x.Secret, x.UpdatedBy, x.UpdatedAt = nv.Value, nv.Secret, nv.UpdatedBy, nv.UpdatedAt
	return nil
}

func (m *memStore) DeleteVariable(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if x, ok := m.vars[id]; !ok || x.OrgID != orgID {
		return ErrNotFound
	}
	delete(m.vars, id)
	return nil
}

func (m *memStore) filter(keep func(*Variable) bool) []*Variable {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Variable{}
	for _, x := range m.vars {
		if keep(x) {
			c := *x
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (m *memStore) ListOrganization(_ context.Context, orgID string) ([]*Variable, error) {
	return m.filter(func(x *Variable) bool { return x.OrgID == orgID && x.ProjectID == "" }), nil
}

func (m *memStore) ListProject(_ context.Context, orgID, projectID string) ([]*Variable, error) {
	return m.filter(func(x *Variable) bool { return x.OrgID == orgID && x.ProjectID == projectID }), nil
}

func (m *memStore) ForEnvironment(_ context.Context, orgID, projectID, envID, envName string) ([]*Variable, error) {
	return m.filter(func(x *Variable) bool {
		if x.OrgID != orgID {
			return false
		}
		if x.ProjectID == "" {
			return x.Environment == "" || x.Environment == envName
		}
		return x.ProjectID == projectID && (x.environmentID == "" || x.environmentID == envID)
	}), nil
}

func (m *memStore) SecretIDs(_ context.Context, orgID string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, x := range m.filter(func(x *Variable) bool { return x.OrgID == orgID && x.Secret }) {
		out[x.ID] = true
	}
	return out, nil
}

type fakeProjects struct{}

func (fakeProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	if orgID != org || id != project {
		return nil, projects.ErrNotFound
	}
	return &projects.Project{ID: project, OrgID: org}, nil
}

func (fakeProjects) ListEnvironments(_ context.Context, orgID, projectID string) ([]*projects.Environment, error) {
	if orgID != org || projectID != project {
		return nil, projects.ErrNotFound
	}
	return []*projects.Environment{
		{ID: "env_prod", Name: "production", Kind: projects.KindProduction},
		{ID: "env_preview", Name: "preview", Kind: projects.KindPreview},
	}, nil
}

type fakeServices struct{}

func (fakeServices) Services(context.Context, string, string) (map[string]string, error) {
	return map[string]string{webSvc: "web", apiSvc: "api"}, nil
}

type fixture struct {
	svc     *Service
	store   *memStore
	secrets *VaultSecrets
	authz   *security.Authorizer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		store:   &memStore{vars: map[string]*Variable{}},
		secrets: NewVaultSecrets(secret.NewService(vaultmem.New(), enc)),
		authz:   security.NewAuthorizer(eng, ws, nil),
	}
	f.svc = NewService(f.store, f.secrets, fakeProjects{}, fakeServices{}, f.authz, nil, func(msg string, err error) {
		t.Errorf("%s: %v", msg, err)
	})
	return f
}

func (f *fixture) as(t *testing.T, user, role string) security.Principal {
	t.Helper()
	if err := f.authz.Assign(context.Background(), org, user, role, "test"); err != nil {
		t.Fatal(err)
	}
	return security.Principal{UserID: user, OrgID: org}
}

func TestService_whoMaySetWhat(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	admin := f.as(t, "ausr_admin", security.RoleAdmin)

	if _, err := f.svc.CreateOrganization(ctx, dev, CreateVariableInput{Key: "REGION", Value: "eu"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer setting an organization variable: %v", err)
	}
	if _, err := f.svc.CreateProject(ctx, viewer, project, CreateVariableInput{Key: "A", Value: "1"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a viewer setting a project variable: %v", err)
	}
	global, err := f.svc.CreateOrganization(ctx, admin, CreateVariableInput{Key: "REGION", Value: "eu", Environment: "staging"})
	if err != nil || global.Scope != ScopeOrganization || global.Environment != "staging" {
		t.Fatalf("organization variable: %+v, %v", global, err)
	}
	if _, err = f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "A", Value: "1"}); err != nil {
		t.Fatalf("a developer setting a project variable: %v", err)
	}
	if err = f.svc.Delete(ctx, dev, global.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer deleting an organization variable: %v", err)
	}

	refusals := []struct {
		name string
		in   CreateVariableInput
		want error
	}{
		{"bad key", CreateVariableInput{Key: "1BAD", Value: "x"}, ErrInvalid},
		{"key with a dash", CreateVariableInput{Key: "MY-KEY", Value: "x"}, ErrInvalid},
		{"unknown environment", CreateVariableInput{Key: "B", Value: "x", Environment: "staging"}, ErrInvalid},
		{"unknown service", CreateVariableInput{Key: "B", Value: "x", Service: "svc:go:gone"}, ErrInvalid},
		{"taken", CreateVariableInput{Key: "A", Value: "2"}, ErrExists},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if _, cerr := f.svc.CreateProject(ctx, dev, project, tt.in); !errors.Is(cerr, tt.want) {
				t.Fatalf("CreateProject: %v, want %v", cerr, tt.want)
			}
		})
	}
	// An unknown service is named against the whole graph, with a hint at
	// what an ID looks like now that it carries the repository's alias.
	_, err = f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "B", Value: "x", Service: "svc:go:gone"})
	if err == nil || !strings.Contains(err.Error(), "isn't a service in the project's graph") ||
		!strings.Contains(err.Error(), "svc:<alias>:<ecosystem>:<path>") {
		t.Fatalf("unknown service: %v", err)
	}
	// The same key on another layer, or for one environment, isn't taken.
	if _, err = f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "A", Value: "3", Environment: "production"}); err != nil {
		t.Fatalf("A for production: %v", err)
	}
	if _, err = f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "A", Value: "4", Service: webSvc}); err != nil {
		t.Fatalf("A for web: %v", err)
	}
}

func TestService_secretsAreWrittenNeverRead(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	sec, err := f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "DATABASE_URL", Value: "postgres://one", Secret: true, Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Value != "" || !sec.Secret {
		t.Fatalf("created secret came back with its value: %+v", sec)
	}
	if got, _ := f.store.GetVariable(ctx, org, sec.ID); got.Value != "" {
		t.Fatalf("the store holds the secret's value: %q", got.Value)
	}
	if val, rerr := f.secrets.Reveal(ctx, org, sec.ID); rerr != nil || string(val) != "postgres://one" {
		t.Fatalf("Vault holds %q, %v", val, rerr)
	}

	res, err := f.svc.Resolve(ctx, dev, project, "production", "")
	if err != nil || len(res.Variables) != 1 || res.Variables[0].Variable.Value != "" || !res.Variables[0].Variable.Secret {
		t.Fatalf("resolution %+v, %v", res, err)
	}

	if _, err = f.svc.Update(ctx, dev, sec.ID, UpdateVariableInput{Value: "postgres://two"}); err != nil {
		t.Fatal(err)
	}
	if val, _ := f.secrets.Reveal(ctx, org, sec.ID); string(val) != "postgres://two" {
		t.Fatalf("rotated secret is %q", val)
	}

	// No longer secret: the value moves into the open, out of Vault.
	plain := false
	got, err := f.svc.Update(ctx, dev, sec.ID, UpdateVariableInput{Value: "postgres://three", Secret: &plain})
	if err != nil || got.Value != "postgres://three" || got.Secret {
		t.Fatalf("made plain: %+v, %v", got, err)
	}
	if _, err = f.secrets.Reveal(ctx, org, sec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old secret is still in Vault: %v", err)
	}

	again, err := f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "TOKEN", Value: "t", Secret: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Delete(ctx, dev, again.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.secrets.Reveal(ctx, org, again.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted variable's secret is still in Vault: %v", err)
	}
}

func TestService_sweepRemovesSecretsWithoutAVariable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	keep, err := f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "KEEP", Value: "k", Secret: true})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "GONE", Value: "g", Secret: true})
	if err != nil {
		t.Fatal(err)
	}
	// What a project deletion does in the database: the row goes, Vault
	// doesn't hear about it.
	if err = f.store.DeleteVariable(ctx, org, gone.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.secrets.Put(ctx, "aorg_other", "var_theirs", []byte("x")); err != nil {
		t.Fatal(err)
	}
	f.svc.Sweep(ctx, org)
	if _, err = f.secrets.Reveal(ctx, org, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the orphaned secret survived: %v", err)
	}
	if _, err = f.secrets.Reveal(ctx, org, keep.ID); err != nil {
		t.Fatalf("a live secret was swept: %v", err)
	}
	if _, err = f.secrets.Reveal(ctx, "aorg_other", "var_theirs"); err != nil {
		t.Fatalf("another organization's secret was swept: %v", err)
	}
}

func TestService_resolveShowsWhereEachValueComesFrom(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	admin := f.as(t, "ausr_admin", security.RoleAdmin)
	steps := []struct {
		p   security.Principal
		org bool
		in  CreateVariableInput
	}{
		{admin, true, CreateVariableInput{Key: "LOG_LEVEL", Value: "info"}},
		{admin, true, CreateVariableInput{Key: "LOG_LEVEL", Value: "warn", Environment: "production"}},
		{admin, true, CreateVariableInput{Key: "ONLY_STAGING", Value: "x", Environment: "staging"}},
		{dev, false, CreateVariableInput{Key: "LOG_LEVEL", Value: "debug", Environment: "preview"}},
		{dev, false, CreateVariableInput{Key: "PORT", Value: "3000", Service: webSvc}},
	}
	for _, st := range steps {
		var err error
		if st.org {
			_, err = f.svc.CreateOrganization(ctx, st.p, st.in)
		} else {
			_, err = f.svc.CreateProject(ctx, st.p, project, st.in)
		}
		if err != nil {
			t.Fatalf("set %s: %v", st.in.Key, err)
		}
	}
	prod, err := f.svc.Resolve(ctx, dev, project, "production", webSvc)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, rv := range prod.Variables {
		got[rv.Key] = rv.Variable.Value + "@" + rv.Variable.Scope
	}
	if len(got) != 2 || got["LOG_LEVEL"] != "warn@organization" || got["PORT"] != "3000@service" {
		t.Fatalf("production for web: %v", got)
	}
	preview, err := f.svc.Resolve(ctx, dev, project, "preview", "")
	if err != nil || len(preview.Variables) != 1 || preview.Variables[0].Variable.Value != "debug" || len(preview.Variables[0].Overrides) != 1 {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if _, err := f.svc.Resolve(ctx, dev, project, "staging", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolving an environment the project doesn't have: %v", err)
	}
}
