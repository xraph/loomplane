package releasewatch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// memWatches is a Store in memory. Due claims every open watch. It finds
// rollback plans among plans.
type memWatches struct {
	mu    sync.Mutex
	w     map[string]*Watch
	plans *fakePlans
}

func (m *memWatches) RollbackPlan(_ context.Context, orgID, watchID string) (string, error) {
	m.plans.mu.Lock()
	defer m.plans.mu.Unlock()
	for _, p := range m.plans.plans {
		if p.OrgID == orgID && p.Rollback != nil && p.Rollback.WatchID == watchID {
			return p.ID, nil
		}
	}
	return "", nil
}

func copyWatch(w *Watch) *Watch {
	data, err := json.Marshal(w)
	if err != nil {
		panic(err)
	}
	var out Watch
	if err = json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	out.Revision = w.Revision
	return &out
}

func (m *memWatches) Insert(_ context.Context, w *Watch) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		m.w = map[string]*Watch{}
	}
	for _, have := range m.w {
		if have.DeploymentID == w.DeploymentID {
			return false, nil
		}
	}
	w.Revision = 1
	m.w[w.ID] = copyWatch(w)
	return true, nil
}

func (m *memWatches) ForDeployment(_ context.Context, orgID, deploymentID string) (*Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.w {
		if w.OrgID == orgID && w.DeploymentID == deploymentID {
			return copyWatch(w), nil
		}
	}
	return nil, ErrNotFound
}

func (m *memWatches) Latest(_ context.Context, orgID, environmentID string) (*Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out *Watch
	for _, w := range m.w {
		if w.OrgID == orgID && w.EnvironmentID == environmentID && (out == nil || w.StartsAt.After(out.StartsAt)) {
			out = w
		}
	}
	if out == nil {
		return nil, ErrNotFound
	}
	return copyWatch(out), nil
}

func (m *memWatches) Due(_ context.Context, now time.Time, _ int) ([]*Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Watch{}
	for _, w := range m.w {
		if w.Open() {
			at := now
			w.CheckedAt = &at
			out = append(out, copyWatch(w))
		}
	}
	slices.SortFunc(out, func(a, b *Watch) int { return a.StartsAt.Compare(b.StartsAt) })
	return out, nil
}

func (m *memWatches) Save(_ context.Context, w *Watch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := m.w[w.ID]
	if have == nil || have.Revision != w.Revision {
		return ErrConflict
	}
	w.Revision++
	m.w[w.ID] = copyWatch(w)
	return nil
}

func (m *memWatches) only(t *testing.T) *Watch {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.w) != 1 {
		t.Fatalf("%d watches, want 1", len(m.w))
	}
	for _, w := range m.w {
		return copyWatch(w)
	}
	return nil
}

func (m *memWatches) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.w)
}

// fakePlans holds plans by ID and bindings by environment.
type fakePlans struct {
	mu       sync.Mutex
	plans    map[string]*planning.Plan
	bindings map[string][]*planning.Binding
}

func (f *fakePlans) GetPlan(_ context.Context, _, id string) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.plans[id]
	if p == nil {
		return nil, planning.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (f *fakePlans) ListBindings(_ context.Context, _, environmentID string) ([]*planning.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*planning.Binding, 0, len(f.bindings[environmentID]))
	for _, b := range f.bindings[environmentID] {
		c := *b
		out = append(out, &c)
	}
	return out, nil
}

func (f *fakePlans) put(p *planning.Plan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[p.ID] = p
}

func (f *fakePlans) setStatus(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[id].Status = status
}

func (f *fakePlans) movePlan(environmentID, name, planID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bindings[environmentID] {
		if b.Name == name {
			b.PlanID = planID
		}
	}
}

// fakeManifests holds manifest revisions by ID; the latest is "man_latest".
type fakeManifests struct{ revs map[string]*manifest.Manifest }

func (f fakeManifests) Latest(_ context.Context, _, _ string) (*manifests.Revision, error) {
	return f.Get(context.Background(), "", "man_latest")
}

func (f fakeManifests) Get(_ context.Context, _, id string) (*manifests.Revision, error) {
	m, ok := f.revs[id]
	if !ok {
		return nil, manifests.ErrNotFound
	}
	return &manifests.Revision{ID: id, Manifest: m}, nil
}

type fakeProjects struct{}

func (fakeProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return &projects.Project{ID: "proj"}, nil
}

func (fakeProjects) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return []*projects.Environment{
		{ID: "env_s", ProjectID: "proj", Name: "staging"},
		{ID: "env_p", ProjectID: "proj", Name: "production"},
	}, nil
}

// fakeHistory lists deployments newest first.
type fakeHistory struct {
	mu      sync.Mutex
	records []planning.ReleaseRecord
}

func (f *fakeHistory) Releases(_ context.Context, _, _ string, _ time.Time, limit int) ([]planning.ReleaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []planning.ReleaseRecord{}
	for _, r := range f.records {
		if r.Status == string(deployments.StatusSucceeded) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeHistory) Latest(context.Context, string, string) (*planning.ReleaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) == 0 {
		return nil, planning.ErrNotFound
	}
	r := f.records[0]
	return &r, nil
}

func (f *fakeHistory) Deployment(_ context.Context, _, id string) (*planning.ReleaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.records {
		if r.DeploymentID == id {
			return &r, nil
		}
	}
	return nil, planning.ErrNotFound
}

func (f *fakeHistory) ReleasedImages(context.Context, string, string, time.Time) (map[string]planning.ReleasedImage, error) {
	return nil, nil
}

func (f *fakeHistory) push(r planning.ReleaseRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append([]planning.ReleaseRecord{r}, f.records...)
}

// read is one Health call.
type read struct {
	org, binding, revision string
	w                      releasehealth.Window
}

// fakeReader answers each binding with a function of the window.
type fakeReader struct {
	mu        sync.Mutex
	reads     []read
	deadlines []time.Time
	answer    func(binding string, w releasehealth.Window) (releasehealth.Health, error)
}

func (f *fakeReader) Health(ctx context.Context, orgID, bindingID, revision string, w releasehealth.Window) (releasehealth.Health, error) {
	f.mu.Lock()
	f.reads = append(f.reads, read{orgID, bindingID, revision, w})
	deadline, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, deadline)
	answer := f.answer
	f.mu.Unlock()
	if answer == nil {
		return healthy(), nil
	}
	return answer(bindingID, w)
}

func (f *fakeReader) calls() []read {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]read(nil), f.reads...)
}

func (f *fakeReader) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = nil
}

func healthy() releasehealth.Health {
	return releasehealth.Health{Requests: 1000, Errors: 1, LatencyMS: 100, Statistic: "p95", Available: true}
}

// fakeRollbacks stands in for planning's automatic rollback.
type fakeRollbacks struct {
	mu         sync.Mutex
	plans      *fakePlans
	created    []planning.AutomaticRollbackInput
	createdFor []string
	approved   []string
	createErr  error
	approveErr error
	// status is what a created plan starts as.
	status string
	// onCreate runs inside the next create, before it answers.
	onCreate func()
}

func (f *fakeRollbacks) CreateAutomaticRollback(_ context.Context, orgID, deploymentID string, in planning.AutomaticRollbackInput) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	f.createdFor = append(f.createdFor, orgID+"/"+deploymentID)
	if hook := f.onCreate; hook != nil {
		f.onCreate = nil
		hook()
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := fmt.Sprintf("plan_rb%d", len(f.created))
	status := f.status
	if status == "" {
		status = planning.StatusPlanning
	}
	p := &planning.Plan{
		ID: id, OrgID: orgID, ProjectID: "proj", EnvironmentID: "env_p", Environment: "production", Kind: planning.KindRollback, Status: status, Executable: true,
		Rollback: &planning.Rollback{Reason: in.Reason, Trigger: in.Trigger, WatchID: in.WatchID, TargetDeploymentID: deploymentID, RestoreDeploymentID: "dep_0", RestorePlanID: "plan_0"},
	}
	f.plans.put(p)
	return p, nil
}

func (f *fakeRollbacks) ApproveAutomatic(_ context.Context, _, planID string) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, planID)
	if f.approveErr != nil {
		return nil, f.approveErr
	}
	return f.plans.GetPlan(context.Background(), "", planID)
}

// fakeDeployments holds deployments by plan.
type fakeDeployments struct {
	mu sync.Mutex
	d  map[string]*deployments.Deployment
}

func (f *fakeDeployments) ForPlan(_ context.Context, _, planID string) (*deployments.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.d[planID]
	if d == nil {
		return nil, deployments.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (f *fakeDeployments) put(d *deployments.Deployment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.d == nil {
		f.d = map[string]*deployments.Deployment{}
	}
	f.d[d.PlanID] = d
}

// fakeRoutes lists a project's routes.
type fakeRoutes []*cloudinfra.Route

func (f fakeRoutes) Routes(context.Context, string, string) ([]*cloudinfra.Route, error) {
	return f, nil
}

// fakeAuthz allows everything but deny.
type fakeAuthz struct {
	mu     sync.Mutex
	deny   bool
	checks []security.Permission
}

func (f *fakeAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, perm)
	if f.deny {
		return security.ErrForbidden
	}
	return nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (f *fakeAudit) Record(_ context.Context, e projects.AuditEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeAudit) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.events))
	for _, e := range f.events {
		out = append(out, e.Action)
	}
	return out
}

func (f *fakeAudit) find(action string) *projects.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.events {
		if f.events[i].Action == action {
			return &f.events[i]
		}
	}
	return nil
}

// clock is a fake time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// rig is a Service over fakes.
type rig struct {
	svc         *Service
	store       *memWatches
	plans       *fakePlans
	history     *fakeHistory
	reader      *fakeReader
	rollbacks   *fakeRollbacks
	deployments *fakeDeployments
	authz       *fakeAuthz
	audit       *fakeAudit
	clock       *clock
	manifests   fakeManifests
}

var (
	depStart = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	finished = depStart.Add(10 * time.Minute)
)

func newRig(t *testing.T, release string) *rig {
	t.Helper()
	body := "version: 1\n"
	if release != "" {
		body += "environments:\n  production:\n    release:\n" + release
	}
	m, err := manifest.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	plans := &fakePlans{plans: map[string]*planning.Plan{}, bindings: map[string][]*planning.Binding{}}
	r := &rig{
		store: &memWatches{plans: plans}, plans: plans, history: &fakeHistory{}, reader: &fakeReader{},
		rollbacks: &fakeRollbacks{plans: plans}, deployments: &fakeDeployments{}, authz: &fakeAuthz{}, audit: &fakeAudit{},
		clock:     &clock{now: finished},
		manifests: fakeManifests{revs: map[string]*manifest.Manifest{"man_1": m, "man_latest": m}},
	}
	r.svc = NewService(Deps{
		Store: r.store, Rollbacks: r.rollbacks, Plans: plans, Deployments: r.deployments, History: r.history,
		Health: r.reader, Manifests: r.manifests, Projects: fakeProjects{}, Authz: r.authz, Audit: r.audit, Now: r.clock.Now,
	})
	return r
}

// binding records a service binding in production, last written by plan_1.
func (r *rig) binding(name, provider string) {
	r.plans.bindings["env_p"] = append(r.plans.bindings["env_p"], &planning.Binding{
		ID: "bind_" + name, OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Subject: planning.ServiceSubject,
		Name: name, Provider: provider, Revision: "rev_" + name, PlanID: "plan_1",
	})
}

// release is plan_1 in production, made from man_1, changing each service
// with op.
func release(actions ...planning.Action) *planning.Plan {
	return &planning.Plan{
		ID: "plan_1", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Environment: "production",
		Kind: planning.KindRelease, Status: planning.StatusApproved, ManifestID: "man_1", Actions: actions,
	}
}

func service(name, provider string, op planning.Op) planning.Action {
	return planning.Action{
		ID: "service:" + name, Op: op, Subject: planning.ServiceSubject, Name: name, Provider: provider,
		Service: &planning.ServiceConfig{Provider: provider, Port: 8080, Public: true},
	}
}

// succeeded is dep_1, plan's deployment, stored succeeded.
func succeeded(plan *planning.Plan) *deployments.Deployment {
	at := finished
	return &deployments.Deployment{
		ID: "dep_1", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Environment: "production", PlanID: plan.ID,
		Status: deployments.StatusSucceeded, StartedAt: depStart, FinishedAt: &at,
	}
}

// arm runs what a successful finish does: Prepare, then Arm with the
// settled deployment. It records the deployment in the history.
func (r *rig) arm(t *testing.T, plan *planning.Plan, d *deployments.Deployment) {
	t.Helper()
	r.plans.put(plan)
	r.history.push(planning.ReleaseRecord{
		DeploymentID: d.ID, PlanID: plan.ID, Kind: plan.Kind, Status: d.Status, StartedAt: d.StartedAt, FinishedAt: *d.FinishedAt,
	})
	running := *d
	running.Status, running.FinishedAt = deployments.StatusRunning, nil
	decision, err := r.svc.Prepare(t.Context(), &running, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.svc.Arm(t.Context(), d, decision); err != nil {
		t.Fatal(err)
	}
}
