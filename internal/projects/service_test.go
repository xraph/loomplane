package projects

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
	teams    map[string]string // team ID to its organization
	teamSets []string          // "org/project/team" for every SetResponsibleTeam
}

func newMemStore() *memStore {
	return &memStore{projects: map[string]*Project{}, envs: map[string]*Environment{}, teams: map[string]string{}}
}

func (m *memStore) SetResponsibleTeam(_ context.Context, orgID, id, teamID string) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teamSets = append(m.teamSets, orgID+"/"+id+"/"+teamID)
	p, ok := m.projects[id]
	if !ok || p.OrgID != orgID {
		return nil, ErrNotFound
	}
	if teamID != "" && m.teams[teamID] != orgID {
		return nil, ErrTeamNotFound
	}
	p.ResponsibleTeamID = teamID
	c := *p
	return &c, nil
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

// UpdateProject writes what PgStore's does: name, description, icon,
// color, status and updated_at.
func (m *memStore) UpdateProject(_ context.Context, p *Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.projects[p.ID]
	if !ok || q.OrgID != p.OrgID {
		return ErrNotFound
	}
	q.Name, q.Description, q.Icon, q.Color, q.Status, q.UpdatedAt = p.Name, p.Description, p.Icon, p.Color, p.Status, p.UpdatedAt
	return nil
}

func (m *memStore) SetAutonomy(_ context.Context, orgID, id string, in AutonomyInput, at time.Time) (*Project, Autonomy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.OrgID != orgID {
		return nil, Autonomy{}, ErrNotFound
	}
	before := Autonomy{AutonomousPromotion: p.AutonomousPromotion, MinStagingSoakSeconds: p.MinStagingSoakSeconds}
	if in.AutonomousPromotion != nil {
		p.AutonomousPromotion = *in.AutonomousPromotion
	}
	if in.MinStagingSoakSeconds != nil {
		p.MinStagingSoakSeconds = *in.MinStagingSoakSeconds
	}
	p.UpdatedAt = at
	c := *p
	return &c, before, nil
}

func (m *memStore) UpdateExclusions(_ context.Context, orgID, id string, change func([]Exclusion) ([]Exclusion, error)) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.OrgID != orgID {
		return nil, ErrNotFound
	}
	list, err := change(slices.Clone(p.Exclusions))
	if err != nil {
		return nil, err
	}
	p.Exclusions = list
	c := *p
	return &c, nil
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

// attachRecorder stands in for discovery: it attaches "owner/name" or a
// URL ending in it as owner/name, aliased by its name or the alias it's
// given, or fails with err, or with failOn for that repository.
type attachRecorder struct {
	urls   []string
	err    error
	failOn map[string]error
}

func (a *attachRecorder) AttachRepository(_ context.Context, _ security.Principal, proj *Project, in RepositoryInput) (repository, alias string, err error) {
	if a.err != nil {
		return "", "", a.err
	}
	if err := a.failOn[in.Repository]; err != nil {
		return "", "", err
	}
	a.urls = append(a.urls, proj.ID+" "+in.Repository)
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(in.Repository, "git@github.com:"), ".git"), "/")
	repository = strings.Join(parts[len(parts)-2:], "/")
	alias = in.Alias
	if alias == "" {
		alias = parts[len(parts)-1]
	}
	return repository, alias, nil
}

type fixture struct {
	svc    *Service
	store  *memStore
	init   *initRecorder
	audit  *auditRecorder
	attach *attachRecorder
	authz  *security.Authorizer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		store: newMemStore(), init: &initRecorder{}, audit: &auditRecorder{}, attach: &attachRecorder{},
		authz: security.NewAuthorizer(eng, ws, nil),
	}
	f.svc = NewService(f.store, f.authz, f.init, f.audit)
	f.svc.SetAttacher(f.attach)
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
	if !slices.Equal(p.Repositories, []string{"ada/engine"}) || len(f.attach.urls) != 1 {
		t.Fatalf("repositories %v, attached %v", p.Repositories, f.attach.urls)
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

// A project is an app unless it's created as a service project, and an
// update, even one that picks another icon, leaves its kind alone.
func TestService_kind(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	app, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Shop", Icon: "database"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Kind != ProjectKindApp {
		t.Fatalf("default kind %q, want app", app.Kind)
	}
	svc, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Orders DB", Kind: " service ", Icon: "rocket"})
	if err != nil {
		t.Fatal(err)
	}
	icon := "database"
	if _, err = f.svc.Update(ctx, dev, svc.ID, UpdateProjectInput{Icon: &icon}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{app.ID: ProjectKindApp, svc.ID: ProjectKindService} {
		got, err := f.svc.Get(ctx, dev, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != want {
			t.Fatalf("%s kind %q, want %q", got.Name, got.Kind, want)
		}
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
		{"unknown kind", CreateProjectInput{Name: "ok", Kind: "database"}},
		{"service project with a repository", CreateProjectInput{Name: "ok", Kind: ProjectKindService, RepositoryURL: "https://github.com/a/b"}},
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

func TestService_createRemovesTheProjectWhenItsRepositoryCantBeAttached(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	// The attacher says which of its errors are about the repository.
	tests := []struct {
		name       string
		err        error
		repository bool
	}{
		{"the repository", fmt.Errorf("%w: Loomplane can't read that repository", ErrRepository), true},
		{"anything else", errors.New("db down"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.attach.err = tt.err
			_, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine", RepositoryURL: "https://github.com/ada/private"})
			if !errors.Is(err, tt.err) || errors.Is(err, ErrRepository) != tt.repository {
				t.Fatalf("Create: %v", err)
			}
			list, err := f.svc.List(ctx, dev)
			if err != nil || len(list) != 0 {
				t.Fatalf("a half-made project was left behind: %v, %+v", err, list)
			}
		})
	}
}

func TestService_aRepositoryNeedsAnAttacher(t *testing.T) {
	f := newFixture(t)
	f.svc.SetAttacher(nil)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	if _, err := f.svc.Create(context.Background(), dev, CreateProjectInput{Name: "Engine", RepositoryURL: "https://github.com/ada/engine"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create: %v", err)
	}
}

func TestService_createWithSeveralRepositories(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)

	p, err := f.svc.Create(ctx, dev, CreateProjectInput{
		Name: "Shop", RepositoryURL: "https://github.com/acme/web",
		Repositories: []RepositoryInput{
			{Repository: "acme/api"},
			{Repository: "   "},
			{Repository: "other/api", Alias: "billing", Root: "svc", Ref: "main"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Attached in the order sent, the single URL first, a blank one left
	// out, and listed by alias, as the store reads them.
	if want := []string{p.ID + " https://github.com/acme/web", p.ID + " acme/api", p.ID + " other/api"}; !slices.Equal(f.attach.urls, want) {
		t.Fatalf("attached %v, want %v", f.attach.urls, want)
	}
	if want := []string{"acme/api", "other/api", "acme/web"}; !slices.Equal(p.Repositories, want) {
		t.Fatalf("repositories %v, want %v", p.Repositories, want)
	}
}

func TestService_aRepositoryThatFailsTakesTheProjectBackOut(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	f.attach.failOn = map[string]error{"acme/private": fmt.Errorf("%w: Loomplane can't read it", ErrRepository)}

	_, err := f.svc.Create(ctx, dev, CreateProjectInput{
		Name:         "Shop",
		Repositories: []RepositoryInput{{Repository: "acme/web"}, {Repository: "acme/private"}},
	})
	if !errors.Is(err, ErrRepository) || !strings.HasPrefix(err.Error(), "acme/private: ") {
		t.Fatalf("Create: %v, want a repository error naming acme/private", err)
	}
	if list, lerr := f.svc.List(ctx, dev); lerr != nil || len(list) != 0 {
		t.Fatalf("a half-made project was left behind: %v, %+v", lerr, list)
	}
}

func TestService_aProjectStartsWithAtMostTenRepositories(t *testing.T) {
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	repos := make([]RepositoryInput, maxRepositories+1)
	for i := range repos {
		repos[i] = RepositoryInput{Repository: fmt.Sprintf("acme/r%d", i)}
	}
	if _, err := f.svc.Create(context.Background(), dev, CreateProjectInput{Name: "Shop", Repositories: repos}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create: %v", err)
	}
	if len(f.attach.urls) != 0 {
		t.Fatalf("attached %v before refusing", f.attach.urls)
	}
}

// reapRecorder is a Reaper that records what it was asked to read and
// which of its clean ups ran.
type reapRecorder struct {
	reaped   []string
	finished []string
}

func (r *reapRecorder) Reap(_ context.Context, orgID, projectID, environmentID string) func(context.Context) {
	key := orgID + "/" + projectID + "/" + environmentID
	r.reaped = append(r.reaped, key)
	return func(context.Context) { r.finished = append(r.finished, key) }
}

func TestService_reapersRunAroundDeletion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	reaper := &reapRecorder{}
	f.svc.AddReaper(reaper)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil || len(envs) < 2 {
		t.Fatalf("environments: %d, %v", len(envs), err)
	}
	gone := org + "/" + p.ID + "/" + envs[0].ID
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reaper.reaped, []string{gone}) || !slices.Equal(reaper.finished, []string{gone}) {
		t.Fatalf("after deleting an environment: reaped %v, finished %v", reaper.reaped, reaper.finished)
	}

	// A refused deletion reads but cleans up nothing.
	for _, e := range envs[1 : len(envs)-1] {
		if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	reaper.reaped, reaper.finished = nil, nil
	last := envs[len(envs)-1]
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, last.ID); !errors.Is(err, ErrLastEnvironment) {
		t.Fatalf("deleting the last environment: %v", err)
	}
	if len(reaper.reaped) != 1 || len(reaper.finished) != 0 {
		t.Fatalf("a refused deletion: reaped %v, finished %v", reaper.reaped, reaper.finished)
	}

	// A caller who may not delete doesn't get as far as reading.
	viewer := f.principal(t, "ausr_viewer", security.RoleViewer)
	reaper.reaped = nil
	if err = f.svc.DeleteEnvironment(ctx, viewer, p.ID, last.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a viewer's deletion: %v", err)
	}
	if len(reaper.reaped) != 0 {
		t.Fatalf("a forbidden deletion was reaped: %v", reaper.reaped)
	}

	// A project's deletion reads all its environments.
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	whole := org + "/" + p.ID + "/"
	if err = f.svc.Delete(ctx, admin, p.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reaper.reaped, []string{whole}) || !slices.Equal(reaper.finished, []string{whole}) {
		t.Fatalf("after deleting the project: reaped %v, finished %v", reaper.reaped, reaper.finished)
	}
}

// optionsReaper is a ReaperWithOptions that records the options each
// deletion asked for, and fails the test if it's reaped without them.
type optionsReaper struct {
	t    *testing.T
	keep [][]string
}

func (r *optionsReaper) Reap(context.Context, string, string, string) func(context.Context) {
	r.t.Error("a reaper that reads options was reaped without them")
	return nil
}

func (r *optionsReaper) ReapWithOptions(_ context.Context, _, _, _ string, opts DeleteEnvironmentOptions) Reaped {
	r.keep = append(r.keep, opts.Keep)
	return Reaped{}
}

// providerReaper is a ReaperWithOptions with resources in a provider
// account that it removes, unless they're kept.
type providerReaper struct {
	resources []string
	cleaned   int
}

func (r *providerReaper) Reap(context.Context, string, string, string) func(context.Context) {
	return nil
}

func (r *providerReaper) ReapWithOptions(_ context.Context, _, _, _ string, opts DeleteEnvironmentOptions) Reaped {
	var out Reaped
	for _, id := range r.resources {
		if slices.Contains(opts.Keep, id) {
			out.Keeps = append(out.Keeps, id)
		} else {
			out.Removes = append(out.Removes, id)
		}
	}
	out.Cleanup = func(context.Context) { r.cleaned++ }
	return out
}

func TestService_removingProviderResourcesNeedsAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	reaper := &providerReaper{resources: []string{"bnd_web", "bnd_db"}}
	f.svc.AddReaper(reaper)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"qa", "demo"} {
		if _, err = f.svc.CreateEnvironment(ctx, dev, p.ID, CreateEnvironmentInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil || len(envs) != 4 {
		t.Fatalf("environments: %d, %v", len(envs), err)
	}

	// A developer may not delete what runs in a provider account.
	err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID, DeleteEnvironmentOptions{Keep: []string{"bnd_db"}})
	if !errors.Is(err, security.ErrForbidden) || !strings.Contains(err.Error(), "An admin must delete this environment") {
		t.Fatalf("a developer removing provider resources: %v", err)
	}
	if _, gerr := f.store.GetEnvironment(ctx, org, p.ID, envs[0].ID); gerr != nil || reaper.cleaned != 0 {
		t.Fatalf("a refused deletion deleted something: %v, cleaned %d", gerr, reaper.cleaned)
	}

	// Keeping everything removes nothing there, so a developer may.
	f.audit.events = nil
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID, DeleteEnvironmentOptions{Keep: []string{"bnd_web", "bnd_db"}}); err != nil {
		t.Fatalf("a developer keeping everything: %v", err)
	}
	if got := f.audit.events[0].Attributes; !slices.Equal(got["kept"].([]string), []string{"bnd_web", "bnd_db"}) || got["removed"] != nil {
		t.Fatalf("audited %v", got)
	}

	// An admin may remove them, and the audit says what went and stayed.
	f.audit.events = nil
	if err = f.svc.DeleteEnvironment(ctx, admin, p.ID, envs[1].ID, DeleteEnvironmentOptions{Keep: []string{"bnd_db"}}); err != nil {
		t.Fatalf("an admin removing provider resources: %v", err)
	}
	got := f.audit.events[0].Attributes
	if !slices.Equal(got["removed"].([]string), []string{"bnd_web"}) || !slices.Equal(got["kept"].([]string), []string{"bnd_db"}) {
		t.Fatalf("audited %v", got)
	}

	// Nothing in a provider account is what deleting always was.
	reaper.resources = nil
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[2].ID); err != nil {
		t.Fatalf("a developer deleting an environment with nothing on a provider: %v", err)
	}
}

func TestService_deleteEnvironmentPassesItsOptionsOn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	plain, withOptions := &reapRecorder{}, &optionsReaper{t: t}
	f.svc.AddReaper(plain)
	f.svc.AddReaper(withOptions)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateEnvironment(ctx, dev, p.ID, CreateEnvironmentInput{Name: "qa"}); err != nil {
		t.Fatal(err)
	}
	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil || len(envs) < 3 {
		t.Fatalf("environments: %d, %v", len(envs), err)
	}
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID, DeleteEnvironmentOptions{Keep: []string{"bnd_db"}}); err != nil {
		t.Fatal(err)
	}
	// Without options nothing is kept.
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[1].ID); err != nil {
		t.Fatal(err)
	}
	if len(withOptions.keep) != 2 || !slices.Equal(withOptions.keep[0], []string{"bnd_db"}) || len(withOptions.keep[1]) != 0 {
		t.Fatalf("the reaper was asked to keep %v", withOptions.keep)
	}
	// A reaper that doesn't read options is reaped as it always was.
	if len(plain.reaped) != 2 || len(plain.finished) != 2 {
		t.Fatalf("the plain reaper: reaped %v, finished %v", plain.reaped, plain.finished)
	}
}

// checkRecorder is an Authorizer that records every check as
// "permission resource" and allows them all, unless deny is set.
type checkRecorder struct {
	checks []string
	deny   bool
}

func (r *checkRecorder) Require(_ context.Context, _ security.Principal, perm security.Permission, resourceID string) error {
	r.checks = append(r.checks, perm.Name()+" "+resourceID)
	if r.deny {
		return security.ErrForbidden
	}
	return nil
}

func TestSetTeamNeedsProjectUpdateOnTheProject(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	store.projects["proj_1"] = &Project{ID: "proj_1", OrgID: org}
	store.teams["team_1"] = org
	authz := &checkRecorder{}
	svc := NewService(store, authz, &initRecorder{}, &auditRecorder{})
	p := security.Principal{UserID: "ausr_dev", OrgID: org}
	team := "team_1"

	got, err := svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &team})
	if err != nil {
		t.Fatalf("SetTeam: %v", err)
	}
	if got.ResponsibleTeamID != "team_1" {
		t.Fatalf("responsible team %q", got.ResponsibleTeamID)
	}
	if !slices.Equal(authz.checks, []string{"project:update proj_1"}) {
		t.Fatalf("checks %v, want project:update on proj_1", authz.checks)
	}

	denied := &checkRecorder{deny: true}
	store.teamSets = nil
	svc = NewService(store, denied, &initRecorder{}, &auditRecorder{})
	if _, err := svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &team}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied SetTeam: %v", err)
	}
	if len(store.teamSets) != 0 {
		t.Fatalf("a refused SetTeam reached the store: %v", store.teamSets)
	}
}

func TestSetTeamClearsAndAudits(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	store.projects["proj_1"] = &Project{ID: "proj_1", OrgID: org, ResponsibleTeamID: "team_1"}
	store.teams["team_1"] = org
	store.teams["team_other"] = "aorg_other"
	audit := &auditRecorder{}
	svc := NewService(store, &checkRecorder{}, &initRecorder{}, audit)
	p := security.Principal{UserID: "ausr_dev", OrgID: org}

	got, err := svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: nil})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got.ResponsibleTeamID != "" {
		t.Fatalf("after clearing: %q", got.ResponsibleTeamID)
	}
	blank := "  "
	if _, err = svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &blank}); err != nil {
		t.Fatalf("clear with a blank ID: %v", err)
	}
	if want := []string{org + "/proj_1/", org + "/proj_1/"}; !slices.Equal(store.teamSets, want) {
		t.Fatalf("store calls %v, want %v", store.teamSets, want)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit %v: clearing a project with no team must not audit", audit.actions)
	}
	e := audit.events[0]
	if e.Action != "project.team.set" || e.ProjectID != "proj_1" || e.Principal.UserID != "ausr_dev" {
		t.Fatalf("audit event %+v", e)
	}
	if v, ok := e.Attributes["team_id"]; !ok || v != "" {
		t.Fatalf("team_id attribute %v (present %v)", v, ok)
	}
	if e.Attributes["previous_team_id"] != "team_1" {
		t.Fatalf("previous_team_id attribute %v", e.Attributes["previous_team_id"])
	}

	// Another workspace's team is refused, and nothing is audited.
	other := "team_other"
	if _, err = svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &other}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("another workspace's team: %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("a refused change was audited: %v", audit.actions)
	}
	mine := "team_1"
	if _, err = svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &mine}); err != nil {
		t.Fatal(err)
	}
	if last := audit.events[len(audit.events)-1]; last.Attributes["team_id"] != "team_1" {
		t.Fatalf("team_id attribute %v", last.Attributes["team_id"])
	}
}

func TestSetTeamAuditsThePreviousTeam(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	store.projects["proj_1"] = &Project{ID: "proj_1", OrgID: org, ResponsibleTeamID: "team_1"}
	store.teams["team_1"] = org
	store.teams["team_2"] = org
	audit := &auditRecorder{}
	svc := NewService(store, &checkRecorder{}, &initRecorder{}, audit)
	p := security.Principal{UserID: "ausr_dev", OrgID: org}

	next := "team_2"
	if _, err := svc.SetTeam(ctx, p, "proj_1", SetTeamInput{TeamID: &next}); err != nil {
		t.Fatal(err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit %v", audit.actions)
	}
	a := audit.events[0].Attributes
	if a["team_id"] != "team_2" || a["previous_team_id"] != "team_1" {
		t.Fatalf("attributes %v", a)
	}

	// A project with no team, cleared again, changes nothing and audits nothing.
	store.projects["proj_1"].ResponsibleTeamID = ""
	if _, err := svc.SetTeam(ctx, p, "proj_1", SetTeamInput{}); err != nil {
		t.Fatal(err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("clearing an unset team was audited: %v", audit.actions)
	}
}

func TestService_createRollbackRunsTheReapers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	reaper := &reapRecorder{}
	f.svc.AddReaper(reaper)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)

	// The first repository attaches, the second can't: the project goes, and
	// what the first one's attach set up is cleaned up after it.
	f.attach.failOn = map[string]error{"acme/private": fmt.Errorf("%w: Loomplane can't read it", ErrRepository)}
	_, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Shop", Repositories: []RepositoryInput{{Repository: "acme/api"}, {Repository: "acme/private"}}})
	if !errors.Is(err, ErrRepository) {
		t.Fatalf("Create: %v", err)
	}
	if len(reaper.reaped) != 1 || !slices.Equal(reaper.reaped, reaper.finished) || !strings.HasSuffix(reaper.reaped[0], "/") {
		t.Fatalf("a rolled-back attach: reaped %v, finished %v", reaper.reaped, reaper.finished)
	}

	// The same when the project's initialization can't start.
	f.attach.failOn = nil
	reaper.reaped, reaper.finished = nil, nil
	f.init.err = errors.New("queue down")
	if _, err = f.svc.Create(ctx, dev, CreateProjectInput{Name: "Shop", RepositoryURL: "https://github.com/acme/api"}); err == nil {
		t.Fatal("Create succeeded without its initialization")
	} else if !strings.Contains(err.Error(), "initialization") {
		t.Fatalf("Create: %v", err)
	}
	if len(reaper.reaped) != 1 || !slices.Equal(reaper.reaped, reaper.finished) {
		t.Fatalf("a rolled-back initialization: reaped %v, finished %v", reaper.reaped, reaper.finished)
	}
}

// fakePreparer is a Preparer whose Run records each call and returns err.
type fakePreparer struct {
	removes []string
	err     error
	ran     []string
	order   *[]string
}

func (p *fakePreparer) PrepareDelete(_ context.Context, _, projectID, environmentID string, _ DeleteEnvironmentOptions) (Prepared, error) {
	key := projectID + "/" + environmentID
	return Prepared{Removes: p.removes, Run: func(context.Context) error {
		p.ran = append(p.ran, key)
		if p.order != nil {
			*p.order = append(*p.order, "prepare")
		}
		return p.err
	}}, nil
}

// orderReaper notes when it's asked to read, after any Preparer ran.
type orderReaper struct{ order *[]string }

func (r orderReaper) Reap(context.Context, string, string, string) func(context.Context) {
	*r.order = append(*r.order, "reap")
	return nil
}

func TestService_preparersRunBeforeTheDeletion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var order []string
	prep := &fakePreparer{removes: []string{"web on Vercel"}, order: &order}
	f.svc.AddPreparer(prep)
	f.svc.AddReaper(orderReaper{order: &order})
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, admin, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}

	// A failed preparation refuses the deletion before anything is read.
	prep.err = ErrFrontendsHeld
	if err = f.svc.Delete(ctx, admin, p.ID); !errors.Is(err, ErrFrontendsHeld) {
		t.Fatalf("a failed preparation: %v", err)
	}
	if _, gerr := f.store.GetProject(ctx, org, p.ID); gerr != nil || slices.Contains(order, "reap") {
		t.Fatalf("a refused deletion went ahead: %v, %v", gerr, order)
	}

	prep.err, order = nil, nil
	if err = f.svc.Delete(ctx, admin, p.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"prepare", "reap"}) || !slices.Equal(prep.ran, []string{p.ID + "/", p.ID + "/"}) {
		t.Fatalf("order %v, ran %v", order, prep.ran)
	}
	if _, gerr := f.store.GetProject(ctx, org, p.ID); !errors.Is(gerr, ErrNotFound) {
		t.Fatalf("after the deletion: %v", gerr)
	}
}

func TestService_preparerRemovingFromAProviderNeedsAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	prep := &fakePreparer{removes: []string{"web on Vercel"}}
	f.svc.AddPreparer(prep)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CreateEnvironment(ctx, dev, p.ID, CreateEnvironmentInput{Name: "qa"}); err != nil {
		t.Fatal(err)
	}
	envs, err := f.svc.Environments(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID)
	if !errors.Is(err, security.ErrForbidden) || len(prep.ran) != 0 {
		t.Fatalf("a developer removing frontends: %v, ran %v", err, prep.ran)
	}
	// Settling records alone removes nothing, so a developer may.
	prep.removes = nil
	if err = f.svc.DeleteEnvironment(ctx, dev, p.ID, envs[0].ID); err != nil || len(prep.ran) != 1 {
		t.Fatalf("a developer settling records: %v, ran %v", err, prep.ran)
	}
}

// refusingStore is a store that would refuse every deletion up front.
type refusingStore struct {
	*memStore
	err error
}

func (s refusingStore) DeletionRefusal(context.Context, string, string, string) error { return s.err }

// A deletion the store would refuse anyway removes nothing first: what a
// preparer removes from a provider can't come back.
func TestService_aRefusedDeletionRunsNoPreparer(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	store := refusingStore{memStore: f.store, err: ErrLastEnvironment}
	svc := NewService(store, f.authz, f.init, f.audit)
	prep := &fakePreparer{removes: []string{"web on Vercel"}}
	svc.AddPreparer(prep)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, admin, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := f.svc.Environments(ctx, admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteEnvironment(ctx, admin, p.ID, envs[0].ID); !errors.Is(err, ErrLastEnvironment) || len(prep.ran) != 0 {
		t.Fatalf("a refused deletion: %v, ran %v", err, prep.ran)
	}
	store.err = ErrFleetManaged
	svc = NewService(store, f.authz, f.init, f.audit)
	svc.AddPreparer(prep)
	if err = svc.Delete(ctx, admin, p.ID); !errors.Is(err, ErrFleetManaged) || len(prep.ran) != 0 {
		t.Fatalf("a refused project deletion: %v, ran %v", err, prep.ran)
	}
}
