package agentrequests

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/suspension"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

const (
	orgA = "org_a"
	orgB = "org_b"

	ada = "usr_ada" // developer, the chat's asker
	bo  = "usr_bo"  // developer
	cy  = "usr_cy"  // admin
	dee = "usr_dee" // viewer, in Payments
	eve = "usr_eve" // developer, in Payments
	bea = "usr_bea" // org B's admin

	projA = "proj_a"
	projB = "proj_b"
)

func person(org, user string) security.Principal { return security.Principal{OrgID: org, UserID: user} }

// ---- the store ----

type memStore struct {
	mu   sync.Mutex
	rows map[string]*Request
	// failInsert fails the next Insert.
	failInsert error
}

func newMemStore() *memStore { return &memStore{rows: map[string]*Request{}} }

type memTx struct {
	s      *memStore
	staged []*Request
	done   bool
}

func (t *memTx) Query(context.Context, string, ...any) (driver.Rows, error) {
	return nil, errors.New("memTx: no queries")
}
func (t *memTx) Exec(context.Context, string, ...any) (driver.Result, error) {
	return nil, errors.New("memTx: no statements")
}
func (t *memTx) Commit() error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if t.done {
		return errors.New("memTx: done")
	}
	t.done = true
	for _, r := range t.staged {
		t.s.rows[r.ID] = clone(r)
	}
	return nil
}
func (t *memTx) Rollback() error { t.done = true; return nil }

func clone(r *Request) *Request {
	c := *r
	c.Context = slices.Clone(r.Context)
	c.Options = slices.Clone(r.Options)
	if r.AnsweredAt != nil {
		at := *r.AnsweredAt
		c.AnsweredAt = &at
	}
	return &c
}

func (s *memStore) Begin(context.Context) (Tx, error) { return &memTx{s: s}, nil }

func (s *memStore) Insert(_ context.Context, tx Tx, r *Request) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failInsert != nil {
		err := s.failInsert
		s.failInsert = nil
		return false, err
	}
	t := tx.(*memTx)
	for _, row := range s.rows {
		if row.RunID == r.RunID && row.ToolCallID == r.ToolCallID {
			return false, nil
		}
	}
	for _, row := range t.staged {
		if row.RunID == r.RunID && row.ToolCallID == r.ToolCallID {
			return false, nil
		}
	}
	t.staged = append(t.staged, clone(r))
	return true, nil
}

func (s *memStore) Get(_ context.Context, orgID, reqID string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[reqID]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	return clone(r), nil
}

func (s *memStore) all(orgID string) []*Request {
	out := []*Request{}
	for _, r := range s.rows {
		if r.OrgID == orgID {
			out = append(out, clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func (s *memStore) List(_ context.Context, orgID string, f StoreFilter) ([]*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Request{}
	for _, r := range s.all(orgID) {
		switch {
		case f.Status != "" && r.Status != f.Status,
			f.ProjectID != "" && r.ProjectID != f.ProjectID,
			f.TaskID != "" && r.TaskID != f.TaskID:
			continue
		}
		if f.Mine != "" {
			mine := r.AskedFor == f.Mine || (r.TeamID != "" && membersOf[r.TeamID][f.Mine]) ||
				(f.Admin && r.RouteSource == "admins")
			if !mine {
				continue
			}
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

func (s *memStore) Pause(_ context.Context, orgID, runID, suspensionID string) ([]*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Request{}
	for _, r := range s.all(orgID) {
		if r.RunID == runID && r.SuspensionID == suspensionID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *memStore) Settle(_ context.Context, orgID, reqID string, st Settlement) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[reqID]
	if !ok || r.OrgID != orgID || r.Status != StatusPending {
		return false, nil
	}
	at := st.At
	r.Status, r.Outcome, r.AnsweredBy, r.Answer, r.AnsweredAt, r.UpdatedAt = st.Status, st.Outcome, st.AnsweredBy, st.Answer, &at, at
	return true, nil
}

func (s *memStore) Reopen(_ context.Context, orgID, reqID string, st Settlement) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[reqID]
	if !ok || r.OrgID != orgID || r.Status != st.Status || r.Outcome != st.Outcome || r.AnsweredBy != st.AnsweredBy {
		return false, nil
	}
	r.Status, r.Outcome, r.AnsweredBy, r.Answer, r.AnsweredAt = StatusPending, "", "", "", nil
	return true, nil
}

func (s *memStore) CancelTask(_ context.Context, orgID, taskID string, at time.Time) ([]*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Request{}
	for _, r := range s.rows {
		if r.OrgID == orgID && r.TaskID == taskID && r.Status == StatusPending {
			when := at
			r.Status, r.Outcome, r.AnsweredAt, r.UpdatedAt = StatusCancelled, OutcomeCancelled, &when, at
			out = append(out, clone(r))
		}
	}
	return out, nil
}

func (s *memStore) Due(_ context.Context, now time.Time, limit int) ([]Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Ref{}
	for _, r := range s.rows {
		if r.Status != StatusPending {
			continue
		}
		if r.Kind == KindApprovePlan || r.Kind == KindConnectGitHub || r.Kind == KindCreateConnection || r.ExpiresAt.Before(now) {
			out = append(out, Ref{OrgID: r.OrgID, ID: r.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memStore) only(t *testing.T) *Request {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) != 1 {
		t.Fatalf("%d requests, want 1", len(s.rows))
	}
	for _, r := range s.rows {
		return clone(r)
	}
	return nil
}

func (s *memStore) byCall(t *testing.T, callID string) *Request {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ToolCallID == callID {
			return clone(r)
		}
	}
	t.Fatalf("no request for %s", callID)
	return nil
}

func (s *memStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// ---- cortex ----

type decision struct {
	checkpoint string
	d          checkpoint.Decision
	principal  any
	bound      bool
}

type provided struct {
	runID     string
	in        engine.ResumeInput
	principal any
	bound     bool
}

type fakeResumer struct {
	mu        sync.Mutex
	decisions []decision
	results   []provided
	err       error
	decideErr error
}

func (f *fakeResumer) DecideCheckpoint(ctx context.Context, cp id.CheckpointID, d checkpoint.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.decideErr != nil {
		return f.decideErr
	}
	for _, prior := range f.decisions {
		if prior.checkpoint == cp.String() {
			return cortex.ErrInvalidState
		}
	}
	f.decisions = append(f.decisions, decision{checkpoint: cp.String(), d: d, principal: cortex.PrincipalFromContext(ctx), bound: isBound(ctx)})
	return nil
}

func (f *fakeResumer) ProvideResults(ctx context.Context, runID id.AgentRunID, in engine.ResumeInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.results = append(f.results, provided{runID: runID.String(), in: in, principal: cortex.PrincipalFromContext(ctx), bound: isBound(ctx)})
	return nil
}

func (f *fakeResumer) provided(t *testing.T) []provided {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.results)
}

func (f *fakeResumer) decided(t *testing.T) []decision {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.decisions)
}

// ---- runs ----

type boundKey struct{}

func isBound(ctx context.Context) bool { v, _ := ctx.Value(boundKey{}).(bool); return v }

type fakeRuns struct {
	mu      sync.Mutex
	tasks   map[string]*agentruns.Task // by run ID
	bindErr error
	events  []string
}

func (f *fakeRuns) ByRun(_ context.Context, orgID, runID string) (*agentruns.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[runID]
	if !ok || t.OrgID != orgID {
		return nil, agentruns.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (f *fakeRuns) Bind(ctx context.Context, t *agentruns.Task) (context.Context, error) {
	if f.bindErr != nil {
		return ctx, f.bindErr
	}
	var on *security.Principal
	if t.RequestedBy != "" {
		p := person(t.OrgID, t.RequestedBy)
		on = &p
	}
	ctx = cortex.WithPrincipal(ctx, security.AgentPrincipal(t.OrgID, on))
	return context.WithValue(ctx, boundKey{}, true), nil
}

func (f *fakeRuns) RecordRequest(_ context.Context, t *agentruns.Task, requestID, kind string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, t.ID+":"+requestID+":"+kind)
	return nil
}

// ---- plans ----

type rejection struct {
	by, plan, note string
}

type fakePlans struct {
	mu       sync.Mutex
	plans    map[string]*planning.Plan
	rejected []rejection
}

func (f *fakePlans) Get(_ context.Context, p security.Principal, planID string) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pl, ok := f.plans[planID]
	if !ok || pl.OrgID != p.OrgID {
		return nil, planning.ErrNotFound
	}
	c := *pl
	return &c, nil
}

func (f *fakePlans) Reject(_ context.Context, p security.Principal, planID string, in planning.DecideInput) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pl, ok := f.plans[planID]
	if !ok || pl.OrgID != p.OrgID {
		return nil, planning.ErrNotFound
	}
	if !grants[p.OrgID+"/"+p.UserID][security.DeploymentApprove] {
		return nil, security.ErrForbidden
	}
	if pl.Status != planning.StatusReady {
		return nil, planning.ErrNotReady
	}
	pl.Status, pl.DecidedBy = planning.StatusRejected, p.UserID
	f.rejected = append(f.rejected, rejection{by: p.UserID, plan: planID, note: in.Note})
	c := *pl
	return &c, nil
}

func (f *fakePlans) set(status, by string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans["plan_1"].Status, f.plans["plan_1"].DecidedBy = status, by
}

// ---- projects ----

type fakeProjects struct{}

var allProjects = []*projects.Project{
	{ID: projA, OrgID: orgA, Slug: "checkout-api", Name: "Checkout API"},
	{ID: "proj_c", OrgID: orgA, Slug: "billing", Name: "Billing"},
	{ID: projB, OrgID: orgB, Slug: "other", Name: "Other"},
}

func (fakeProjects) Get(_ context.Context, p security.Principal, projectID string) (*projects.Project, error) {
	for _, pr := range allProjects {
		if pr.ID == projectID && pr.OrgID == p.OrgID {
			c := *pr
			return &c, nil
		}
	}
	return nil, projects.ErrNotFound
}

func (fakeProjects) List(_ context.Context, p security.Principal) ([]*projects.Project, error) {
	out := []*projects.Project{}
	for _, pr := range allProjects {
		if pr.OrgID == p.OrgID {
			c := *pr
			out = append(out, &c)
		}
	}
	return out, nil
}

func (fakeProjects) Environments(_ context.Context, p security.Principal, projectID string) ([]*projects.Environment, error) {
	return []*projects.Environment{
		{ID: "env_stg", OrgID: p.OrgID, ProjectID: projectID, Name: "staging"},
		{ID: "env_prod", OrgID: p.OrgID, ProjectID: projectID, Name: "production"},
	}, nil
}

// ---- variables ----

type varCall struct {
	by      security.Principal
	project string
	create  *environments.CreateVariableInput
	update  *environments.UpdateVariableInput
	id      string
}

type fakeVariables struct {
	mu       sync.Mutex
	existing []*environments.Variable
	calls    []varCall
	err      error
}

func (f *fakeVariables) ListProject(_ context.Context, p security.Principal, projectID string) ([]*environments.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*environments.Variable{}
	for _, v := range f.existing {
		if v.OrgID == p.OrgID && v.ProjectID == projectID {
			c := *v
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeVariables) CreateProject(_ context.Context, p security.Principal, projectID string, in environments.CreateVariableInput) (*environments.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, varCall{by: p, project: projectID, create: &in})
	return &environments.Variable{ID: "var_new", OrgID: p.OrgID, ProjectID: projectID, Key: in.Key, Secret: in.Secret, Environment: in.Environment}, nil
}

func (f *fakeVariables) Update(_ context.Context, p security.Principal, varID string, in environments.UpdateVariableInput) (*environments.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, varCall{by: p, id: varID, update: &in})
	return &environments.Variable{ID: varID, OrgID: p.OrgID, Secret: true}, nil
}

// ---- GitHub and clouds ----

type fakeGitHub struct {
	mu   sync.Mutex
	inst []repositories.GitHubInstallation
}

func (f *fakeGitHub) Status(_ context.Context, p security.Principal) (*repositories.GitHubStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &repositories.GitHubStatus{Configured: true, Installations: []repositories.GitHubInstallation{}}
	for _, i := range f.inst {
		if strings.HasPrefix(i.ID, p.OrgID+":") {
			out.Installations = append(out.Installations, i)
		}
	}
	return out, nil
}

func (f *fakeGitHub) add(org, account string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inst = append(f.inst, repositories.GitHubInstallation{ID: org + ":" + account, Account: account, CreatedAt: at})
}

type fakeConnections struct {
	mu    sync.Mutex
	conns []*connections.Connection
}

func (f *fakeConnections) List(_ context.Context, p security.Principal) ([]*connections.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*connections.Connection{}
	for _, c := range f.conns {
		if c.OrgID == p.OrgID {
			cc := *c
			out = append(out, &cc)
		}
	}
	return out, nil
}

// ---- authorization ----

var (
	workspaceAdmin = []security.Permission{security.AgentUse, security.AIManage, security.ProjectRead, security.VariableManage,
		security.RepositoryConnect, security.CloudAccountManage, security.DeploymentApprove}
	developer = []security.Permission{security.AgentUse, security.ProjectRead, security.VariableManage}
	viewer    = []security.Permission{security.AgentUse, security.ProjectRead}
)

func set(perms ...security.Permission) map[security.Permission]bool {
	m := map[security.Permission]bool{}
	for _, p := range perms {
		m[p] = true
	}
	return m
}

var grants = map[string]map[security.Permission]bool{
	orgA + "/" + ada: set(developer...),
	orgA + "/" + bo:  set(developer...),
	orgA + "/" + cy:  set(workspaceAdmin...),
	orgA + "/" + dee: set(viewer...),
	orgA + "/" + eve: set(developer...),
	orgB + "/" + bea: set(workspaceAdmin...),
}

type fakeAuthz struct{}

func (fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	if p.IsAgent() {
		return nil
	}
	if grants[p.OrgID+"/"+p.UserID][perm] {
		return nil
	}
	if _, member := grants[p.OrgID+"/"+p.UserID]; !member {
		return security.ErrNotMember
	}
	return security.ErrForbidden
}

// ---- safety ----

type fakeSafety struct {
	mu       sync.Mutex
	unbound  []string
	outbound []string
	scanned  []string
}

func (f *fakeSafety) Outbound(ctx context.Context, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !isBound(ctx) {
		f.unbound = append(f.unbound, "outbound: "+text)
		return "", agentsafety.ErrOutboundBlocked
	}
	f.outbound = append(f.outbound, text)
	if strings.Contains(text, "BLOCK") {
		return "", agentsafety.ErrOutboundBlocked
	}
	return strings.ReplaceAll(text, "sk-live-123", "[redacted]"), nil
}

func (f *fakeSafety) Untrusted(ctx context.Context, source, text string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !isBound(ctx) {
		f.unbound = append(f.unbound, "untrusted: "+text)
		return agentsafety.Withheld
	}
	f.scanned = append(f.scanned, source+": "+text)
	return text
}

// ---- teams ----

var membersOf = map[string]map[string]bool{
	"team_pay":   {dee: true, eve: true},
	"team_ops":   {bo: true},
	"team_empty": {},
}

var teamNames = map[string]string{"team_pay": "Payments", "team_ops": "Ops", "team_empty": "Empty"}

type fakeTeams struct {
	mu sync.Mutex
	// responsible is the project's responsible team.
	responsible map[string]string
}

func (f *fakeTeams) members(teamID string) []string {
	out := make([]string, 0, len(membersOf[teamID]))
	for u := range membersOf[teamID] {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func (f *fakeTeams) Resolve(_ context.Context, _ notifications.Querier, orgID, projectID, named, reason string) (teams.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var notes []string
	if named != "" && orgID == orgA {
		teamID := ""
		for tid, name := range teamNames {
			if strings.EqualFold(name, named) || tid == named {
				teamID = tid
			}
		}
		switch {
		case teamID == "":
			notes = append(notes, "No team named \""+named+"\" in this workspace")
		case len(membersOf[teamID]) == 0:
			notes = append(notes, teamNames[teamID]+" has no members")
		default:
			return teams.Route{TeamID: teamID, Team: teamNames[teamID], Source: "named", Reason: reason, Members: f.members(teamID)}, nil
		}
	}
	if tid := f.responsible[projectID]; tid != "" && orgID == orgA {
		if len(membersOf[tid]) == 0 {
			notes = append(notes, teamNames[tid]+" has no members")
		} else {
			return teams.Route{TeamID: tid, Team: teamNames[tid], Source: "project", Note: strings.Join(notes, "; "), Members: f.members(tid)}, nil
		}
	}
	admins := []string{cy}
	if orgID == orgB {
		admins = []string{bea}
	}
	return teams.Route{Team: teams.AdminsName, Source: "admins", Note: strings.Join(notes, "; "), Members: admins}, nil
}

func (f *fakeTeams) IsMember(_ context.Context, _ notifications.Querier, orgID, teamID, userID string) (bool, error) {
	return orgID == orgA && membersOf[teamID][userID], nil
}

// ---- people and audit ----

type fakePeople struct{}

var names = map[string]string{ada: "Ada", bo: "Bo", cy: "Cy", dee: "Dee", eve: "Eve", bea: "Bea"}

func (fakePeople) Name(_ context.Context, userID string) string {
	if n, ok := names[userID]; ok {
		return n
	}
	return "someone"
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

// ---- the rig ----

type rig struct {
	svc     *Service
	store   *memStore
	resumer *fakeResumer
	runs    *fakeRuns
	plans   *fakePlans
	vars    *fakeVariables
	github  *fakeGitHub
	clouds  *fakeConnections
	safety  *fakeSafety
	teams   *fakeTeams
	audit   *fakeAudit
	now     time.Time

	mu      sync.Mutex
	notices []notifications.Notice
	// failEnqueue fails every Enqueue.
	failEnqueue error

	chat, background *agentruns.Task
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		store: newMemStore(), resumer: &fakeResumer{}, safety: &fakeSafety{}, audit: &fakeAudit{},
		runs:  &fakeRuns{tasks: map[string]*agentruns.Task{}},
		plans: &fakePlans{plans: map[string]*planning.Plan{}},
		vars:  &fakeVariables{}, github: &fakeGitHub{}, clouds: &fakeConnections{},
		teams: &fakeTeams{responsible: map[string]string{}},
		now:   time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
	r.chat = r.task("atask_chat", orgA, projA, ada)
	r.background = r.task("atask_bg", orgA, projA, "")
	r.plans.plans["plan_1"] = &planning.Plan{ID: "plan_1", OrgID: orgA, ProjectID: projA, EnvironmentID: "env_prod", Environment: "production", Status: planning.StatusReady, Revision: "rev1"}
	r.svc = NewService(Deps{
		Store: r.store, Engine: func() Resumer { return r.resumer }, Runs: r.runs, Plans: r.plans, Projects: fakeProjects{},
		Variables: r.vars, GitHub: r.github, Connections: r.clouds, Authz: fakeAuthz{}, Safety: r.safety, Teams: r.teams,
		People: fakePeople{}, Audit: r.audit, Now: func() time.Time { return r.clock() },
		Enqueue: func(_ context.Context, _ notifications.Execer, n notifications.Notice) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.failEnqueue != nil {
				return r.failEnqueue
			}
			r.notices = append(r.notices, n)
			return nil
		},
		Warn: func(msg string, err error) { t.Logf("warn: %s: %v", msg, err) },
	})
	t.Cleanup(func() {
		r.safety.mu.Lock()
		defer r.safety.mu.Unlock()
		if len(r.safety.unbound) > 0 {
			t.Errorf("text was scanned before the scope was bound: %q", r.safety.unbound)
		}
	})
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

func (r *rig) task(taskID, org, project, requestedBy string) *agentruns.Task {
	t := &agentruns.Task{ID: taskID, OrgID: org, ProjectID: project, Kind: agentruns.KindChat, RunID: id.NewAgentRunID().String(),
		Status: agentruns.StatusWaiting, RequestedBy: requestedBy}
	if requestedBy == "" {
		t.Kind = agentruns.KindInvestigation
	}
	r.runs.mu.Lock()
	r.runs.tasks[t.RunID] = t
	r.runs.mu.Unlock()
	return t
}

func (r *rig) sent() []notifications.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notices)
}

func args(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// external is an external-tool pause of one or more calls.
func external(calls ...suspension.PendingCall) suspension.Notice {
	expires := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	return suspension.Notice{SuspensionID: id.NewSuspensionID(), Reason: suspension.ReasonExternalTool, Pending: calls, ExpiresAt: &expires}
}

// approval is a checkpoint pause on approve_plan.
func approval(t *testing.T, planID, revision string) suspension.Notice {
	expires := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	return suspension.Notice{
		SuspensionID: id.NewSuspensionID(), Reason: suspension.ReasonApproval, CheckpointID: id.NewCheckpointID(), ExpiresAt: &expires,
		Pending: []suspension.PendingCall{{ID: "call_approve", Name: "approve_plan",
			Arguments: args(t, map[string]string{"plan_id": planID, "revision": revision}),
			Note:      "Needs a person: autonomous promotion is off for this project"}},
	}
}

func ask(t *testing.T, callID string, in map[string]any) suspension.PendingCall {
	return suspension.PendingCall{ID: callID, Name: "ask_person", Arguments: args(t, in)}
}

// suspend hands the notice to the service for task and returns what it
// made.
func (r *rig) suspend(t *testing.T, task *agentruns.Task, n suspension.Notice) {
	t.Helper()
	if err := r.svc.OnSuspended(context.Background(), task, n); err != nil {
		t.Fatal(err)
	}
}

// askAda puts a question in Ada's chat and returns its request.
func (r *rig) askAda(t *testing.T, in map[string]any) *Request {
	t.Helper()
	r.suspend(t, r.chat, external(ask(t, "call_ask", in)))
	return r.store.byCall(t, "call_ask")
}

var _ = agentauthz.Decider{}
