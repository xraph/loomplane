package releaseprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/dispatch"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	dispatchmemory "github.com/xraph/dispatch/store/memory"
	"github.com/xraph/grove/driver"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// rig is a Service over fakes: the world's sources, a store in memory, a
// real Dispatch engine over a memory store, and recorders for everything
// it tells anyone.
type rig struct {
	w         *world
	svc       *Service
	store     *memStore
	runs      *fakeRuns
	requests  *fakeRequests
	teams     *fakeTeams
	planStore *fakePlanStore
	places    *fakePlaces
	audit     *fakeAudit
	notices   *noticeLog
	shield    *fakeShield
	tenants   *fakeTenants
	jobStore  *dispatchmemory.Store
	jobs      *dispatchengine.Engine
	warnings  []string
	mu        sync.Mutex
	clock     time.Time
}

func (r *rig) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clock
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(d)
}

var rigNow = time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC)

func newRig(t *testing.T) *rig {
	t.Helper()
	w := greenWorld()
	r := &rig{
		w: w, store: newMemStore(), runs: newFakeRuns(), requests: &fakeRequests{},
		teams:     &fakeTeams{members: []string{"usr_lee", "usr_ada"}},
		planStore: &fakePlanStore{plans: map[string]*planning.Plan{}},
		places:    &fakePlaces{},
		audit:     &fakeAudit{}, notices: &noticeLog{}, shield: &fakeShield{},
		tenants: &fakeTenants{off: map[string]bool{}}, clock: rigNow,
	}
	r.jobStore = dispatchmemory.New()
	d, err := dispatch.New(dispatch.WithStore(r.jobStore))
	if err != nil {
		t.Fatal(err)
	}
	if r.jobs, err = dispatchengine.Build(d); err != nil {
		t.Fatal(err)
	}
	r.svc = NewService(Deps{
		Store: r.store, Sources: w.sources(), Runs: r.runs, Requests: r.requests,
		Jobs:      func() *dispatchengine.Engine { return r.jobs },
		Safety:    &agentsafety.Scanner{Engine: r.shield},
		Binder:    &agentscope.Binder{Tenants: r.tenants},
		Teams:     r.teams,
		PlanStore: r.planStore,
		Places:    r.places,
		Audit:     r.audit,
		Notify:    r.notices.enqueue,
		Now:       r.now,
		Warn: func(msg string, err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.warnings = append(r.warnings, fmt.Sprintf("%s: %v", msg, err))
		},
	})
	return r
}

// dev asks for releases; ada approves production; vi only reads.
func user(id string) security.Principal {
	return security.Principal{OrgID: "org_a", UserID: id}
}

var (
	dev = user("usr_dev")
	ada = user("usr_ada")
	vi  = user("usr_vi")
)

// openPlan is a ready plan for production that someone is deciding on.
func (r *rig) openPlan(kind, from, createdBy string) *planning.Plan {
	p := *r.w.plan
	p.ID, p.Kind, p.CreatedBy = "plan_open", kind, createdBy
	p.From, p.FromEnvironmentID = "", ""
	if from != "" {
		p.From, p.FromEnvironmentID = from, r.w.staging.ID
	}
	r.w.plans.open = []*planning.Plan{&p}
	return &p
}

// reviewing stores a report that's being reviewed: plan_a ready, its task
// in the runs, with status.
func (r *rig) reviewing(t *testing.T, taskStatus, decision string) *ReleaseReport {
	t.Helper()
	rep := &ReleaseReport{
		OrgID: "org_a", ProjectID: "proj_a", FromEnvironmentID: "env_stg", FromEnvironment: "staging",
		ToEnvironmentID: "env_prd", ToEnvironment: "production", Scope: []string{"api"},
		Status: StatusReviewing, State: StateReviewing, RequestedBy: "usr_dev", Via: ViaDashboard,
		PlanID: "plan_a", PlanRevision: "rev_1", TaskID: "atask_1", RunID: "arun_1", Decision: decision,
	}
	if err := r.store.Create(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	plan := *r.w.plan
	r.planStore.plans["plan_a"] = &plan
	r.w.plans.byID = map[string]*planning.Plan{"plan_a": &plan}
	if taskStatus != "" {
		r.runs.add(&agentruns.Task{
			ID: "atask_1", OrgID: "org_a", ProjectID: "proj_a", RunID: "arun_1", Kind: agentruns.KindReleasePreparation,
			Status: taskStatus, RequestedBy: "usr_dev", ReleasePlanID: "plan_a", ReleaseRevision: "rev_1",
		})
	}
	return rep
}

// pendingJobs are the prepare jobs waiting in Dispatch.
func (r *rig) pendingJobs(t *testing.T) []*job.Job {
	t.Helper()
	all, err := r.jobStore.ListJobsByState(context.Background(), job.StatePending, job.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var out []*job.Job
	for _, j := range all {
		if j.Name == JobPrepare {
			out = append(out, j)
		}
	}
	return out
}

func payloadOf(t *testing.T, j *job.Job) JobPayload {
	t.Helper()
	var p JobPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// memStore is the Store in memory, with PgStore's rules: one planning or
// reviewing report per target environment, Settle and Finish only from the
// state read, Finish's send in the same step as its settle.
type memStore struct {
	mu       sync.Mutex
	reports  map[string]*ReleaseReport
	notified map[string]string
	order    []string
	next     int
	settles  int
	// beforeFinish, when set, runs as Finish starts, before its
	// compare-and-set: tests land a cancel there.
	beforeFinish func()
}

func newMemStore() *memStore {
	return &memStore{reports: map[string]*ReleaseReport{}, notified: map[string]string{}}
}

func cloneReport(r *ReleaseReport) *ReleaseReport {
	c := *r
	c.Scope = slices.Clone(r.Scope)
	c.Gates = slices.Clone(r.Gates)
	c.Concerns = slices.Clone(r.Concerns)
	return &c
}

func active(r *ReleaseReport) bool { return r.Status == StatusPlanning || r.Status == StatusReviewing }

func unsettledReport(r *ReleaseReport) bool { return active(r) || !Terminal(r.State) }

func (m *memStore) Create(_ context.Context, r *ReleaseReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.RequestedBy == "" || r.OrgID == "" || r.ProjectID == "" || r.ToEnvironmentID == "" {
		return errors.New("memStore: refused")
	}
	for _, o := range m.reports {
		if o.OrgID == r.OrgID && o.ToEnvironmentID == r.ToEnvironmentID && active(o) && active(r) {
			return ErrInProgress
		}
	}
	if r.ID == "" {
		m.next++
		r.ID = fmt.Sprintf("rrep_%d", m.next)
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt, r.UpdatedAt = rigNow, rigNow
	}
	if r.Scope == nil {
		r.Scope = []string{}
	}
	m.reports[r.ID] = cloneReport(r)
	m.order = append(m.order, r.ID)
	return nil
}

func (m *memStore) get(orgID, id string) (*ReleaseReport, error) {
	r, ok := m.reports[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *memStore) Get(_ context.Context, orgID, id string) (*ReleaseReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return nil, err
	}
	return cloneReport(r), nil
}

func (m *memStore) List(_ context.Context, orgID, projectID string, limit int) ([]*ReleaseReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*ReleaseReport{}
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		r := m.reports[m.order[i]]
		if r.OrgID == orgID && r.ProjectID == projectID {
			out = append(out, cloneReport(r))
		}
	}
	return out, nil
}

func (m *memStore) Active(_ context.Context, orgID, toEnvironmentID string) (*ReleaseReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reports {
		if r.OrgID == orgID && r.ToEnvironmentID == toEnvironmentID && active(r) {
			return cloneReport(r), nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) ForPlan(_ context.Context, orgID, planID string) (*ReleaseReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.order) - 1; i >= 0; i-- {
		r := m.reports[m.order[i]]
		if r.OrgID == orgID && planID != "" && r.PlanID == planID {
			return cloneReport(r), nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) Unsettled(_ context.Context, limit int) ([]*ReleaseReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*ReleaseReport{}
	for _, id := range m.order {
		if r := m.reports[id]; unsettledReport(r) && len(out) < limit {
			out = append(out, cloneReport(r))
		}
	}
	return out, nil
}

func (m *memStore) SetPlan(_ context.Context, orgID, id, planID string, reused bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return false, err
	}
	if r.Status != StatusPlanning || r.PlanID != "" {
		return false, nil
	}
	r.PlanID, r.ReusedPlan = planID, reused
	return true, nil
}

func (m *memStore) SetEvidence(_ context.Context, orgID, id, planRevision string, ev *ReadinessEvidence, gates []GateCheck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return err
	}
	if r.Status != StatusPlanning {
		return ErrMovedOn
	}
	r.PlanRevision, r.Evidence, r.Gates, r.Preview = planRevision, ev, gates, true
	return nil
}

func (m *memStore) SetTask(_ context.Context, orgID, id, taskID, runID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return false, err
	}
	if r.Status != StatusPlanning || r.TaskID != "" {
		return false, nil
	}
	r.TaskID, r.RunID, r.Status, r.State = taskID, runID, StatusReviewing, StateReviewing
	return true, nil
}

func (m *memStore) RecordEvaluation(_ context.Context, orgID, id string, gates []GateCheck, decision string, ev *ReadinessEvidence, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return err
	}
	if r.Status != StatusReviewing {
		return ErrMovedOn
	}
	r.Gates, r.Decision, r.Preview, r.EvaluatedAt = gates, decision, false, &at
	if ev != nil {
		r.Evidence = ev
	}
	return nil
}

func (m *memStore) AddConcern(_ context.Context, orgID, id string, c Concern) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return err
	}
	if len(r.Concerns) >= MaxConcerns {
		return ErrTooManyConcerns
	}
	r.Concerns = append(r.Concerns, c)
	return nil
}

func (m *memStore) Settle(_ context.Context, orgID, id, from, status, state, reason, errText string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return err
	}
	if status != StatusDone && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("memStore: can't settle as %q", status)
	}
	if r.State != from || !unsettledReport(r) {
		return ErrMovedOn
	}
	m.settles++
	r.Status, r.State, r.StateReason, r.Error, r.UpdatedAt = status, state, reason, errText, at
	if r.FinishedAt == nil {
		r.FinishedAt = &at
	}
	return nil
}

func (m *memStore) Finish(ctx context.Context, orgID, id, from, status, state, reason, errText string, at time.Time, send func(ctx context.Context, tx NoticeTx) error) error {
	if hook := m.beforeFinish; hook != nil {
		m.beforeFinish = nil
		hook()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.get(orgID, id)
	if err != nil {
		return err
	}
	if status != StatusDone && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("memStore: can't finish as %q", status)
	}
	if r.State != from || !unsettledReport(r) {
		return ErrMovedOn
	}
	before := cloneReport(r)
	m.settles++
	r.Status, r.State, r.StateReason, r.Error, r.UpdatedAt = status, state, reason, errText, at
	if r.FinishedAt == nil {
		r.FinishedAt = &at
	}
	if send != nil {
		if err := send(ctx, fakeTx{}); err != nil {
			m.reports[id] = before
			m.settles--
			return err
		}
		m.notified[id] = state
	}
	return nil
}

func (m *memStore) stored(t *testing.T, id string) *ReleaseReport {
	t.Helper()
	r, err := m.Get(context.Background(), "org_a", id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reports)
}

// fakeTx is the notice transaction: the fakes never query it.
type fakeTx struct{}

func (fakeTx) Exec(context.Context, string, ...any) (driver.Result, error) {
	return nil, errors.New("fakeTx: no database")
}

func (fakeTx) Query(context.Context, string, ...any) (driver.Rows, error) {
	return nil, errors.New("fakeTx: no database")
}

// fakeRuns holds tasks by run ID and records cancels.
type fakeRuns struct {
	mu          sync.Mutex
	byRun       map[string]*agentruns.Task
	byRunErr    error
	byRunCalls  int
	cancels     []string // Cancel, as the person
	cancelTasks []string // CancelTask, for an approver
	starts      []agentruns.StartInput
	// startFn answers Start; nil refuses.
	startFn func(agentruns.StartInput) (*agentruns.Task, error)
}

func newFakeRuns() *fakeRuns { return &fakeRuns{byRun: map[string]*agentruns.Task{}} }

func (f *fakeRuns) add(t *agentruns.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRun[t.RunID] = t
}

func (f *fakeRuns) Start(_ context.Context, in agentruns.StartInput) (*agentruns.Task, error) {
	f.mu.Lock()
	f.starts = append(f.starts, in)
	fn := f.startFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("not in this test")
	}
	return fn(in)
}

func (f *fakeRuns) started() []agentruns.StartInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

func (f *fakeRuns) ByRun(_ context.Context, orgID, runID string) (*agentruns.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byRunCalls++
	if f.byRunErr != nil {
		return nil, f.byRunErr
	}
	t, ok := f.byRun[runID]
	if !ok || t.OrgID != orgID {
		return nil, agentruns.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (f *fakeRuns) cancel(id string) error {
	for _, t := range f.byRun {
		if t.ID == id {
			if agentruns.Terminal(t.Status) {
				return agentruns.ErrNotCancelable
			}
			t.Status = agentruns.StatusCancelled
			return nil
		}
	}
	return agentruns.ErrNotFound
}

func (f *fakeRuns) Cancel(_ context.Context, p security.Principal, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, p.UserID+" "+taskID)
	return f.cancel(taskID)
}

func (f *fakeRuns) CancelTask(_ context.Context, orgID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelTasks = append(f.cancelTasks, orgID+" "+id)
	return f.cancel(id)
}

// cancelAll is what turning the agent off does: every open task
// cancelled, with no error of its own.
func (f *fakeRuns) cancelAll(orgID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.byRun {
		if t.OrgID == orgID && !agentruns.Terminal(t.Status) {
			t.Status, t.Error = agentruns.StatusCancelled, ""
		}
	}
}

type fakeRequests struct {
	mu      sync.Mutex
	list    []*agentrequests.Request
	err     error
	filters []agentrequests.ListFilter
	callers []security.Principal
}

func (f *fakeRequests) List(_ context.Context, p security.Principal, flt agentrequests.ListFilter) (*agentrequests.RequestList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, flt)
	f.callers = append(f.callers, p)
	if f.err != nil {
		return nil, f.err
	}
	return &agentrequests.RequestList{Requests: f.list}, nil
}

type fakeTeams struct {
	mu      sync.Mutex
	members []string
	err     error
	calls   [][4]string
}

func (f *fakeTeams) Resolve(_ context.Context, _ notifications.Querier, orgID, projectID, named, reason string) (teams.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [4]string{orgID, projectID, named, reason})
	if f.err != nil {
		return teams.Route{}, f.err
	}
	return teams.Route{Team: "Platform", Source: "project", Members: slices.Clone(f.members)}, nil
}

// fakePlanStore reads plans with no principal, as planning.PgStore does.
// It has no way to change one.
type fakePlanStore struct {
	mu    sync.Mutex
	plans map[string]*planning.Plan
	err   error
	reads int
}

func (f *fakePlanStore) GetPlan(_ context.Context, orgID, id string) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.plans[id]
	if !ok || p.OrgID != orgID {
		return nil, planning.ErrNotFound
	}
	c := *p
	return &c, nil
}

// fakePlaces says whether projects and environments still exist.
type fakePlaces struct {
	mu         sync.Mutex
	goneProj   map[string]bool
	goneEnv    map[string]bool
	err        error
	projReads  int
	envReads   int
	lastOrgIDs []string
}

func (f *fakePlaces) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projReads++
	f.lastOrgIDs = append(f.lastOrgIDs, orgID)
	if f.err != nil {
		return nil, f.err
	}
	if f.goneProj[id] {
		return nil, projects.ErrNotFound
	}
	return &projects.Project{ID: id, OrgID: orgID, Name: "Acme"}, nil
}

func (f *fakePlaces) GetEnvironment(_ context.Context, orgID, projectID, id string) (*projects.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envReads++
	if f.err != nil {
		return nil, f.err
	}
	if f.goneEnv[id] {
		return nil, projects.ErrEnvironmentNotFound
	}
	return &projects.Environment{ID: id, OrgID: orgID, ProjectID: projectID}, nil
}

func (f *fakeAudit) named(action string) []projects.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []projects.AuditEvent
	for _, e := range f.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// noticeLog is the notifications outbox.
type noticeLog struct {
	mu      sync.Mutex
	notices []notifications.Notice
	err     error
}

func (n *noticeLog) enqueue(_ context.Context, _ notifications.Execer, notice notifications.Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.notices = append(n.notices, notice)
	return nil
}

func (n *noticeLog) all() []notifications.Notice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.notices)
}

// fakeShield is shield: outbound text holding a credential is blocked;
// everything else passes. It records what it scanned.
type fakeShield struct {
	mu       sync.Mutex
	outbound []string
}

func (s *fakeShield) ScanInput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: in.Text}, nil
}

func (s *fakeShield) ScanOutput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbound = append(s.outbound, in.Text)
	if strings.Contains(in.Text, "AKIA") {
		return &scan.Result{Blocked: true, Decision: scan.DecisionBlock}, nil
	}
	return &scan.Result{Decision: scan.DecisionAllow}, nil
}

func (s *fakeShield) scanned() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.outbound)
}

// fakeTenants is agentsettings' tenant lookup: a workspace whose agent is
// off can't be bound.
type fakeTenants struct {
	mu  sync.Mutex
	off map[string]bool
}

func (f *fakeTenants) NexusTenant(_ context.Context, orgID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.off[orgID] {
		return "", agentsettings.ErrDisabled
	}
	return "tenant_" + orgID, nil
}

type fakePush struct {
	err    error
	states []string
}

func (f *fakePush) ReleasePush(_ context.Context, r *ReleaseReport, state string) (*notifications.Push, error) {
	f.states = append(f.states, r.ID+":"+state)
	if f.err != nil {
		return nil, f.err
	}
	return &notifications.Push{Class: notifications.ClassUpdate, Category: notifications.CategoryAgent, Collapse: "release:" + r.ID}, nil
}
