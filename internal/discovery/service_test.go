package discovery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

const (
	org     = "aorg_test"
	commit  = "0123456789abcdef0123456789abcdef01234567"
	project = "proj_mono"
)

// memStore is an in-memory Store for these tests. PgStore's own behaviour
// is covered against Postgres in tests/integration.
type memStore struct {
	mu       sync.Mutex
	analyses map[string]*Analysis
	attempts map[string]int
	graphs   map[string]*graph.Graph
	sources  map[string]*Source
}

func newMemStore() *memStore {
	return &memStore{analyses: map[string]*Analysis{}, attempts: map[string]int{}, graphs: map[string]*graph.Graph{}, sources: map[string]*Source{}}
}

func (m *memStore) get(orgID, id string) (*Analysis, error) {
	a, ok := m.analyses[id]
	if !ok || a.OrgID != orgID {
		return nil, ErrNotFound
	}
	return a, nil
}

func (m *memStore) CreateAnalysis(_ context.Context, a *Analysis) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *a
	m.analyses[a.ID] = &c
	return nil
}

func (m *memStore) DeleteAnalysis(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(orgID, id); err == nil {
		delete(m.analyses, id)
	}
	return nil
}

func (m *memStore) GetAnalysis(_ context.Context, orgID, id string) (*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.get(orgID, id)
	if err != nil {
		return nil, err
	}
	c := *a
	return &c, nil
}

func (m *memStore) ListAnalyses(_ context.Context, orgID, projectID string, limit int) ([]*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Analysis{}
	for _, a := range m.analyses {
		if a.OrgID == orgID && a.ProjectID == projectID {
			c := *a
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ListSourceAnalyses(_ context.Context, orgID, sourceID string, limit int) ([]*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Analysis{}
	for _, a := range m.analyses {
		if a.OrgID == orgID && a.SourceID == sourceID {
			c := *a
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ActiveAnalysis(_ context.Context, orgID, sourceID string, since time.Time) (*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.analyses {
		if a.OrgID == orgID && a.SourceID == sourceID && !a.Finished() && a.CreatedAt.After(since) {
			c := *a
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) BeginAttempt(_ context.Context, orgID, id string, now time.Time) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.get(orgID, id)
	if err != nil {
		return 0, false, err
	}
	if a.Finished() {
		return 0, false, nil
	}
	a.Status = StatusFetching
	if a.StartedAt == nil {
		a.StartedAt = &now
	}
	m.attempts[id]++
	return m.attempts[id], true, nil
}

func (m *memStore) Progress(_ context.Context, orgID, id, status, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, err := m.get(orgID, id); err == nil && !a.Finished() {
		a.Status, a.CommitSHA = status, sha
	}
	return nil
}

func (m *memStore) Succeed(_ context.Context, orgID, id string, g *graph.Graph, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, err := m.get(orgID, id); err == nil && !a.Finished() {
		a.Status, a.Summary, a.FinishedAt = StatusSucceeded, g.Summarize(), &now
		m.graphs[id] = g
	}
	return nil
}

func (m *memStore) Fail(_ context.Context, orgID, id, reason string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, err := m.get(orgID, id); err == nil && !a.Finished() {
		a.Status, a.Error, a.FinishedAt = StatusFailed, reason, &now
	}
	return nil
}

func (m *memStore) Graph(_ context.Context, orgID, id string) (*graph.Graph, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(orgID, id); err != nil {
		return nil, err
	}
	g, ok := m.graphs[id]
	if !ok {
		return nil, ErrNoGraph
	}
	return g, nil
}

func (m *memStore) CreateSource(_ context.Context, s *Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.sources {
		if x.ProjectID == s.ProjectID && x.Alias == s.Alias {
			return ErrAliasTaken
		}
	}
	c := *s
	m.sources[s.ID] = &c
	return nil
}

func (m *memStore) findSource(orgID, projectID, alias string, live bool) *Source {
	for _, x := range m.sources {
		if x.OrgID == orgID && x.ProjectID == projectID && x.Alias == alias && (!live || x.RemovedAt == nil) {
			return x
		}
	}
	return nil
}

func (m *memStore) GetSource(_ context.Context, orgID, projectID, alias string) (*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(orgID, projectID, alias, false)
	if x == nil {
		return nil, ErrSourceNotFound
	}
	c := *x
	return &c, nil
}

func (m *memStore) ListSources(_ context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Source{}
	for _, x := range m.sources {
		if x.OrgID == orgID && x.ProjectID == projectID && (withRemoved || x.RemovedAt == nil) {
			c := *x
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (m *memStore) UpdateSource(_ context.Context, s *Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(s.OrgID, s.ProjectID, s.Alias, true)
	if x == nil {
		return ErrSourceNotFound
	}
	x.Root, x.Ref, x.UpdatedAt = s.Root, s.Ref, s.UpdatedAt
	return nil
}

func (m *memStore) RemoveSource(_ context.Context, orgID, projectID, alias string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(orgID, projectID, alias, true)
	if x == nil {
		return ErrSourceNotFound
	}
	x.RemovedAt, x.UpdatedAt = &now, now
	return nil
}

// fakeAccess stands in for GitHub: it records what was checked and
// answers err.
type fakeAccess struct {
	err     error
	checked []string
}

func (a *fakeAccess) Check(_ context.Context, _ string, repo repositories.Repository, ref string) error {
	a.checked = append(a.checked, repo.FullName()+"@"+ref)
	return a.err
}

type fakeProjects map[string]*projects.Project

func (f fakeProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	p, ok := f[id]
	if !ok || p.OrgID != orgID {
		return nil, projects.ErrNotFound
	}
	return p, nil
}

type queue struct {
	mu   sync.Mutex
	ids  []string
	fail error
	// failOn makes the nth call (counting from 1) fail, and only that one.
	failOn, calls int
}

func (q *queue) AnalyzeRepository(_ context.Context, _, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.fail != nil {
		return q.fail
	}
	if q.failOn != 0 && q.calls == q.failOn {
		return errors.New("queue down")
	}
	q.ids = append(q.ids, id)
	return nil
}

type audits struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *audits) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// dirFetcher "fetches" by copying a local directory, the way a tarball
// would land.
type dirFetcher struct {
	src      string
	fetchErr error
}

func (d *dirFetcher) Resolve(_ context.Context, _ repositories.Repository, _, _ string) (string, error) {
	return commit, nil
}

func (d *dirFetcher) Fetch(_ context.Context, _ repositories.Repository, _, _, dest string) (repositories.Stats, error) {
	if d.fetchErr != nil {
		return repositories.Stats{}, d.fetchErr
	}
	err := filepath.WalkDir(d.src, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(d.src, p)
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
	return repositories.Stats{}, err
}

type fixture struct {
	svc    *Service
	runner *Runner
	store  *memStore
	queue  *queue
	audit  *audits
	fetch  *dirFetcher
	authz  *security.Authorizer
	access *fakeAccess
}

func newFixture(t *testing.T, maxAttempts int) *fixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	authz := security.NewAuthorizer(eng, ws, nil)
	f := &fixture{store: newMemStore(), queue: &queue{}, audit: &audits{}, fetch: &dirFetcher{src: "../../examples/polyglot"}, authz: authz, access: &fakeAccess{}}
	prj := fakeProjects{
		project:     {ID: project, OrgID: org},
		"proj_bare": {ID: "proj_bare", OrgID: org},
	}
	f.svc = NewService(f.store, prj, authz, f.queue, f.audit)
	f.svc.SetAccess(f.access)
	f.source(t, project, "mono", "acme/mono", "")
	f.runner = NewRunner(f.store, f.fetch, nil, t.TempDir(), maxAttempts)
	return f
}

func (f *fixture) as(t *testing.T, user, role string) security.Principal {
	t.Helper()
	if err := f.authz.Assign(context.Background(), org, user, role, "test"); err != nil {
		t.Fatal(err)
	}
	return security.Principal{UserID: user, OrgID: org}
}

// source attaches a source straight through the store.
func (f *fixture) source(t *testing.T, projectID, alias, repo, root string) *Source {
	t.Helper()
	now := time.Now().UTC()
	s := &Source{
		ID: "src_" + projectID + "_" + alias, OrgID: org, ProjectID: projectID, Alias: alias,
		Repository: repo, Root: root, CreatedBy: "ausr_seed", CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateSource(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestService_startQueuesAnAnalysis(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	as, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Ref: " release/1.2 "})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	a := as[0]
	if a.Status != StatusQueued || a.Repository != "acme/mono" || a.Ref != "release/1.2" || !strings.HasPrefix(a.ID, "anl_") {
		t.Fatalf("analysis %+v", a)
	}
	if a.Source != "mono" || a.SourceID != "src_proj_mono_mono" {
		t.Fatalf("analysis source %q (%s)", a.Source, a.SourceID)
	}
	if len(f.queue.ids) != 1 || f.queue.ids[0] != a.ID {
		t.Fatalf("queued %v", f.queue.ids)
	}
	if len(f.audit.events) != 1 || f.audit.events[0].Action != "analysis.start" {
		t.Fatalf("audit %+v", f.audit.events)
	}
	if _, err = f.svc.Start(ctx, dev, project, StartAnalysisInput{}); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("second start while one runs: %v", err)
	}
	list, err := f.svc.List(ctx, f.as(t, "ausr_viewer", security.RoleViewer), project)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v, %d", err, len(list))
	}
}

func TestService_startRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	tests := []struct {
		name    string
		p       security.Principal
		project string
		sources []string
		ref     string
		want    error
	}{
		{"viewer", viewer, project, nil, "", security.ErrForbidden},
		{"another org's project", dev, "proj_elsewhere", nil, "", projects.ErrNotFound},
		{"no repository", dev, "proj_bare", nil, "", ErrNoSources},
		{"unknown source", dev, project, []string{"nope"}, "", ErrSourceNotFound},
		{"bad ref", dev, project, nil, "../../etc/passwd", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.Start(ctx, tt.p, tt.project, StartAnalysisInput{Sources: tt.sources, Ref: tt.ref}); !errors.Is(err, tt.want) {
				t.Fatalf("Start: %v, want %v", err, tt.want)
			}
		})
	}
	f.queue.fail = errors.New("queue down")
	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{}); err == nil {
		t.Fatal("Start succeeded without its job")
	}
	if len(f.store.analyses) != 0 {
		t.Fatal("an analysis without a job was left queued")
	}
}

func TestService_startAnalyzesEachSource(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	web := f.source(t, project, "web", "acme/site", "")
	web.Ref = "develop"
	if err := f.store.UpdateSource(ctx, web); err != nil {
		t.Fatal(err)
	}
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Ref: "main"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a ref with two sources: %v", err)
	}
	all, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Source != "mono" || all[1].Source != "web" || all[1].Ref != "develop" || len(f.queue.ids) != 2 {
		t.Fatalf("analyses %+v, queued %v", all, f.queue.ids)
	}
}

func TestService_oneRunningSourceRefusesTheWholeRequest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	f.source(t, project, "web", "acme/site", "")
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Sources: []string{"mono"}}); err != nil {
		t.Fatal(err)
	}
	// mono is still running, so analyzing everything is refused, and web
	// isn't queued on its own either.
	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{}); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("Start while mono runs: %v", err)
	}
	if len(f.queue.ids) != 1 {
		t.Fatalf("queued %v", f.queue.ids)
	}
}

func TestService_aFailedStartTakesBackWhatItQueued(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	f.source(t, project, "web", "acme/site", "")
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	f.queue.failOn = 2
	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{}); err == nil {
		t.Fatal("Start succeeded though the second job couldn't be queued")
	}
	if len(f.store.analyses) != 0 {
		t.Fatalf("analyses left behind: %+v", f.store.analyses)
	}
	// Nothing is stuck as "already running", so the retry starts both.
	f.queue.failOn = 0
	all, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil || len(all) != 2 || len(f.store.analyses) != 2 {
		t.Fatalf("retry: %v, %d analyses, %d stored", err, len(all), len(f.store.analyses))
	}
}

func TestRunner_analyzesAndStoresTheGraph(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	as, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
	a := as[0]
	if err = f.runner.Run(ctx, org, a.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.svc.Get(ctx, dev, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSucceeded || got.CommitSHA != commit || got.Summary.Services != 7 || got.FinishedAt == nil {
		t.Fatalf("analysis %+v", got)
	}
	g, err := f.svc.Graph(ctx, dev, a.ID)
	if err != nil || len(g.Services) != 7 {
		t.Fatalf("Graph: %v", err)
	}
	// A duplicate delivery of the finished job changes nothing.
	if err := f.runner.Run(ctx, org, a.ID); err != nil || f.store.attempts[a.ID] != 1 {
		t.Fatalf("duplicate run: %v, attempts %d", err, f.store.attempts[a.ID])
	}
	// Dispatch has no dedup, and the analysis may be gone by the time it runs.
	if err := f.runner.Run(ctx, org, "anl_gone"); err != nil {
		t.Fatalf("run of a deleted analysis: %v", err)
	}
}

func TestRunner_failures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		err        error
		maxAttempt int
		retried    bool
		reason     string
	}{
		{"permanent", repositories.ErrNotFound, 3, false, repositories.ErrNotFound.Error()},
		{"transient, then out of attempts", errors.New("connection reset"), 2, true, "the analysis failed: connection reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.maxAttempt)
			dev := f.as(t, "ausr_dev", security.RoleDeveloper)
			as, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
			if err != nil {
				t.Fatal(err)
			}
			a := as[0]
			f.fetch.fetchErr = tt.err
			err = f.runner.Run(ctx, org, a.ID)
			if tt.retried {
				if err == nil {
					t.Fatal("a transient failure wasn't retried")
				}
				if got, _ := f.store.GetAnalysis(ctx, org, a.ID); got.Finished() {
					t.Fatalf("finished after one try: %+v", got)
				}
				err = f.runner.Run(ctx, org, a.ID)
			}
			if err != nil {
				t.Fatalf("final run: %v", err)
			}
			got, _ := f.store.GetAnalysis(ctx, org, a.ID)
			if got.Status != StatusFailed || got.Error != tt.reason {
				t.Fatalf("analysis %+v", got)
			}
			if _, err := f.svc.Graph(ctx, dev, a.ID); !errors.Is(err, ErrNoGraph) {
				t.Fatalf("graph of a failed analysis: %v", err)
			}
		})
	}
}

func TestSources_lifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)

	mono, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "https://github.com/acme/mono.git"})
	if err != nil {
		t.Fatal(err)
	}
	if mono.Alias != "mono" || mono.Repository != "acme/mono" || mono.Root != "" || !strings.HasPrefix(mono.ID, "src_") {
		t.Fatalf("source %+v", mono)
	}
	web, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "web", Root: "/apps/web/", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if web.Root != "apps/web" || web.Ref != "main" {
		t.Fatalf("source %+v", web)
	}
	list, err := f.svc.Sources(ctx, viewer, "proj_bare")
	if err != nil || len(list) != 2 || list[0].Alias != "mono" || list[1].Alias != "web" {
		t.Fatalf("Sources: %v, %+v", err, list)
	}

	ref := "release"
	web, err = f.svc.UpdateSource(ctx, dev, "proj_bare", "web", UpdateSourceInput{Ref: &ref})
	if err != nil || web.Ref != "release" || f.access.checked[len(f.access.checked)-1] != "acme/site@release" {
		t.Fatalf("UpdateSource: %v, %+v, checked %v", err, web, f.access.checked)
	}

	if err = f.svc.RemoveSource(ctx, dev, "proj_bare", "mono"); err != nil {
		t.Fatal(err)
	}
	list, _ = f.svc.Sources(ctx, viewer, "proj_bare")
	if len(list) != 1 || list[0].Alias != "web" {
		t.Fatalf("after removing mono: %+v", list)
	}
	// An alias is never reused, so an old plan's "mono" still means acme/mono.
	if _, err = f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/other", Alias: "mono"}); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("re-adding a removed alias: %v", err)
	}
	actions := make([]string, 0, len(f.audit.events))
	for _, e := range f.audit.events {
		actions = append(actions, e.Action)
	}
	if !slices.Equal(actions, []string{"source.add", "source.add", "source.update", "source.remove"}) {
		t.Fatalf("audit %v", actions)
	}
}

func TestSources_refusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	f.source(t, "proj_bare", "platform", "acme/mono", "services")
	tests := []struct {
		name    string
		p       security.Principal
		project string
		in      AddSourceInput
		want    error
	}{
		{"viewer", viewer, "proj_bare", AddSourceInput{Repository: "acme/site"}, security.ErrForbidden},
		{"another org's project", dev, "proj_elsewhere", AddSourceInput{Repository: "acme/site"}, projects.ErrNotFound},
		{"not GitHub", dev, "proj_bare", AddSourceInput{Repository: "https://gitlab.com/acme/site"}, ErrInvalidSource},
		{"reserved alias", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "go"}, ErrAliasReserved},
		{"bad alias", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "Web"}, ErrInvalidSource},
		{"root climbs out", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Root: "../x"}, ErrInvalidSource},
		{"bad ref", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Ref: "../../etc"}, ErrInvalidSource},
		{"nested root", dev, "proj_bare", AddSourceInput{Repository: "acme/mono", Alias: "api", Root: "services/api"}, ErrRootsOverlap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.AddSource(ctx, tt.p, tt.project, tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("AddSource: %v, want %v", err, tt.want)
			}
		})
	}
	// Another directory of the same repository is fine.
	if _, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/mono", Alias: "apps", Root: "apps"}); err != nil {
		t.Fatalf("a separate directory: %v", err)
	}
	f.access.err = repositories.ErrNotFound
	if _, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/private"}); !errors.Is(err, ErrSourceInaccessible) {
		t.Fatalf("a repository Loomplane can't read: %v", err)
	}
}

func TestSources_attachingANewProjectsRepositorySaysWhatsWrongWithIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	proj := &projects.Project{ID: "proj_bare", OrgID: org}
	f.source(t, "proj_bare", "platform", "acme/mono", "")
	tests := []struct {
		name, url string
		want      error
	}{
		{"not GitHub", "https://gitlab.com/acme/site", ErrInvalidSource},
		{"alias taken", "acme/platform", ErrAliasTaken},
		{"overlapping root", "acme/mono", ErrRootsOverlap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: tt.url})
			if !errors.Is(err, tt.want) || !errors.Is(err, projects.ErrRepository) {
				t.Fatalf("AttachRepository: %v, want %v as a repository error", err, tt.want)
			}
		})
	}
	f.access.err = repositories.ErrNotFound
	_, _, err := f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: "acme/private"})
	if !errors.Is(err, ErrSourceInaccessible) || !errors.Is(err, projects.ErrRepository) {
		t.Fatalf("a repository Loomplane can't read: %v", err)
	}
	// Anything else isn't the repository's fault, so it isn't one.
	f.access.err = errors.New("github is down")
	_, _, err = f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: "acme/other"})
	if err == nil || errors.Is(err, projects.ErrRepository) {
		t.Fatalf("an outage: %v", err)
	}
}

func TestSources_twoOfANewProjectsRepositoriesWantingOneAlias(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	proj := &projects.Project{ID: "proj_bare", OrgID: org}
	if _, alias, err := f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: "acme/api"}); err != nil || alias != "api" {
		t.Fatalf("the first: %q %v", alias, err)
	}
	_, _, err := f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: "other/api"})
	if !errors.Is(err, ErrAliasTaken) || !errors.Is(err, projects.ErrRepository) ||
		!strings.Contains(err.Error(), "another of its repositories already has the alias api") {
		t.Fatalf("the second: %v", err)
	}
	if _, alias, err := f.svc.AttachRepository(ctx, dev, proj, projects.RepositoryInput{Repository: "other/api", Alias: "other-api"}); err != nil || alias != "other-api" {
		t.Fatalf("with an alias of its own: %q %v", alias, err)
	}
}

// refFetcher records the refs it's asked to resolve.
type refFetcher struct {
	refs []string
	err  error
}

func (r *refFetcher) Resolve(_ context.Context, _ repositories.Repository, ref, _ string) (string, error) {
	r.refs = append(r.refs, ref)
	return commit, r.err
}

func (r *refFetcher) Fetch(context.Context, repositories.Repository, string, string, string) (repositories.Stats, error) {
	return repositories.Stats{}, errors.New("not fetched")
}

func TestGitHubAccess_aCommitRefStillReadsTheRepository(t *testing.T) {
	ctx := context.Background()
	repo := repositories.Repository{Owner: "acme", Name: "private"}
	tests := []struct {
		name, ref string
		want      []string
	}{
		// GitHub resolves a commit without asking, so reading the
		// repository's default branch is what proves it can be read.
		{"commit", commit, []string{""}},
		{"branch", "main", []string{"main"}},
		{"default branch", "", []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &refFetcher{}
			if err := (GitHubAccess{Fetcher: f}).Check(ctx, org, repo, tt.ref); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.refs, tt.want) {
				t.Fatalf("resolved %q, want %q", f.refs, tt.want)
			}
		})
	}
	f := &refFetcher{err: repositories.ErrNotFound}
	if err := (GitHubAccess{Fetcher: f}).Check(ctx, org, repo, commit); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a commit of a repository Loomplane can't read: %v", err)
	}
}

func TestService_projectGraphComposesEverySource(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	f.source(t, project, "web", "acme/site", "")
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	queued, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
	// Only mono's analysis runs; web's is still queued.
	if err = f.runner.Run(ctx, org, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.ProjectGraph(ctx, f.as(t, "ausr_viewer", security.RoleViewer), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Sources) != 2 || view.Sources[0].Shown == nil || view.Sources[1].Shown != nil || view.Sources[1].Latest == nil {
		t.Fatalf("sources %+v", view.Sources)
	}
	if len(view.Graph.Services) != 7 || !strings.HasPrefix(view.Graph.Services[0].ID, "svc:mono:") {
		t.Fatalf("services %+v", view.Graph.Services)
	}
	known, err := ServiceCatalog{Store: f.store}.Services(ctx, org, project)
	if err != nil || known[view.Graph.Services[0].ID] == "" {
		t.Fatalf("catalog %v, %v", known, err)
	}
}
