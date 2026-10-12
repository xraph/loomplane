package projects

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

const org = "aorg_test"

// memStore is an in-memory Store for the service's tests. PgStore's own
// behaviour is covered against Postgres in tests/integration.
type memStore struct {
	mu       sync.Mutex
	projects map[string]*Project
	envs     map[string]*Environment
}

func newMemStore() *memStore {
	return &memStore{projects: map[string]*Project{}, envs: map[string]*Environment{}}
}

func (m *memStore) CreateProject(_ context.Context, p *Project, envs []*Environment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, q := range m.projects {
		if q.OrgID == p.OrgID && q.Slug == p.Slug {
			return ErrSlugTaken
		}
	}
	c := *p
	m.projects[p.ID] = &c
	for _, e := range envs {
		ec := *e
		m.envs[e.ID] = &ec
	}
	return nil
}

func (m *memStore) GetProject(_ context.Context, orgID, id string) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *p
	return &c, nil
}

func (m *memStore) ListProjects(_ context.Context, orgID string) ([]*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Project{}
	for _, p := range m.projects {
		if p.OrgID == orgID {
			c := *p
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *memStore) UpdateProject(_ context.Context, p *Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q, ok := m.projects[p.ID]; !ok || q.OrgID != p.OrgID {
		return ErrNotFound
	}
	c := *p
	m.projects[p.ID] = &c
	return nil
}

func (m *memStore) DeleteProject(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[id]; !ok || p.OrgID != orgID {
		return ErrNotFound
	}
	delete(m.projects, id)
	for k, e := range m.envs {
		if e.ProjectID == id {
			delete(m.envs, k)
		}
	}
	return nil
}

func (m *memStore) CreateEnvironment(_ context.Context, e *Environment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[e.ProjectID]; !ok || p.OrgID != e.OrgID {
		return ErrNotFound
	}
	for _, x := range m.envs {
		if x.ProjectID == e.ProjectID && x.Name == e.Name {
			return ErrEnvironmentExists
		}
	}
	c := *e
	m.envs[e.ID] = &c
	return nil
}

func (m *memStore) GetEnvironment(_ context.Context, orgID, projectID, id string) (*Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok || e.OrgID != orgID || e.ProjectID != projectID {
		return nil, ErrEnvironmentNotFound
	}
	c := *e
	return &c, nil
}

func (m *memStore) DeleteEnvironment(_ context.Context, orgID, projectID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok || e.OrgID != orgID || e.ProjectID != projectID {
		return ErrEnvironmentNotFound
	}
	n := 0
	for _, x := range m.envs {
		if x.ProjectID == projectID {
			n++
		}
	}
	if n <= 1 {
		return ErrLastEnvironment
	}
	delete(m.envs, id)
	return nil
}

func (m *memStore) ListEnvironments(_ context.Context, orgID, projectID string) ([]*Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Environment{}
	for _, e := range m.envs {
		if e.OrgID == orgID && e.ProjectID == projectID {
			c := *e
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// initRecorder stands in for the Dispatch enqueuer.
type initRecorder struct {
	queued []string
	err    error
}

func (r *initRecorder) InitializeProject(_ context.Context, _, projectID string) error {
	if r.err != nil {
		return r.err
	}
	r.queued = append(r.queued, projectID)
	return nil
}

type auditRecorder struct {
	actions []string
	events  []AuditEvent
}

func (a *auditRecorder) Record(_ context.Context, e AuditEvent) {
	a.actions = append(a.actions, e.Action)
	a.events = append(a.events, e)
}

type fixture struct {
	svc   *Service
	store *memStore
	init  *initRecorder
	audit *auditRecorder
	authz *security.Authorizer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		store: newMemStore(), init: &initRecorder{}, audit: &auditRecorder{},
		authz: security.NewAuthorizer(eng, ws, nil),
	}
	f.svc = NewService(f.store, f.authz, f.init, f.audit)
	return f
}

func (f fixture) principal(t *testing.T, user, role string) security.Principal {
	t.Helper()
	if err := f.authz.Assign(context.Background(), org, user, role, "test"); err != nil {
		t.Fatal(err)
	}
	return security.Principal{UserID: user, OrgID: org}
}

func TestService_createInitializeAndRead(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)

	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "  Difference Engine ", RepositoryURL: "git@github.com:ada/engine.git"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Slug != "difference-engine" || p.Name != "Difference Engine" || p.Status != StatusInitializing || p.OrgID != org || p.CreatedBy != "ausr_dev" {
		t.Fatalf("created %+v", p)
	}
	if len(f.init.queued) != 1 || f.init.queued[0] != p.ID {
		t.Fatalf("initialization queued for %v", f.init.queued)
	}
	// Environments exist from the start, before the job runs.
	if envs, eerr := f.svc.Environments(ctx, dev, p.ID); eerr != nil || len(envs) != len(DefaultEnvironments()) {
		t.Fatalf("environments before initialization: %d, %v", len(envs), eerr)
	}

	// The job may run twice; the second run must change nothing.
	for range 2 {
		if err = f.svc.Initialize(ctx, org, p.ID); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	got, err := f.svc.Get(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusReady {
		t.Fatalf("status %q after initialization", got.Status)
	}
	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != len(DefaultEnvironments()) {
		t.Fatalf("%d environments, want %d", len(envs), len(DefaultEnvironments()))
	}
	if strings.Join(f.audit.actions, ",") != "project.create" {
		t.Fatalf("audit %v", f.audit.actions)
	}
}

func TestService_rolesAreEnforced(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	viewer := f.principal(t, "ausr_viewer", security.RoleViewer)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	stranger := security.Principal{UserID: "ausr_stranger", OrgID: org}

	if _, err := f.svc.Create(ctx, viewer, CreateProjectInput{Name: "x"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer create: %v", err)
	}
	if _, err := f.svc.List(ctx, stranger); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("stranger list: %v", err)
	}
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.List(ctx, viewer); err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	if err := f.svc.Delete(ctx, dev, p.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer delete: %v", err)
	}
	if err := f.svc.Delete(ctx, admin, p.ID); err != nil {
		t.Fatalf("admin delete: %v", err)
	}
	if _, err := f.svc.Get(ctx, admin, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestService_otherOrganizationsAreInvisible(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	other := security.Principal{UserID: "ausr_other", OrgID: "aorg_other"}
	if err = f.authz.Assign(ctx, other.OrgID, other.UserID, security.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Get(ctx, other, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org reading the project: %v, want ErrNotFound", err)
	}
	list, err := f.svc.List(ctx, other)
	if err != nil || len(list) != 0 {
		t.Fatalf("other org lists %d projects (%v)", len(list), err)
	}
}

func TestService_createRollsBackWhenEnqueueFails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	f.init.err = errors.New("dispatch is down")
	if _, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"}); err == nil {
		t.Fatal("Create succeeded without its initialization job")
	}
	list, err := f.svc.List(ctx, dev)
	if err != nil || len(list) != 0 {
		t.Fatalf("a stranded project was left behind: %d (%v)", len(list), err)
	}
}

func TestService_validation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	tests := []struct {
		name string
		in   CreateProjectInput
	}{
		{"empty name", CreateProjectInput{Name: "   "}},
		{"long name", CreateProjectInput{Name: strings.Repeat("a", maxNameLength+1)}},
		{"name with no slug", CreateProjectInput{Name: "!!!"}},
		{"bad slug", CreateProjectInput{Name: "ok", Slug: "Not_A_Slug"}},
		{"double hyphen", CreateProjectInput{Name: "ok", Slug: "a--b"}},
		{"http repository", CreateProjectInput{Name: "ok", RepositoryURL: "http://github.com/a/b"}},
		{"credentials in repository", CreateProjectInput{Name: "ok", RepositoryURL: "https://me:token@github.com/a/b"}},
		{"long description", CreateProjectInput{Name: "ok", Description: strings.Repeat("d", maxDescriptionLength+1)}},
		{"unknown icon", CreateProjectInput{Name: "ok", Icon: "unicorn"}},
		{"lucide's name for an icon", CreateProjectInput{Name: "ok", Icon: "shopping-bag"}},
		{"icon in the wrong case", CreateProjectInput{Name: "ok", Icon: "Store"}},
		{"unknown color", CreateProjectInput{Name: "ok", Color: "chartreuse"}},
		{"color as hex", CreateProjectInput{Name: "ok", Color: "#ffcc00"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.Create(ctx, dev, tt.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v, want ErrInvalid", err)
			}
		})
	}
}

func TestService_updateKeepsSlug(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	name, desc := "Analytical Engine", "Now with loops"
	got, err := f.svc.Update(ctx, dev, p.ID, UpdateProjectInput{Name: &name, Description: &desc})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != name || got.Description != desc || got.Slug != "engine" {
		t.Fatalf("updated %+v", got)
	}
	empty := " "
	if _, err := f.svc.Update(ctx, dev, p.ID, UpdateProjectInput{Name: &empty}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name: %v", err)
	}
}

func TestService_createStyle(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	// An empty want means any of the names will do: the service picked.
	tests := []struct {
		name                string
		icon, color         string
		wantIcon, wantColor string
	}{
		{name: "both picked when left out"},
		{name: "both kept when given", icon: "rocket", color: "teal", wantIcon: "rocket", wantColor: "teal"},
		{name: "color picked when only the icon is given", icon: "atom", wantIcon: "atom"},
		{name: "icon picked when only the color is given", color: "slate", wantColor: "slate"},
		{name: "spaces trimmed", icon: " leaf ", color: " green ", wantIcon: "leaf", wantColor: "green"},
		{name: "blank picks", icon: "  ", color: "  "},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine " + strings.Repeat("i", i+1), Icon: tt.icon, Color: tt.color})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			checkStyle(t, p.Icon, tt.wantIcon, icons)
			checkStyle(t, p.Color, tt.wantColor, colors)
			got, err := f.svc.Get(ctx, dev, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Icon != p.Icon || got.Color != p.Color {
				t.Fatalf("stored %s/%s, created %s/%s", got.Icon, got.Color, p.Icon, p.Color)
			}
		})
	}
}

// checkStyle fails unless got is want, or, with no want, one of names.
func checkStyle(t *testing.T, got, want string, names []string) {
	t.Helper()
	if want != "" && got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !slices.Contains(names, got) {
		t.Fatalf("%q isn't one of %v", got, names)
	}
}

func TestService_createPicksAVarietyOfStyles(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	// A fair pick gives twenty projects the same color once in 10^19 runs,
	// so this only fails when the pick stops being random.
	const projectCount = 20
	seenIcons, seenColors := map[string]bool{}, map[string]bool{}
	for i := range projectCount {
		p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine " + strings.Repeat("i", i+1)})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		seenIcons[p.Icon], seenColors[p.Color] = true, true
	}
	if len(seenIcons) < 2 || len(seenColors) < 2 {
		t.Fatalf("%d projects wore %d icons and %d colors", projectCount, len(seenIcons), len(seenColors))
	}
}

func TestService_updateStyle(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine", Icon: "store", Color: "gold"})
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	changes := []struct {
		name                string
		in                  UpdateProjectInput
		wantIcon, wantColor string
		wantFields          []string
	}{
		{"icon only", UpdateProjectInput{Icon: str("rocket")}, "rocket", "gold", []string{"icon"}},
		{"color only", UpdateProjectInput{Color: str(" teal ")}, "rocket", "teal", []string{"color"}},
		{"both", UpdateProjectInput{Icon: str("hexagon"), Color: str("slate")}, "hexagon", "slate", []string{"icon", "color"}},
		{"neither", UpdateProjectInput{Name: str("Analytical Engine")}, "hexagon", "slate", []string{"name"}},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			got, uerr := f.svc.Update(ctx, dev, p.ID, tt.in)
			if uerr != nil {
				t.Fatalf("Update: %v", uerr)
			}
			if got.Icon != tt.wantIcon || got.Color != tt.wantColor {
				t.Fatalf("updated to %s/%s, want %s/%s", got.Icon, got.Color, tt.wantIcon, tt.wantColor)
			}
			stored, gerr := f.svc.Get(ctx, dev, p.ID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if stored.Icon != tt.wantIcon || stored.Color != tt.wantColor {
				t.Fatalf("stored %s/%s, want %s/%s", stored.Icon, stored.Color, tt.wantIcon, tt.wantColor)
			}
			last := f.audit.events[len(f.audit.events)-1]
			if fields, _ := last.Attributes["fields"].([]string); last.Action != "project.update" || !slices.Equal(fields, tt.wantFields) {
				t.Fatalf("audited %s %v, want project.update %v", last.Action, last.Attributes, tt.wantFields)
			}
		})
	}

	refusals := []struct {
		name string
		in   UpdateProjectInput
	}{
		{"unknown icon", UpdateProjectInput{Icon: str("unicorn")}},
		{"unknown color", UpdateProjectInput{Color: str("chartreuse")}},
		{"empty icon", UpdateProjectInput{Icon: str("")}},
		{"blank color", UpdateProjectInput{Color: str("  ")}},
		{"good icon, unknown color", UpdateProjectInput{Icon: str("zap"), Color: str("mauve")}},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if _, uerr := f.svc.Update(ctx, dev, p.ID, tt.in); !errors.Is(uerr, ErrInvalid) {
				t.Fatalf("error %v, want ErrInvalid", uerr)
			}
			stored, gerr := f.svc.Get(ctx, dev, p.ID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if stored.Icon != "hexagon" || stored.Color != "slate" {
				t.Fatalf("a refused update left %s/%s behind", stored.Icon, stored.Color)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Difference Engine":         "difference-engine",
		"  API -- Gateway!! ":       "api-gateway",
		"Ünïcödé Service":           "n-c-d-service",
		"already-a-slug":            "already-a-slug",
		"!!!":                       "",
		strings.Repeat("ab ", 30):   "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab",
		"Service 2 (EU) / Frontend": "service-2-eu-frontend",
	}
	for in, want := range tests {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if got := Slugify(in); got != "" && validateSlug(got) != nil {
			t.Errorf("Slugify(%q) = %q, which isn't a valid slug", in, got)
		}
	}
}

type sweepCounter struct{ orgs []string }

func (c *sweepCounter) Sweep(_ context.Context, orgID string) { c.orgs = append(c.orgs, orgID) }

func TestService_environments(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	sweeps := &sweepCounter{}
	f.svc.AddCleaner(sweeps)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.principal(t, "ausr_viewer", security.RoleViewer)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}

	staging, err := f.svc.CreateEnvironment(ctx, dev, p.ID, CreateEnvironmentInput{Name: " staging "})
	if err != nil || staging.Name != "staging" || staging.Kind != KindStaging {
		t.Fatalf("create staging: %+v, %v", staging, err)
	}
	qa, err := f.svc.CreateEnvironment(ctx, dev, p.ID, CreateEnvironmentInput{Name: "qa-eu"})
	if err != nil || qa.Kind != KindDevelopment {
		t.Fatalf("create qa-eu: %+v, %v", qa, err)
	}
	refusals := []struct {
		name string
		p    security.Principal
		in   CreateEnvironmentInput
		want error
	}{
		{"viewer", viewer, CreateEnvironmentInput{Name: "demo"}, security.ErrForbidden},
		{"taken", dev, CreateEnvironmentInput{Name: "production"}, ErrEnvironmentExists},
		{"upper case", dev, CreateEnvironmentInput{Name: "Prod"}, ErrInvalid},
		{"double hyphen", dev, CreateEnvironmentInput{Name: "a--b"}, ErrInvalid},
		{"unknown kind", dev, CreateEnvironmentInput{Name: "demo", Kind: "canary"}, ErrInvalid},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if _, cerr := f.svc.CreateEnvironment(ctx, tt.p, p.ID, tt.in); !errors.Is(cerr, tt.want) {
				t.Fatalf("CreateEnvironment: %v, want %v", cerr, tt.want)
			}
		})
	}

	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range envs {
		if i == len(envs)-1 {
			if err := f.svc.DeleteEnvironment(ctx, dev, p.ID, e.ID); !errors.Is(err, ErrLastEnvironment) {
				t.Fatalf("deleting the last environment: %v", err)
			}
			break
		}
		if err := f.svc.DeleteEnvironment(ctx, dev, p.ID, e.ID); err != nil {
			t.Fatalf("delete %s: %v", e.Name, err)
		}
	}
	if left, _ := f.svc.Environments(ctx, dev, p.ID); len(left) != 1 {
		t.Fatalf("%d environments left", len(left))
	}
	if len(sweeps.orgs) != len(envs)-1 {
		t.Fatalf("cleaners ran %d times", len(sweeps.orgs))
	}
	if err := f.svc.DeleteEnvironment(ctx, dev, p.ID, "env_missing"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("delete a missing environment: %v", err)
	}
}
