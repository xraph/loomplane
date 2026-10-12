package discovery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
}

func newMemStore() *memStore {
	return &memStore{analyses: map[string]*Analysis{}, attempts: map[string]int{}, graphs: map[string]*graph.Graph{}}
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

func (m *memStore) ActiveAnalysis(_ context.Context, orgID, projectID string, since time.Time) (*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.analyses {
		if a.OrgID == orgID && a.ProjectID == projectID && !a.Finished() && a.CreatedAt.After(since) {
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
}

func (q *queue) AnalyzeRepository(_ context.Context, _, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail != nil {
		return q.fail
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
}

func newFixture(t *testing.T, maxAttempts int) *fixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	authz := security.NewAuthorizer(eng, ws, nil)
	f := &fixture{store: newMemStore(), queue: &queue{}, audit: &audits{}, fetch: &dirFetcher{src: "../../examples/polyglot"}, authz: authz}
	prj := fakeProjects{
		project:     {ID: project, OrgID: org, RepositoryURL: "https://github.com/acme/mono"},
		"proj_bare": {ID: "proj_bare", OrgID: org},
		"proj_lab":  {ID: "proj_lab", OrgID: org, RepositoryURL: "https://gitlab.com/acme/mono"},
	}
	f.svc = NewService(f.store, prj, authz, f.queue, f.audit)
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

func TestService_startQueuesAnAnalysis(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	a, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Ref: " release/1.2 "})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if a.Status != StatusQueued || a.Repository != "acme/mono" || a.Ref != "release/1.2" || !strings.HasPrefix(a.ID, "anl_") {
		t.Fatalf("analysis %+v", a)
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
		ref     string
		want    error
	}{
		{"viewer", viewer, project, "", security.ErrForbidden},
		{"another org's project", dev, "proj_elsewhere", "", projects.ErrNotFound},
		{"no repository", dev, "proj_bare", "", ErrNoRepository},
		{"not GitHub", dev, "proj_lab", "", ErrInvalid},
		{"bad ref", dev, project, "../../etc/passwd", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.Start(ctx, tt.p, tt.project, StartAnalysisInput{Ref: tt.ref}); !errors.Is(err, tt.want) {
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

func TestRunner_analyzesAndStoresTheGraph(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	a, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
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
			a, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
			if err != nil {
				t.Fatal(err)
			}
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
