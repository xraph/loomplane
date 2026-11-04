package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/dispatch/job"
	"github.com/xraph/grove/driver"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

var admins = []string{"user_ada", "user_bob"}

// rig is a Watcher over fakes: a lease store and an event log in memory,
// the detectors' fake world, reports in agentreports' MemStore, and
// recording runs, budget, jobs and notices.
type rig struct {
	t       *testing.T
	mu      sync.Mutex
	now     time.Time
	store   *memWatchStore
	events  *fakeEvents
	world   *fakeWorld
	reports *agentreports.Service
	rstore  *agentreports.MemStore
	flaky   *flakyReports
	budget  *fakeBudget
	runs    *fakeRuns
	jobs    *recordingJobs
	notices *noticeLog
	issues  *fakeIssues
	shield  *quotingShield
	warned  []string
	w       *Watcher
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t: t, now: t0.Add(time.Hour), events: &fakeEvents{}, world: newWorld(),
		rstore: agentreports.NewMemStore(), budget: &fakeBudget{enabled: map[string]bool{org: true}},
		runs: &fakeRuns{tasks: map[string]*agentruns.Task{}}, jobs: &recordingJobs{}, notices: &noticeLog{},
		issues: &fakeIssues{accepted: map[string]bool{}}, shield: &quotingShield{},
	}
	r.store = &memWatchStore{rows: map[string]*memWatch{}, now: r.clock, notices: r.notices}
	r.flaky = &flakyReports{MemStore: r.rstore}
	r.reports = agentreports.NewService(agentreports.Deps{Store: r.flaky, Now: r.clock})
	warn := func(msg string, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.warned = append(r.warned, msg+": "+err.Error())
	}
	r.w = &Watcher{
		Store: r.store, Events: r.events, Reports: r.reports, Budget: r.budget, Runs: r.runs,
		Detector: &Detector{World: r.world, Now: r.clock},
		Safety:   &agentsafety.Scanner{Engine: r.shield}, Binder: &agentscope.Binder{Tenants: tenants{}},
		DB: adminsDB{users: admins}, Jobs: r.jobs, Issues: r.issues, Authz: memberAuthz{},
		Owner: "worker_1", Now: r.clock, Warn: warn,
	}
	return r
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func (r *rig) run() {
	r.t.Helper()
	if err := r.w.RunOrg(r.t.Context(), org); err != nil {
		r.t.Fatalf("RunOrg: %v", err)
	}
}

func (r *rig) state() *State {
	r.t.Helper()
	st, err := r.store.Get(r.t.Context(), org)
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func (r *rig) open() []*agentreports.Report {
	r.t.Helper()
	list, err := r.reports.Open(r.t.Context(), org, "", "")
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func (r *rig) only() *agentreports.Report {
	r.t.Helper()
	list := r.open()
	if len(list) != 1 {
		r.t.Fatalf("%d open reports, want 1", len(list))
	}
	return list[0]
}

func (r *rig) lookup(id string) *agentreports.Report {
	r.t.Helper()
	rep, err := r.reports.Lookup(r.t.Context(), org, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return rep
}

// fail stores a deployment whose step for component failed, finishing
// at, and records its event.
func (r *rig) fail(id, component string, at time.Time) {
	r.t.Helper()
	dep := failedDeployment(id, "env_prod")
	dep.Steps[1].ActionID, dep.Steps[1].Name = component, strings.TrimPrefix(component, "service:")
	dep.FinishedAt = &at
	r.world.deployments[id] = dep
	r.events.add(r.t, "deployment.updated", dep)
}

func (r *rig) warnings(sub string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, w := range r.warned {
		if strings.Contains(w, sub) {
			out = append(out, w)
		}
	}
	return out
}

// memWatchStore is the lease Store in memory, on the rig's clock.
type memWatchStore struct {
	mu      sync.Mutex
	rows    map[string]*memWatch
	now     func() time.Time
	notices notifications.Execer
}

type memWatch struct {
	st    *State
	owner string
	until time.Time
}

func (m *memWatchStore) Claim(_ context.Context, orgID, owner string, ttl time.Duration, highWater func() (int64, error)) (*State, bool, error) {
	hw, err := highWater()
	if err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	row, ok := m.rows[orgID]
	if !ok {
		row = &memWatch{st: &State{OrgID: orgID, EventCursor: hw, Checks: map[string]Check{}}}
		m.rows[orgID] = row
	}
	if row.owner != "" && row.owner != owner && row.until.After(now) {
		return nil, false, nil
	}
	row.owner, row.until = owner, now.Add(ttl)
	return row.st.clone(), true, nil
}

func (m *memWatchStore) save(owner string, st *State) error {
	row, ok := m.rows[st.OrgID]
	if !ok || row.owner != owner || !row.until.After(m.now()) {
		return ErrLeaseLost
	}
	row.st = st.clone()
	return nil
}

func (m *memWatchStore) Save(_ context.Context, owner string, st *State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.save(owner, st)
}

func (m *memWatchStore) SaveNotifying(ctx context.Context, owner string, st *State, send func(context.Context, notifications.Execer) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[st.OrgID]
	if !ok || row.owner != owner || !row.until.After(m.now()) {
		return ErrLeaseLost
	}
	if err := send(ctx, m.notices); err != nil {
		return err
	}
	return m.save(owner, st)
}

func (m *memWatchStore) Release(_ context.Context, orgID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[orgID]; ok && row.owner == owner {
		row.owner, row.until = "", time.Time{}
	}
	return nil
}

func (m *memWatchStore) Get(_ context.Context, orgID string) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[orgID]; ok {
		return row.st.clone(), nil
	}
	return &State{OrgID: orgID}, nil
}

// fakeEvents is one workspace's event log: events after floor are kept,
// high is the last sequence.
type fakeEvents struct {
	mu    sync.Mutex
	list  []realtime.Event
	floor int64
	high  int64
}

func (e *fakeEvents) add(t *testing.T, typ string, data any) {
	t.Helper()
	ev := event(t, typ, data)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.high++
	ev.ID = cursor(org, e.high)
	e.list = append(e.list, ev)
}

func (e *fakeEvents) HighWater(_ context.Context, o string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cursor(o, e.high), nil
}

func (e *fakeEvents) Events(_ context.Context, o, after string, limit int) ([]realtime.Event, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	seq, err := sequence(o, after)
	if err != nil || seq < e.floor || seq > e.high {
		return nil, false, nil
	}
	out := []realtime.Event{}
	for _, ev := range e.list {
		n, _ := sequence(o, ev.ID)
		if n > seq && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, true, nil
}

type fakeBudget struct {
	mu        sync.Mutex
	enabled   map[string]bool
	exhausted bool
}

func (b *fakeBudget) EnabledOrgs(context.Context) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for o, on := range b.enabled {
		if on {
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (b *fakeBudget) Enabled(_ context.Context, o string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled[o], nil
}

func (b *fakeBudget) RequireBudget(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exhausted {
		return agentsettings.ErrBudgetExhausted
	}
	return nil
}

func (b *fakeBudget) exhaust(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.exhausted = on
}

// fakeRuns starts tasks that stay running until a test says otherwise.
type fakeRuns struct {
	mu      sync.Mutex
	started []agentruns.StartInput
	tasks   map[string]*agentruns.Task // by run ID
	err     error                      // what Start returns, when set
	finish  string                     // the status ByRun reports for every task, when set
}

func (f *fakeRuns) Start(_ context.Context, in agentruns.StartInput) (*agentruns.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.started = append(f.started, in)
	n := len(f.started)
	t := &agentruns.Task{
		ID: "atask_" + string(rune('0'+n)), RunID: "arun_" + string(rune('0'+n)), OrgID: in.OrgID, Kind: in.Kind,
		Status: agentruns.StatusRunning, Metadata: in.Metadata,
	}
	f.tasks[t.RunID] = t
	return t, nil
}

func (f *fakeRuns) ByRun(_ context.Context, o, runID string) (*agentruns.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[runID]
	if !ok || t.OrgID != o {
		return nil, agentruns.ErrNotFound
	}
	out := *t
	if f.finish != "" {
		out.Status = f.finish
	}
	return &out, nil
}

func (f *fakeRuns) set(runID, status, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[runID].Status, f.tasks[runID].Error = status, why
}

func (f *fakeRuns) starts() []agentruns.StartInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

type recordingJobs struct {
	mu        sync.Mutex
	watched   []string
	delivered []string
}

func (j *recordingJobs) WatchOrg(_ context.Context, o string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.watched = append(j.watched, o)
	return nil
}

func (j *recordingJobs) Deliver(_ context.Context, o, id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.delivered = append(j.delivered, o+"/"+id)
	return nil
}

func (j *recordingJobs) deliveries() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.delivered)
}

type fakeIssues struct {
	mu       sync.Mutex
	accepted map[string]bool
	asked    int
}

func (f *fakeIssues) Access(_ context.Context, _, repo string) (repositories.IssueAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	return repositories.IssueAccess{Repository: repo, Accepted: f.accepted[repo]}, nil
}

func (*fakeIssues) Issues(context.Context, string, string) (repositories.Issues, error) {
	return nil, repositories.ErrIssuesNotAccepted
}

func (*fakeIssues) IssuesStatus(context.Context, security.Principal) (*repositories.IssuesStatus, error) {
	return &repositories.IssuesStatus{Configured: true, Installations: []repositories.InstallationIssues{{Account: "acme", Accepted: true}}}, nil
}

// tenants binds every workspace to a model tenant of its own.
type tenants struct{}

func (tenants) NexusTenant(_ context.Context, o string) (string, error) { return "tenant_" + o, nil }

// memberAuthz lets every member of org use the agent.
type memberAuthz struct{}

func (memberAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	if p.OrgID != org || perm != security.AgentUse || resourceID != "" {
		return security.ErrNotMember
	}
	return nil
}

// quotingShield quotes untrusted text with its source, as shield does,
// and records what it scanned.
type quotingShield struct {
	mu    sync.Mutex
	scans []scan.Input
}

func (s *quotingShield) ScanInput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans = append(s.scans, *in)
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: "<untrusted_content source=\"" + in.Source + "\">" + in.Text + "</untrusted_content>"}, nil
}

func (*quotingShield) ScanOutput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: in.Text}, nil
}

// adminsDB answers notifications.Admins.
type adminsDB struct{ users []string }

func (a adminsDB) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	if !strings.Contains(query, "authsome_members") {
		return nil, errors.New("adminsDB: unexpected query")
	}
	return &stringRows{vals: a.users}, nil
}

type stringRows struct {
	vals []string
	i    int
}

func (r *stringRows) Next() bool {
	r.i++
	return r.i <= len(r.vals)
}

func (r *stringRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.vals[r.i-1]
	return nil
}

func (r *stringRows) Columns() ([]string, error) { return []string{"user_id"}, nil }
func (r *stringRows) Close() error               { return nil }
func (r *stringRows) Err() error                 { return nil }

// noticeLog records the notices notifications.Enqueue queues, one row per
// recipient.
type noticeLog struct {
	mu   sync.Mutex
	rows []sentNotice
	fail bool
}

type sentNotice struct {
	Kind, Subject, User, Title, Body, Path, Need string
}

func (l *noticeLog) Exec(_ context.Context, query string, args ...any) (driver.Result, error) {
	if !strings.Contains(query, "loomplane_notification_outbox") {
		return nil, errors.New("noticeLog: unexpected statement")
	}
	var p struct{ Title, Body, Path, Need string }
	if err := json.Unmarshal([]byte(args[2].(string)), &p); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return nil, errors.New("the outbox is down")
	}
	for _, user := range args[6].([]string) {
		l.rows = append(l.rows, sentNotice{Kind: args[3].(string), Subject: args[4].(string), User: user,
			Title: p.Title, Body: p.Body, Path: p.Path, Need: p.Need})
	}
	return nil, nil
}

func (l *noticeLog) sent() []sentNotice {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.rows)
}

func TestFirstRunStartsAtHighWater(t *testing.T) {
	r := newRig(t)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	if n := len(r.open()); n != 0 {
		t.Fatalf("a failure from before the watcher started opened %d reports", n)
	}
	if st := r.state(); st.EventCursor != 1 || st.TickedAt == nil {
		t.Fatalf("state %+v, want the cursor at the high water mark", st)
	}
}

func TestOneFailedDeploymentOpensOneReportAndOneInvestigation(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	starts := r.runs.starts()
	if len(starts) != 1 {
		t.Fatalf("%d investigations started, want 1", len(starts))
	}
	in := starts[0]
	if in.Kind != agentruns.KindInvestigation || in.OnBehalfOf != nil || in.OrgID != org || in.ProjectID != project ||
		in.EnvironmentID != "env_prod" || in.Metadata[agentruns.MetaReportID] != rep.ID || in.Metadata[agentruns.MetaFingerprint] != rep.Fingerprint {
		t.Fatalf("start input %+v", in)
	}
	if !strings.HasPrefix(in.Input, "Investigate report "+rep.ID+": ") {
		t.Errorf("input %q", in.Input)
	}
	if rep.Investigation != agentreports.InvestigationRunning || rep.TaskID != "atask_1" || rep.RunID != "arun_1" {
		t.Fatalf("report investigation %s, task %q, run %q", rep.Investigation, rep.TaskID, rep.RunID)
	}
	if st := r.state(); st.EventCursor != 1 {
		t.Fatalf("cursor %d, want 1", st.EventCursor)
	}
	// The next pass starts nothing more.
	r.run()
	if n := len(r.runs.starts()); n != 1 {
		t.Fatalf("%d investigations after a second pass", n)
	}
}

func TestDeliveryWaitsForTheInvestigation(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	if got := r.jobs.deliveries(); len(got) != 0 {
		t.Fatalf("delivered while the investigation runs: %v", got)
	}
	r.runs.set("arun_1", agentruns.StatusCompleted, "")
	r.advance(time.Minute)
	r.run()
	rep := r.only()
	if rep.Investigation != agentreports.InvestigationDone {
		t.Fatalf("investigation %s, want done", rep.Investigation)
	}
	if got := r.jobs.deliveries(); !slices.Equal(got, []string{org + "/" + rep.ID}) {
		t.Fatalf("deliveries %v, want the report once", got)
	}
	r.advance(time.Minute)
	r.run()
	if got := r.jobs.deliveries(); len(got) != 1 {
		t.Fatalf("deliveries after another pass %v", got)
	}
}

func TestDeliveryGoesAheadAfterTheGrace(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	r.advance(9 * time.Minute)
	r.run()
	if got := r.jobs.deliveries(); len(got) != 0 {
		t.Fatalf("delivered within the grace: %v", got)
	}
	r.advance(2 * time.Minute)
	r.run()
	if got := r.jobs.deliveries(); len(got) != 1 {
		t.Fatalf("deliveries after 11 minutes %v, want one", got)
	}
}

func TestGapOrLongPauseJumpsToHighWater(t *testing.T) {
	t.Run("a gap", func(t *testing.T) {
		r := newRig(t)
		r.run()
		r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
		r.run()
		rep := r.only()
		// A later deployment fixed it, but its events were pruned with
		// others the watcher never read.
		fixed := succeededDeployment("dep_2", "plan_1")
		r.world.deployments[fixed.ID] = fixed
		r.world.recent = []*deployments.Deployment{fixed, r.world.deployments["dep_1"]}
		r.events.add(t, "deployment.updated", fixed)
		r.fail("dep_3", "service:web", t0.Add(25*time.Minute))
		r.events.floor = 2
		r.advance(time.Minute)
		r.run()
		if st := r.state(); st.EventCursor != 3 {
			t.Fatalf("cursor %d, want the high water mark 3", st.EventCursor)
		}
		if got := r.lookup(rep.ID); got.Status != agentreports.StatusResolved || got.Resolution != "deployment_failed no longer holds" {
			t.Fatalf("the open report wasn't rechecked: %s %q", got.Status, got.Resolution)
		}
		if n := len(r.open()); n != 0 {
			t.Fatalf("the gap was replayed: %d open reports", n)
		}
	})
	t.Run("a long pause", func(t *testing.T) {
		r := newRig(t)
		r.run()
		r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
		r.run()
		rep := r.only()
		// While the watcher was off, a deployment fixed it and another
		// failed.
		fixed := succeededDeployment("dep_2", "plan_1")
		r.world.deployments[fixed.ID] = fixed
		r.world.recent = []*deployments.Deployment{fixed, r.world.deployments["dep_1"]}
		r.advance(2 * time.Hour)
		r.events.add(t, "deployment.updated", fixed)
		r.fail("dep_3", "service:web", t0.Add(2*time.Hour))
		r.run()
		if st := r.state(); st.EventCursor != 3 {
			t.Fatalf("cursor %d, want the high water mark", st.EventCursor)
		}
		if got := r.lookup(rep.ID); got.Status != agentreports.StatusResolved || got.Resolution != "deployment_failed no longer holds" {
			t.Fatalf("the open report wasn't rechecked: %s %q", got.Status, got.Resolution)
		}
		if n := len(r.open()); n != 0 {
			t.Fatalf("what happened while off was replayed: %d reports", n)
		}
	})
}

func TestBudgetExhaustedPausesAndNotifiesOnce(t *testing.T) {
	r := newRig(t)
	r.run()
	r.budget.exhaust(true)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	st := r.state()
	if !st.Paused || st.PausedReason != PauseBudget || st.PausedAt == nil || !st.PausedAt.Equal(r.clock()) {
		t.Fatalf("state %+v, want paused for the budget", st)
	}
	rep := r.only()
	if rep.Investigation != agentreports.InvestigationSkipped || rep.InvestigationNote != noteBudget {
		t.Fatalf("investigation %s %q", rep.Investigation, rep.InvestigationNote)
	}
	if n := len(r.runs.starts()); n != 0 {
		t.Fatalf("%d investigations started without budget", n)
	}
	sent := r.notices.sent()
	if len(sent) != len(admins) {
		t.Fatalf("notices %+v, want one per admin", sent)
	}
	for i, n := range sent {
		if n.User != admins[i] || n.Kind != notifications.KindAgentBudget || n.Title != pausedTitle || n.Body != pausedBody ||
			n.Path != "/settings/ai" || n.Need != security.AIManage.Name() || n.Subject != "watcher-paused:"+org+":"+unix(r.clock()) {
			t.Errorf("notice %+v", n)
		}
	}
	// Another failure while paused: skipped too, and no second notice.
	r.advance(time.Minute)
	r.fail("dep_2", "service:web", t0.Add(70*time.Minute))
	r.run()
	if got := len(r.notices.sent()); got != len(admins) {
		t.Fatalf("%d notices after a second failure, want %d", got, len(admins))
	}
	for _, rep := range r.open() {
		if rep.Investigation != agentreports.InvestigationSkipped || rep.InvestigationNote != noteBudget {
			t.Errorf("%s: investigation %s %q", rep.Component, rep.Investigation, rep.InvestigationNote)
		}
	}
}

func unix(t time.Time) string { return itoa(t.Unix()) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestPausedWatcherStillReports(t *testing.T) {
	r := newRig(t)
	r.run()
	r.budget.exhaust(true)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	if got := r.jobs.deliveries(); !slices.Equal(got, []string{org + "/" + rep.ID}) {
		t.Fatalf("deliveries %v, want the report queued while paused", got)
	}
}

func TestResumesWhenTheBudgetAllows(t *testing.T) {
	r := newRig(t)
	r.run()
	r.budget.exhaust(true)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	pausedAt := *r.state().PausedAt
	r.budget.exhaust(false)
	r.advance(time.Minute)
	r.fail("dep_2", "service:web", t0.Add(62*time.Minute))
	r.run()
	if st := r.state(); st.Paused || st.PausedAt != nil || st.PausedReason != "" {
		t.Fatalf("state %+v, want resumed", st)
	}
	var resumed []sentNotice
	for _, n := range r.notices.sent() {
		if strings.HasPrefix(n.Subject, "watcher-resumed:") {
			resumed = append(resumed, n)
		}
	}
	if len(resumed) != len(admins) || resumed[0].Title != resumedTitle || resumed[0].Body != resumedBody ||
		resumed[0].Subject != "watcher-resumed:"+org+":"+itoa(pausedAt.Unix()) || resumed[0].Path != "/settings/ai" {
		t.Fatalf("resume notices %+v", resumed)
	}
	starts := r.runs.starts()
	if len(starts) != 1 || !strings.Contains(starts[0].Input, "failed at web") {
		t.Fatalf("starts %+v, want the new report investigated", starts)
	}
	r.advance(time.Minute)
	r.run()
	if got := len(r.notices.sent()); got != 2*len(admins) {
		t.Fatalf("%d notices after another pass", got)
	}
}

func TestRunOutOfBudgetMidRunPauses(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	r.runs.set("arun_1", agentruns.StatusFailed, "The workspace's AI budget for the month is used up.")
	r.budget.exhaust(true)
	r.advance(time.Minute)
	// No new report: only the failed run can pause the watcher.
	r.run()
	rep = r.lookup(rep.ID)
	if rep.Investigation != agentreports.InvestigationFailed || rep.InvestigationNote != noteStopped {
		t.Fatalf("investigation %s %q", rep.Investigation, rep.InvestigationNote)
	}
	if !r.state().Paused {
		t.Fatal("the watcher didn't pause")
	}
	if got := len(r.notices.sent()); got != len(admins) {
		t.Fatalf("%d notices, want one pause notice per admin", got)
	}
}

func TestPausingTwiceInAPassNotifiesOnce(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	r.runs.set("arun_1", agentruns.StatusFailed, "The workspace's AI budget for the month is used up.")
	r.budget.exhaust(true)
	r.advance(time.Minute)
	// A new failure pauses first; the failed run then finds the watcher
	// paused already.
	r.fail("dep_2", "service:web", t0.Add(62*time.Minute))
	r.run()
	if !r.state().Paused {
		t.Fatal("the watcher didn't pause")
	}
	if got := len(r.notices.sent()); got != len(admins) {
		t.Fatalf("%d notices, want one pause notice per admin", got)
	}
}

func TestInvestigationsAreCapped(t *testing.T) {
	r := newRig(t)
	r.run()
	for i, c := range []string{"service:a", "service:b", "service:c", "service:d"} {
		r.fail("dep_"+c, c, t0.Add(time.Duration(5+i)*time.Minute))
	}
	r.run()
	if n := len(r.runs.starts()); n != MaxConcurrentInvestigations {
		t.Fatalf("%d investigations started, want %d", n, MaxConcurrentInvestigations)
	}
	var capped []string
	for _, rep := range r.open() {
		if rep.Investigation == agentreports.InvestigationSkipped {
			if rep.InvestigationNote != "Three investigations were already running." {
				t.Errorf("note %q", rep.InvestigationNote)
			}
			capped = append(capped, rep.Component)
		}
	}
	// Newest first: the oldest waits.
	if !slices.Equal(capped, []string{"service:a"}) {
		t.Fatalf("capped %v, want the oldest", capped)
	}
}

func TestManualReportsAreNotInvestigated(t *testing.T) {
	r := newRig(t)
	r.run()
	obs, err := r.reports.Observe(t.Context(), agentreports.Signal{
		OrgID: org, ProjectID: project, Kind: agentreports.SignalManual, Component: "manual", Severity: agentreports.SeverityMedium,
		Title: "The API is slow on Mondays", OpenedBy: "user_ada", OpenedByTask: "atask_chat", Occurrence: "atask_chat:call_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.run()
	rep := r.lookup(obs.Report.ID)
	if rep.Investigation != agentreports.InvestigationSkipped || rep.InvestigationNote != "" {
		t.Fatalf("investigation %s %q", rep.Investigation, rep.InvestigationNote)
	}
	if n := len(r.runs.starts()); n != 0 {
		t.Fatalf("%d investigations of a manual report", n)
	}
}

// failingWorld fails to read one deployment.
type failingWorld struct {
	*fakeWorld
	failID string
}

func (w failingWorld) Deployment(ctx context.Context, o, id string) (*deployments.Deployment, error) {
	if id == w.failID {
		return nil, errors.New("the database is down")
	}
	return w.fakeWorld.Deployment(ctx, o, id)
}

func TestADetectorErrorDoesNotBlockTheCursor(t *testing.T) {
	r := newRig(t)
	r.w.Detector.World = failingWorld{fakeWorld: r.world, failID: "dep_5"}
	r.run()
	for i := 1; i <= 10; i++ {
		r.fail("dep_"+itoa(int64(i)), "service:api", t0.Add(time.Duration(i)*time.Minute))
	}
	r.run()
	if st := r.state(); st.EventCursor != 10 {
		t.Fatalf("cursor %d, want 10", st.EventCursor)
	}
	if rep := r.only(); rep.Occurrences != 9 {
		t.Fatalf("occurrences %d, want 9", rep.Occurrences)
	}
	if got := r.warnings("skip event"); len(got) != 1 || !strings.Contains(got[0], org+":5") {
		t.Fatalf("warnings %q, want one naming event 5", got)
	}
}

func TestWaitingIssuesRetryWhenAccepted(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	r.runs.set("arun_1", agentruns.StatusCompleted, "")
	r.advance(time.Minute)
	r.run()
	// The delivery ran: notice sent, issue waiting on the installation.
	waiting(t, r.rstore, rep.ID, r.clock())
	r.advance(10 * time.Minute) // past DeliveryRequeue
	r.run()
	if got := r.jobs.deliveries(); len(got) != 1 {
		t.Fatalf("deliveries %v: a waiting report went again before its installation accepted", got)
	}
	r.issues.accepted["acme/app"] = true
	r.advance(time.Minute)
	r.run()
	if got := r.jobs.deliveries(); len(got) != 2 || got[1] != org+"/"+rep.ID {
		t.Fatalf("deliveries %v, want the waiting report again", got)
	}
}

// waiting delivers a report as a delivery job that found its
// installation hasn't accepted Issues would.
func waiting(t *testing.T, s *agentreports.MemStore, id string, at time.Time) {
	t.Helper()
	ctx := t.Context()
	d, err := s.ClaimDelivery(ctx, org, id, "job", agentreports.DeliveryLease)
	if err != nil || d == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err = s.SaveNotice(ctx, org, id, "job", d.Report.Revision, d.PendingNotice,
		func(context.Context, notifications.Execer, int) error { return nil }, at); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveIssue(ctx, org, id, "job", d.Report.Revision, agentreports.IssueSave{State: agentreports.IssueWaiting, Done: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseDelivery(ctx, org, id, "job", true); err != nil {
		t.Fatal(err)
	}
}

func TestTickFansOutPerEnabledOrg(t *testing.T) {
	r := newRig(t)
	r.budget.enabled = map[string]bool{"org_b": true, "org_a": true, "org_off": false}
	if err := r.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.jobs.watched, []string{"org_a", "org_b"}) {
		t.Fatalf("watched %v", r.jobs.watched)
	}
}

func TestAWorkspaceWithTheAgentOffIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.budget.enabled[org] = false
	r.run()
	if _, ok := r.store.rows[org]; ok {
		t.Fatal("the watcher claimed a workspace with the agent off")
	}
}

func TestAnotherPassHoldingTheLeaseIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.run()
	if _, ok, err := r.store.Claim(t.Context(), org, "worker_2:x", LeaseTTL, func() (int64, error) { return 0, nil }); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	if n := len(r.open()); n != 0 {
		t.Fatalf("a pass ran under another's lease: %d reports", n)
	}
	r.advance(LeaseTTL + time.Second)
	r.run()
	if n := len(r.open()); n != 1 {
		t.Fatalf("%d reports once the lease ran out, want 1", n)
	}
}

func TestStatusShowsThePause(t *testing.T) {
	r := newRig(t)
	r.run()
	r.budget.exhaust(true)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	st, err := r.w.Status(t.Context(), security.Principal{UserID: "user_ada", OrgID: org})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Paused || st.PausedReason != PauseBudget || st.PausedAt == nil || st.TickedAt == nil || !st.Issues.Configured {
		t.Fatalf("status %+v", st)
	}
	if _, err = r.w.Status(t.Context(), security.Principal{UserID: "user_x", OrgID: "org_other"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("another workspace's status: %v", err)
	}
}

func TestDeliveryJobsEndBeforeTheirLease(t *testing.T) {
	if deliverJobTimeout >= agentreports.DeliveryLease {
		t.Fatalf("the delivery job may run %s, past its %s lease", deliverJobTimeout, agentreports.DeliveryLease)
	}
	if runTimeout >= LeaseTTL || orgJobTimeout > LeaseTTL {
		t.Fatalf("a pass may outlive its lease")
	}
	a, err := attemptOwner("worker_1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := attemptOwner("worker_1")
	if a == b || !strings.HasPrefix(a, "worker_1:") {
		t.Fatalf("attempt owners %q and %q", a, b)
	}
}

// flakyReports is the report MemStore with reads and moves that can be
// made to fail.
type flakyReports struct {
	*agentreports.MemStore
	mu          sync.Mutex
	failPending bool
	failRunning int // how many moves to running fail
}

func (f *flakyReports) set(pending bool, running int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPending, f.failRunning = pending, running
}

func (f *flakyReports) Pending(ctx context.Context, orgID string) ([]*agentreports.Report, error) {
	f.mu.Lock()
	fail := f.failPending
	f.mu.Unlock()
	if fail {
		return nil, errors.New("the reports store is down")
	}
	return f.MemStore.Pending(ctx, orgID)
}

func (f *flakyReports) SetInvestigation(ctx context.Context, orgID, id string, from []string, inv agentreports.Investigation, at time.Time) (bool, error) {
	f.mu.Lock()
	fail := f.failRunning > 0 && inv.Status == agentreports.InvestigationRunning
	if fail {
		f.failRunning--
	}
	f.mu.Unlock()
	if fail {
		return false, errors.New("the reports store is down")
	}
	return f.MemStore.SetInvestigation(ctx, orgID, id, from, inv, at)
}

func TestARepeatStillDeliversWhileItsIssueWaits(t *testing.T) {
	r := newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	r.runs.set("arun_1", agentruns.StatusCompleted, "")
	r.advance(time.Minute)
	r.run()
	waiting(t, r.rstore, rep.ID, r.clock())
	for range 3 {
		r.advance(25 * time.Minute)
		r.run()
	}
	if got := r.jobs.deliveries(); len(got) != 1 {
		t.Fatalf("deliveries %v: the waiting issue went again before its installation accepted", got)
	}
	// The failure comes back over an hour after the last notice: a repeat
	// with a notice to send, while the installation still hasn't accepted.
	r.fail("dep_2", "service:api", r.clock())
	r.run()
	if got := r.jobs.deliveries(); len(got) != 2 || got[1] != org+"/"+rep.ID {
		t.Fatalf("deliveries %v, want the repeat's notice delivered", got)
	}
	if r.lookup(rep.ID).Occurrences != 2 {
		t.Fatal("the failure wasn't counted as a repeat")
	}
}

func TestALaterStepFailingDoesNotDropEvents(t *testing.T) {
	r := newRig(t)
	r.run()
	r.flaky.set(true, 0)
	// Two hours of passes whose delivery step fails every time.
	for range 4 {
		r.advance(30 * time.Minute)
		if err := r.w.RunOrg(t.Context(), org); err == nil {
			t.Fatal("a pass with a failing delivery step succeeded")
		}
	}
	r.fail("dep_1", "service:api", r.clock())
	r.advance(30 * time.Second)
	if err := r.w.RunOrg(t.Context(), org); err == nil {
		t.Fatal("a pass with a failing delivery step succeeded")
	}
	if n := len(r.open()); n != 1 {
		t.Fatalf("%d reports: the pass jumped past an event the watcher could read", n)
	}
	if st := r.state(); st.EventCursor != 1 || st.TickedAt == nil || !st.TickedAt.Equal(r.clock()) {
		t.Fatalf("state %+v, want the tick recorded with the cursor", st)
	}
}

func TestAnInvestigationThatNeverEndsFreesItsSlot(t *testing.T) {
	r := newRig(t)
	r.run()
	for i, c := range []string{"service:a", "service:b", "service:c"} {
		r.fail("dep_"+c, c, t0.Add(time.Duration(5+i)*time.Minute))
	}
	r.run()
	// One of them waits on a person it can never ask, and never ends.
	r.runs.set("arun_2", agentruns.StatusWaiting, "")
	for range 2 {
		r.advance(30 * time.Minute)
		r.run()
	}
	r.advance(time.Minute)
	r.run()
	var unfinished int
	for _, rep := range r.open() {
		if rep.Investigation == agentreports.InvestigationFailed {
			if rep.InvestigationNote != "The investigation didn't finish within an hour." {
				t.Errorf("note %q", rep.InvestigationNote)
			}
			unfinished++
		}
	}
	if unfinished != 3 {
		t.Fatalf("%d investigations failed at the ceiling, want 3", unfinished)
	}
	// The slots are free again: a new report is investigated.
	r.fail("dep_new", "service:d", r.clock())
	r.run()
	if n := len(r.runs.starts()); n != 4 {
		t.Fatalf("%d investigations, want a fourth once the slots freed", n)
	}
}

func TestALiveRunWhoseMoveFailedKeepsItsFindings(t *testing.T) {
	r := newRig(t)
	r.run()
	r.flaky.set(false, moveAttempts)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	if err := r.w.RunOrg(t.Context(), org); err == nil {
		t.Fatal("the pass hid that it couldn't record the run")
	}
	r.flaky.set(false, 0)
	rep := r.only()
	if rep.Investigation != agentreports.InvestigationQueued || rep.RunID != "" || len(r.runs.starts()) != 1 {
		t.Fatalf("investigation %s, run %q, %d starts", rep.Investigation, rep.RunID, len(r.runs.starts()))
	}
	// Within the hour the watcher leaves it alone; the run writes its
	// findings, its task record naming the report.
	r.advance(20 * time.Minute)
	r.run()
	if got := r.lookup(rep.ID); got.Investigation != agentreports.InvestigationQueued {
		t.Fatalf("a live run's report went %s within the hour", got.Investigation)
	}
	if _, err := r.reports.WriteFindings(t.Context(), org, rep.ID, agentreports.Writer{TaskID: "atask_1", Investigates: rep.ID},
		agentreports.FindingsInput{Summary: "The api's start script is missing.", Confidence: "high"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r.advance(25 * time.Minute)
		r.run()
	}
	if got := r.lookup(rep.ID); got.Investigation != agentreports.InvestigationDone {
		t.Fatalf("investigation %s %q, want done with its findings", got.Investigation, got.InvestigationNote)
	}
	if n := len(r.runs.starts()); n != 1 {
		t.Fatalf("%d starts, want the one run", n)
	}
}

func TestWatcherJobsRunOnTheSweepQueue(t *testing.T) {
	w := &Watcher{}
	for name, opts := range map[string]job.Options{
		JobTick: TickJob(w).Opts, JobOrg: OrgJob(w).Opts, JobDeliver: DeliverJob(nil, "worker_1").Opts,
	} {
		if opts.Queue != agentruns.SweepQueue {
			t.Errorf("%s runs on %q, want %q", name, opts.Queue, agentruns.SweepQueue)
		}
	}
	if got := DeliverJob(nil, "w").Opts.MaxRetries; got != 8 {
		t.Errorf("the delivery job retries %d times, want 8", got)
	}
	if DeliverJob(nil, "w").Opts.Timeout >= agentreports.DeliveryLease {
		t.Error("the delivery job outlives its lease")
	}
	if (DispatchEnqueuer{}).queue() != agentruns.SweepQueue {
		t.Error("the enqueuer's default queue isn't the sweep queue")
	}
}

// auditLog records audited actions.
type auditLog struct {
	mu      sync.Mutex
	actions []string
}

func (a *auditLog) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, e.Action)
}

func (a *auditLog) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.actions)
}

func TestAPauseIsAuditedOnlyOnceSaved(t *testing.T) {
	r := newRig(t)
	audit := &auditLog{}
	r.w.Audit = audit
	r.run()
	r.budget.exhaust(true)
	r.notices.mu.Lock()
	r.notices.fail = true
	r.notices.mu.Unlock()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	if err := r.w.RunOrg(t.Context(), org); err == nil {
		t.Fatal("the pause saved without its notice")
	}
	if r.state().Paused || len(audit.recorded()) != 0 {
		t.Fatalf("paused %v, audit %v: nothing was saved, so nothing is audited", r.state().Paused, audit.recorded())
	}
	r.notices.mu.Lock()
	r.notices.fail = false
	r.notices.mu.Unlock()
	r.advance(time.Minute)
	r.fail("dep_2", "service:web", t0.Add(62*time.Minute))
	r.run()
	if !r.state().Paused || !slices.Equal(audit.recorded(), []string{"agent.watcher.pause"}) {
		t.Fatalf("paused %v, audit %v", r.state().Paused, audit.recorded())
	}
	r.budget.exhaust(false)
	r.advance(time.Minute)
	r.run()
	if got := audit.recorded(); !slices.Equal(got, []string{"agent.watcher.pause", "agent.watcher.resume"}) {
		t.Fatalf("audit %v", got)
	}
}

func TestALiveRunIsRecordedDespiteAFailedMove(t *testing.T) {
	r := newRig(t)
	r.run()
	r.flaky.set(false, moveAttempts-1)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	if rep := r.only(); rep.Investigation != agentreports.InvestigationRunning || rep.RunID != "arun_1" {
		t.Fatalf("investigation %s, run %q, want the run recorded", rep.Investigation, rep.RunID)
	}
}

// pgError is the kind of error a run fails with: it names the host, the
// user and the database.
const pgError = `failed to connect to host=db.internal user=loomplane database=loomplane: dial tcp 10.0.3.7:5432: connect: connection refused`

// An investigation that can't start, or stops early, leaves a fixed note
// on its report. The error goes to the log, never into the note, and so
// never into the issue the report becomes.
func TestFailedInvestigationsKeepTheirErrorsOutOfTheIssue(t *testing.T) {
	issueBody := func(r *rig, id string) string {
		t.Helper()
		svc := agentreports.NewService(agentreports.Deps{Store: r.rstore, Binder: r.w.Binder, Safety: r.w.Safety, Now: r.clock})
		text, err := svc.Text(context.Background(), r.lookup(id))
		if err != nil {
			t.Fatal(err)
		}
		return text.IssueBody
	}

	// The run can't start.
	r := newRig(t)
	r.run()
	r.runs.err = errors.New(pgError)
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep := r.only()
	if rep.Investigation != agentreports.InvestigationFailed || rep.InvestigationNote != noteNotStarted {
		t.Fatalf("investigation %s %q", rep.Investigation, rep.InvestigationNote)
	}
	if body := issueBody(r, rep.ID); strings.Contains(body, "db.internal") || !strings.Contains(body, noteNotStarted) {
		t.Fatalf("issue body %q", body)
	}
	if got := r.warnings("db.internal"); len(got) != 1 {
		t.Fatalf("warnings %v, want the error logged once", got)
	}

	// The run starts and then fails.
	r = newRig(t)
	r.run()
	r.fail("dep_1", "service:api", t0.Add(5*time.Minute))
	r.run()
	rep = r.only()
	r.runs.set("arun_1", agentruns.StatusFailed, pgError)
	r.advance(time.Minute)
	r.run()
	rep = r.lookup(rep.ID)
	if rep.Investigation != agentreports.InvestigationFailed || rep.InvestigationNote != noteStopped {
		t.Fatalf("investigation %s %q", rep.Investigation, rep.InvestigationNote)
	}
	if body := issueBody(r, rep.ID); strings.Contains(body, "db.internal") || strings.Contains(body, "5432") || !strings.Contains(body, noteStopped) {
		t.Fatalf("issue body %q", body)
	}
	if got := r.warnings("db.internal"); len(got) != 1 {
		t.Fatalf("warnings %v, want the error logged once", got)
	}
}
