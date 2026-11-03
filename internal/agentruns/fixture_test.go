package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex"
	cortexagent "github.com/xraph/cortex/agent"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/session"
	"github.com/xraph/cortex/store"
	"github.com/xraph/cortex/suspension"
	"github.com/xraph/dispatch"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	dispatchmemory "github.com/xraph/dispatch/store/memory"
	"github.com/xraph/warden"
	wardenmemory "github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Workspaces: Engines (orgA), where Ada owns, Al administers, Bo develops
// and Vi views; Ledger (orgB), where Bob owns.
const (
	orgA = "o"
	orgB = "o2"
	ada  = "usr_ada"
	al   = "usr_al"
	bo   = "usr_bo"
	vi   = "usr_vi"
	bob  = "usr_bob"
)

// memberBook is who belongs to which workspace, and as what.
type memberBook struct {
	mu   sync.Mutex
	orgs map[string]map[string]string
}

func newMembers() *memberBook {
	return &memberBook{orgs: map[string]map[string]string{
		orgA: {ada: security.MemberOwner, al: security.MemberAdmin, bo: security.MemberMember, vi: security.MemberViewer},
		orgB: {bob: security.MemberOwner},
	}}
}

func (m *memberBook) lookup(_ context.Context, org, user string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.orgs[org][user]
	return role, ok, nil
}

func (m *memberBook) set(org, user, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if role == "" {
		delete(m.orgs[org], user)
		return
	}
	m.orgs[org][user] = role
}

func person(org, user string) security.Principal {
	return security.Principal{Kind: security.KindUser, UserID: user, OrgID: org}
}

func personPtr(org, user string) *security.Principal {
	p := person(org, user)
	return &p
}

// newAuthz is the real Authorizer over an in-memory Warden, with both
// workspaces' agents turned on.
func newAuthz(t *testing.T, m *memberBook) *security.Authorizer {
	t.Helper()
	store := wardenmemory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	a := security.NewAuthorizer(eng, store, m.lookup)
	for org, people := range m.orgs {
		if err := a.AssignAgent(t.Context(), org); err != nil {
			t.Fatal(err)
		}
		for user, role := range people {
			if err := a.Assign(t.Context(), org, user, security.RoleForMember(role), "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a
}

// memStore is the Store over maps.
type memStore struct {
	mu      sync.Mutex
	tasks   map[string]*Task
	records map[string]Record
	queued  map[string]time.Time // continue_queued_at
	enq     map[string]time.Time // enqueued_at
	events  map[string][]Event
	now     func() time.Time
}

func newMemStore(now func() time.Time) *memStore {
	return &memStore{
		tasks: map[string]*Task{}, records: map[string]Record{}, queued: map[string]time.Time{},
		enq: map[string]time.Time{}, events: map[string][]Event{}, now: now,
	}
}

func clone(t *Task) *Task {
	c := *t
	c.Metadata = maps.Clone(t.Metadata)
	if t.FinishedAt != nil {
		f := *t.FinishedAt
		c.FinishedAt = &f
	}
	return &c
}

func (m *memStore) Create(_ context.Context, t *Task, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[t.ID]; ok {
		return errors.New("duplicate")
	}
	m.tasks[t.ID] = clone(t)
	m.records[t.ID] = rec
	m.enq[t.ID] = t.CreatedAt
	return nil
}

func (m *memStore) find(orgID, id string) (*Task, error) {
	t, ok := m.tasks[id]
	if !ok || t.OrgID != orgID {
		return nil, ErrNotFound
	}
	return t, nil
}

func (m *memStore) Get(_ context.Context, orgID, id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	return clone(t), nil
}

func (m *memStore) ByRun(_ context.Context, orgID, runID string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.OrgID == orgID && t.RunID == runID {
			return clone(t), nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) Record(_ context.Context, orgID, id string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, id); err != nil {
		return nil, err
	}
	r := m.records[id]
	return &r, nil
}

func (m *memStore) Session(_ context.Context, orgID, sessionID string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *Task
	for _, t := range m.tasks {
		if t.OrgID == orgID && t.SessionID == sessionID && (found == nil || t.CreatedAt.After(found.CreatedAt)) {
			found = t
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return clone(found), nil
}

func (m *memStore) List(_ context.Context, orgID string, f StoreFilter) ([]*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Task
	for _, t := range m.tasks {
		if t.OrgID != orgID || (f.ProjectID != "" && t.ProjectID != f.ProjectID) || (f.Kind != "" && t.Kind != f.Kind) ||
			(f.RequestedBy != "" && t.RequestedBy != f.RequestedBy) {
			continue
		}
		out = append(out, clone(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *memStore) Open(_ context.Context, orgID string) ([]*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Task
	for _, t := range m.tasks {
		if t.OrgID == orgID && !Terminal(t.Status) {
			out = append(out, clone(t))
		}
	}
	return out, nil
}

func (m *memStore) Transition(_ context.Context, orgID, id string, tr Transition) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.find(orgID, id)
	if err != nil {
		return false, err
	}
	if !slices.Contains(tr.From, t.Status) {
		return false, nil
	}
	t.Status = tr.Status
	if tr.Error != "" {
		t.Error = tr.Error
	}
	if tr.Tokens != nil {
		t.Tokens = *tr.Tokens
	}
	t.UpdatedAt = m.now()
	if Terminal(tr.Status) {
		at := m.now()
		t.FinishedAt = &at
	}
	if tr.Event != nil {
		m.append(t, []NewEvent{*tr.Event})
	}
	return true, nil
}

func (m *memStore) ClaimContinue(_ context.Context, orgID, id string, now time.Time, stale time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.find(orgID, id)
	if err != nil || Terminal(t.Status) {
		return false, err
	}
	if at, ok := m.queued[id]; ok && !at.Before(now.Add(-stale)) {
		return false, nil
	}
	m.queued[id] = now
	return true, nil
}

func (m *memStore) ClearContinue(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, id); err != nil {
		return err
	}
	delete(m.queued, id)
	return nil
}

func (m *memStore) MarkEnqueued(_ context.Context, orgID, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, id); err != nil {
		return err
	}
	m.enq[id] = at
	return nil
}

func (m *memStore) append(t *Task, in []NewEvent) int64 {
	for _, e := range in {
		t.EventSeq++
		m.events[t.ID] = append(m.events[t.ID], Event{TaskID: t.ID, Seq: t.EventSeq, Kind: e.Kind, Data: e.Data, At: m.now()})
	}
	return t.EventSeq
}

func (m *memStore) AppendEvents(_ context.Context, orgID, taskID string, in []NewEvent) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.find(orgID, taskID)
	if err != nil {
		return 0, err
	}
	return m.append(t, in), nil
}

func (m *memStore) WithholdStep(_ context.Context, orgID, taskID string, e NewEvent) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.find(orgID, taskID)
	if err != nil {
		return 0, err
	}
	evs := m.events[taskID]
	var step int64
	for _, ev := range evs {
		if ev.Kind == string(engine.EventStep) {
			step = ev.Seq
		}
	}
	for i := range evs {
		if evs[i].Seq > step && evs[i].Kind == string(engine.EventToken) {
			evs[i].Data = json.RawMessage(withheldText)
		}
	}
	return m.append(t, []NewEvent{e}), nil
}

func (m *memStore) Events(_ context.Context, orgID, taskID string, after int64, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, taskID); err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range m.events[taskID] {
		if e.Seq > after {
			out = append(out, e)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) Stale(_ context.Context, queuedBefore, continueBefore time.Time, _ int) ([]StaleTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StaleTask
	for id, t := range m.tasks {
		switch {
		case t.Status == StatusQueued && m.enq[id].Before(queuedBefore):
			out = append(out, StaleTask{OrgID: t.OrgID, ID: id, Queued: true})
		case !Terminal(t.Status) && !m.queued[id].IsZero() && m.queued[id].Before(continueBefore):
			out = append(out, StaleTask{OrgID: t.OrgID, ID: id})
		}
	}
	return out, nil
}

func (m *memStore) task(t *testing.T, id string) *Task {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	got, ok := m.tasks[id]
	if !ok {
		t.Fatalf("no task %s", id)
	}
	return clone(got)
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks)
}

func (m *memStore) eventsOf(id string) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events[id])
}

// fakeSettings answers Effective and RequireBudget.
type fakeSettings struct {
	mu        sync.Mutex
	off       map[string]bool
	exhausted map[string]bool
}

func (f *fakeSettings) Effective(_ context.Context, orgID string) (*agentsettings.Effective, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.off[orgID] {
		return nil, agentsettings.ErrDisabled
	}
	return &agentsettings.Effective{
		OrgID: orgID, Provider: "anthropic", Model: "claude-sonnet-5-5", SummaryModel: "claude-haiku-5-5",
		NexusTenantID: "nx_" + orgID, MonthlyBudgetUSD: "10.00", Enabled: true,
	}, nil
}

func (f *fakeSettings) RequireBudget(_ context.Context, orgID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exhausted[orgID] {
		return agentsettings.ErrBudgetExhausted
	}
	return nil
}

func (f *fakeSettings) enabled(_ context.Context, orgID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.off[orgID], nil
}

func (f *fakeSettings) NexusTenant(_ context.Context, orgID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.off[orgID] {
		return "", agentsettings.ErrDisabled
	}
	return "nx_" + orgID, nil
}

func (f *fakeSettings) turnOff(orgID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.off[orgID] = true
}

// fakeProjects is the projects service over a map, scoped to the
// principal's organization like the real store.
type fakeProjects struct {
	authz    *security.Authorizer
	projects map[string]*projects.Project
	envs     map[string][]*projects.Environment
}

func (f *fakeProjects) add(org, id string) {
	f.projects[id] = &projects.Project{ID: id, OrgID: org, Name: "Project " + id, Slug: id}
	f.envs[id] = []*projects.Environment{
		{ID: id + "_production", ProjectID: id, Name: "production", Kind: projects.KindProduction},
		{ID: id + "_staging", ProjectID: id, Name: "staging", Kind: projects.KindStaging},
	}
}

func (f *fakeProjects) Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error) {
	proj, ok := f.projects[id]
	if !ok || proj.OrgID != p.OrgID {
		return nil, projects.ErrNotFound
	}
	if err := f.authz.Require(ctx, p, security.ProjectRead, id); err != nil {
		return nil, err
	}
	c := *proj
	return &c, nil
}

func (f *fakeProjects) Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error) {
	if _, err := f.Get(ctx, p, projectID); err != nil {
		return nil, err
	}
	return f.envs[projectID], nil
}

// fakePlans is the planning service: plans by ID, read through the
// principal's organization as the real store reads them.
type fakePlans struct {
	agentops.PlanService
	authz *security.Authorizer

	mu       sync.Mutex
	plans    map[string]*planning.Plan
	approved []string
}

func (f *fakePlans) add(p *planning.Plan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[p.ID] = p
}

func (f *fakePlans) Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error) {
	f.mu.Lock()
	plan, ok := f.plans[id]
	f.mu.Unlock()
	if !ok || plan.OrgID != p.OrgID {
		return nil, planning.ErrNotFound
	}
	if err := f.authz.Require(ctx, p, security.DeploymentRead, plan.ProjectID); err != nil {
		return nil, err
	}
	c := *plan
	return &c, nil
}

func (f *fakePlans) Create(_ context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error) {
	plan := &planning.Plan{
		ID: "plan_agent", OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: projectID + "_" + in.Environment,
		Environment: in.Environment, Status: planning.StatusReady, Revision: "r1", Executable: true,
		Actions: []planning.Action{{Op: planning.Update}}, CreatedBy: p.UserID,
	}
	f.add(plan)
	c := *plan
	return &c, nil
}

func (f *fakePlans) Approve(_ context.Context, _ security.Principal, id string, _ planning.DecideInput) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, id)
	plan := *f.plans[id]
	plan.Status = planning.StatusApproved
	return &plan, nil
}

func (f *fakePlans) approvals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.approved)
}

// streamCall is one StreamAgent call the fake engine saw.
type streamCall struct {
	ctx       context.Context
	agent     string
	input     string
	overrides engine.RunOverrides
}

// fakeEngine is cortex as the service uses it: agents by exact scope and
// name, and runs that replay the events given.
type fakeEngine struct {
	mu        sync.Mutex
	agents    map[string]*cortexagent.Config
	created   []*cortexagent.Config
	updated   []*cortexagent.Config
	streams   []streamCall
	continues []string
	cancels   []string
	runs      map[string]*run.Run

	streamErr   error
	continueErr error
	events      []engine.StreamEvent
	// onGetAgent, when set, runs inside GetAgentByName: a test's way in
	// between a job binding its task and driving the run.
	onGetAgent func()
	// script, when set, plays a run's stream instead of events, the way a
	// test needs cortex to order it. It closes nothing; replay does.
	script func(events chan<- engine.StreamEvent)
	// sessions is cortex's session store. order records the session
	// writes and streams, in order.
	sessions *fakeSessions
	order    []string
}

func newFakeEngine() *fakeEngine {
	f := &fakeEngine{agents: map[string]*cortexagent.Config{}, runs: map[string]*run.Run{}}
	f.sessions = &fakeSessions{engine: f, byID: map[string]*session.Session{}}
	return f
}

// fakeSessions is cortex's store as the service uses it: sessions only,
// found at the exact scope they were made at.
type fakeSessions struct {
	store.Store
	engine  *fakeEngine
	mu      sync.Mutex
	byID    map[string]*session.Session
	created []*session.Session
}

func (f *fakeSessions) CreateSession(ctx context.Context, s *session.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[s.ID.String()]; ok {
		return cortex.ErrAlreadyExists
	}
	c := *s
	c.Scope = cortex.ScopeFromContext(ctx)
	c.Metadata = maps.Clone(s.Metadata)
	f.byID[s.ID.String()] = &c
	f.created = append(f.created, &c)
	f.engine.note("create_session")
	return nil
}

func (f *fakeSessions) GetSession(ctx context.Context, sessionID id.SessionID) (*session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[sessionID.String()]
	if !ok || s.Scope.Canonical() != cortex.ScopeFromContext(ctx).Canonical() {
		return nil, cortex.ErrSessionNotFound
	}
	c := *s
	return &c, nil
}

// put stores s as made at the scope of ctx.
func (f *fakeSessions) put(ctx context.Context, s *session.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *s
	c.Scope = cortex.ScopeFromContext(ctx)
	f.byID[s.ID.String()] = &c
}

func (f *fakeSessions) made() []*session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created)
}

func (f *fakeEngine) note(what string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, what)
}

func (f *fakeEngine) calledInOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

func (f *fakeEngine) Store() store.Store { return f.sessions }

func agentKey(ctx context.Context, name string) string {
	return cortex.ScopeFromContext(ctx).Canonical() + "|" + name
}

func (f *fakeEngine) replay(events chan<- engine.StreamEvent) {
	f.mu.Lock()
	evs := slices.Clone(f.events)
	script := f.script
	f.mu.Unlock()
	go func() {
		defer close(events)
		if script != nil {
			script(events)
			return
		}
		for _, e := range evs {
			events <- e
		}
	}()
}

func (f *fakeEngine) StreamAgent(ctx context.Context, name, input string, o *engine.RunOverrides, events chan<- engine.StreamEvent) error {
	f.mu.Lock()
	f.streams = append(f.streams, streamCall{ctx: ctx, agent: name, input: input, overrides: *o})
	f.order = append(f.order, "stream")
	err := f.streamErr
	f.mu.Unlock()
	if err != nil {
		close(events)
		return err
	}
	f.replay(events)
	return nil
}

func (f *fakeEngine) ContinueRunStream(_ context.Context, runID id.AgentRunID, events chan<- engine.StreamEvent) error {
	f.mu.Lock()
	f.continues = append(f.continues, runID.String())
	err := f.continueErr
	f.mu.Unlock()
	if err != nil {
		close(events)
		return err
	}
	f.replay(events)
	return nil
}

func (f *fakeEngine) CancelRun(_ context.Context, runID id.AgentRunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, runID.String())
	return nil
}

func (f *fakeEngine) GetAgentByName(ctx context.Context, name string) (*cortexagent.Config, error) {
	if f.onGetAgent != nil {
		f.onGetAgent()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentKey(ctx, name)]
	if !ok {
		return nil, cortex.ErrAgentNotFound
	}
	c := *a
	c.Metadata = maps.Clone(a.Metadata)
	return &c, nil
}

func (f *fakeEngine) CreateAgent(ctx context.Context, c *cortexagent.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Scope.Canonical() != cortex.ScopeFromContext(ctx).Canonical() {
		return fmt.Errorf("created at %s from a context at %s", c.Scope.Canonical(), cortex.ScopeFromContext(ctx).Canonical())
	}
	k := agentKey(ctx, c.Name)
	if _, ok := f.agents[k]; ok {
		return cortex.ErrAlreadyExists
	}
	cc := *c
	f.agents[k] = &cc
	f.created = append(f.created, &cc)
	return nil
}

func (f *fakeEngine) UpdateAgent(ctx context.Context, c *cortexagent.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cc := *c
	f.agents[agentKey(ctx, c.Name)] = &cc
	f.updated = append(f.updated, &cc)
	return nil
}

func (f *fakeEngine) GetRun(_ context.Context, runID id.AgentRunID) (*run.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID.String()]
	if !ok {
		return nil, cortex.ErrRunNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeEngine) setRun(runID string, r *run.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[runID] = r
}

func (f *fakeEngine) calls() (streams []streamCall, continues, cancels []string, created, updated int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.streams), slices.Clone(f.continues), slices.Clone(f.cancels), len(f.created), len(f.updated)
}

// fakeHandler records what the service hands off.
type fakeHandler struct {
	mu        sync.Mutex
	suspended []suspensionCall
	finished  []string
}

type suspensionCall struct {
	taskID string
	reason string
}

func (h *fakeHandler) OnSuspended(_ context.Context, t *Task, n suspension.Notice) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.suspended = append(h.suspended, suspensionCall{taskID: t.ID, reason: string(n.Reason)})
	return nil
}

func (h *fakeHandler) OnFinished(_ context.Context, t *Task) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finished = append(h.finished, t.ID)
	return nil
}

func (h *fakeHandler) all() ([]suspensionCall, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.suspended), slices.Clone(h.finished)
}

// clock is a settable now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// world is a Service over the fakes, with the real Authorizer, Binder,
// codec and a Dispatch engine nothing works.
type world struct {
	svc      *Service
	store    *memStore
	engine   *fakeEngine
	settings *fakeSettings
	projects *fakeProjects
	plans    *fakePlans
	handler  *fakeHandler
	authz    *security.Authorizer
	jobs     *dispatchengine.Engine
	jobStore *dispatchmemory.Store
	clock    *clock
	registry *agentops.Registry
	members  *memberBook
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		engine:   newFakeEngine(),
		settings: &fakeSettings{off: map[string]bool{}, exhausted: map[string]bool{}},
		handler:  &fakeHandler{},
		members:  newMembers(),
		clock:    &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)},
		registry: agentops.NewRegistry(),
	}
	w.authz = newAuthz(t, w.members)
	w.store = newMemStore(w.clock.now)
	w.projects = &fakeProjects{authz: w.authz, projects: map[string]*projects.Project{}, envs: map[string][]*projects.Environment{}}
	w.projects.add(orgA, "prj_mono")
	w.projects.add(orgB, "prj_ledger")
	w.projects.add(orgA, "prj_two")
	w.plans = &fakePlans{authz: w.authz, plans: map[string]*planning.Plan{}}
	agentops.Builtins(w.registry, &agentops.Deps{})
	w.jobStore = dispatchmemory.New()
	d, err := dispatch.New(dispatch.WithStore(w.jobStore))
	if err != nil {
		t.Fatal(err)
	}
	if w.jobs, err = dispatchengine.Build(d); err != nil {
		t.Fatal(err)
	}
	w.svc = NewService(Deps{
		Store:    w.store,
		Jobs:     func() *dispatchengine.Engine { return w.jobs },
		Binder:   &agentscope.Binder{Tenants: w.settings},
		Registry: w.registry,
		Codec:    &agentauthz.Codec{Authz: w.authz, Enabled: w.settings.enabled},
		Now:      w.clock.now,
	})
	w.svc.engine = func() runner { return w.engine }
	w.svc.settings = w.settings
	w.svc.authz = w.authz
	w.svc.projects = w.projects
	w.svc.plans = w.plans
	w.svc.SetSuspensionHandler(w.handler)
	if err = w.svc.RegisterKind(ChatSpec()); err != nil {
		t.Fatal(err)
	}
	return w
}

// pending is the jobs waiting in Dispatch, oldest first.
func (w *world) pending(t *testing.T) []*job.Job {
	t.Helper()
	jobs, err := w.jobStore.ListJobsByState(context.Background(), job.StatePending, job.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	return jobs
}

func (w *world) pendingNamed(t *testing.T, name string) []JobPayload {
	t.Helper()
	var out []JobPayload
	for _, j := range w.pending(t) {
		if j.Name != name {
			continue
		}
		var p JobPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// start starts Ada's chat in orgA, scoped as given.
func (w *world) start(t *testing.T, in StartInput) *Task {
	t.Helper()
	if in.Kind == "" {
		in.Kind = KindChat
	}
	if in.OrgID == "" {
		in.OrgID = orgA
	}
	if in.Input == "" {
		in.Input = "How are my deployments?"
	}
	task, err := w.svc.Start(context.Background(), in)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return task
}

func (w *world) adaChat(t *testing.T) *Task {
	t.Helper()
	return w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada)})
}

// runStart runs the task's start job inline, on a context with nothing
// bound, the way a Dispatch worker runs it.
func (w *world) runStart(t *testing.T, task *Task) error {
	t.Helper()
	return StartJob(w.svc).Handler(context.Background(), JobPayload{OrgID: task.OrgID, TaskID: task.ID})
}

func (w *world) runContinue(t *testing.T, task *Task) error {
	t.Helper()
	return ContinueJob(w.svc).Handler(context.Background(), JobPayload{OrgID: task.OrgID, TaskID: task.ID})
}

// runCtx is a context carrying a run's scope, as cortex's hooks get it.
func runCtx(task *Task) context.Context {
	s := agentscope.Scope{OrgID: task.OrgID, ProjectID: task.ProjectID, EnvironmentID: task.EnvironmentID}
	return cortex.WithScope(context.Background(), s.Cortex())
}

func mustRunID(t *testing.T, s string) id.AgentRunID {
	t.Helper()
	r, err := id.ParseAgentRunID(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
