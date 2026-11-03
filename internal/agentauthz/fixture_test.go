package agentauthz_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	"github.com/xraph/shield/awareness"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"
	"github.com/xraph/shield/profile"
	"github.com/xraph/shield/reflex"
	"github.com/xraph/shield/scan"
	shieldsqlite "github.com/xraph/shield/store/sqlite"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Workspaces: Engines (orgA), where Ada and Ben own and Vi views; Other (orgB),
// where Bob owns; Strict (orgC), whose shield asks a person for every
// change and blocks secrets; and Unseeded (orgD), whose shield has no
// profiles at all.
const (
	orgA = "o"
	orgB = "o2"
	orgC = "o3"
	orgD = "o4"
	ada  = "usr_ada"
	ben  = "usr_ben"
	vi   = "usr_vi"
	bob  = "usr_bob"
	cy   = "usr_cy"
	dee  = "usr_dee"
)

// window is the approval window the authorizer is configured with.
const window = 72 * time.Hour

// members is who belongs to which workspace, and as what. Tests that
// remove a member change it under membersMu.
type memberBook struct {
	mu   sync.Mutex
	orgs map[string]map[string]string
}

func newMembers() *memberBook {
	return &memberBook{orgs: map[string]map[string]string{
		orgA: {ada: security.MemberOwner, ben: security.MemberOwner, vi: security.MemberViewer},
		orgB: {bob: security.MemberOwner},
		orgC: {cy: security.MemberOwner},
		orgD: {dee: security.MemberOwner},
	}}
}

func (m *memberBook) lookup(_ context.Context, org, user string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.orgs[org][user]
	return role, ok, nil
}

func (m *memberBook) remove(org, user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.orgs[org], user)
}

// newAuthz is the real Authorizer over an in-memory Warden, with every
// workspace's agent turned on.
func newAuthz(t *testing.T, m *memberBook) *security.Authorizer {
	t.Helper()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	a := security.NewAuthorizer(eng, store, m.lookup)
	for _, org := range []string{orgA, orgB, orgC, orgD} {
		if err := a.AssignAgent(t.Context(), org); err != nil {
			t.Fatal(err)
		}
		// Membership hooks give each member their role, as they do when
		// someone joins.
		for user, role := range m.orgs[org] {
			if err := a.Assign(t.Context(), org, user, security.RoleForMember(role), "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a
}

// seededShield is a shield engine over an in-memory store with the
// InfraAgent preset seeded for orgA and orgB under agentscope.App, the
// way the agent's extension seeds a workspace, and orgC seeded with a
// tools profile that asks a person for every change and blocks any call
// carrying a secret. orgD has nothing.
func seededShield(t *testing.T) *shieldengine.Engine {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := shieldsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{orgA, orgB} {
		if _, err = presets.InfraAgent().Seed(ctx, st, org, agentscope.App); err != nil {
			t.Fatal(err)
		}
	}
	strict := presets.Bundle{
		Name: "ask-for-every-change",
		Reflexes: []*reflex.Reflex{{
			Name: "approve-change", Description: "Asks a person about every change.",
			Triggers: []reflex.Trigger{{Type: reflex.TriggerOnContext, Pattern: "risk_class=" + presets.RiskChange}},
			Actions:  []reflex.Action{{Type: reflex.ActionEscalate}},
			Enabled:  true,
		}},
		Awareness: []*awareness.Awareness{{
			Name: "secrets-block", Focus: awareness.FocusSecrets, Action: string(awareness.PIIActionBlock), Enabled: true,
		}},
		Profiles: []*profile.SafetyProfile{{
			Name: presets.InfraAgentTools, Reflexes: []string{"approve-change"},
			Awareness: []profile.AwarenessAssignment{{AwarenessName: "secrets-block"}}, Enabled: true,
		}},
	}
	if _, err = strict.Seed(ctx, st, orgC, agentscope.App); err != nil {
		t.Fatal(err)
	}
	eng, err := shieldengine.New(shieldengine.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// fakeScan quotes untrusted text, allows outbound text, and blocks
// outbound text that holds "BLOCK".
type fakeScan struct{}

func (fakeScan) ScanInput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: "<" + in.Source + ">" + in.Text + "</" + in.Source + ">"}, nil
}

func (fakeScan) ScanOutput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	if strings.Contains(in.Text, "BLOCK") {
		return &scan.Result{Decision: scan.DecisionBlock, Blocked: true}, nil
	}
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: in.Text}, nil
}

// fakeProjects is the projects service over maps, checking the principal
// through the real Authorizer as the service does.
type fakeProjects struct {
	agentops.ProjectService
	authz *security.Authorizer

	mu       sync.Mutex
	projects map[string]*projects.Project
	envs     map[string][]*projects.Environment
	gets     int
}

func (f *fakeProjects) add(org, id string) *projects.Project {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &projects.Project{ID: id, OrgID: org, Name: id, Slug: id}
	f.projects[id] = p
	f.envs[id] = []*projects.Environment{
		{ID: id + "_production", ProjectID: id, Name: "production", Kind: projects.KindProduction},
		{ID: id + "_staging", ProjectID: id, Name: "staging", Kind: projects.KindStaging},
	}
	return p
}

func (f *fakeProjects) setAutonomy(id string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[id].AutonomousPromotion = on
}

func (f *fakeProjects) Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error) {
	f.mu.Lock()
	f.gets++
	pr, ok := f.projects[id]
	f.mu.Unlock()
	if !ok || pr.OrgID != p.OrgID {
		return nil, projects.ErrNotFound
	}
	if err := f.authz.Require(ctx, p, security.ProjectRead, id); err != nil {
		return nil, err
	}
	c := *pr
	return &c, nil
}

func (f *fakeProjects) List(ctx context.Context, p security.Principal) ([]*projects.Project, error) {
	if err := f.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*projects.Project
	for _, pr := range f.projects {
		if pr.OrgID == p.OrgID {
			c := *pr
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeProjects) Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error) {
	if _, err := f.Get(ctx, p, projectID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.envs[projectID], nil
}

func (f *fakeProjects) Create(ctx context.Context, p security.Principal, _ projects.CreateProjectInput) (*projects.Project, error) {
	if err := f.authz.Require(ctx, p, security.ProjectCreate, ""); err != nil {
		return nil, err
	}
	return &projects.Project{ID: "prj_new", OrgID: p.OrgID}, nil
}

// approval is one Approve call.
type approval struct {
	principal security.Principal
	planID    string
	in        planning.DecideInput
}

// fakePlans is the planning service over a map, checking the principal
// through the real Authorizer as planning does.
type fakePlans struct {
	agentops.PlanService
	authz *security.Authorizer

	mu        sync.Mutex
	plans     map[string]*planning.Plan
	approvals []approval
}

func (f *fakePlans) Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error) {
	f.mu.Lock()
	pl, ok := f.plans[id]
	f.mu.Unlock()
	if !ok || pl.OrgID != p.OrgID {
		return nil, planning.ErrNotFound
	}
	if err := f.authz.Require(ctx, p, security.DeploymentRead, pl.ProjectID); err != nil {
		return nil, err
	}
	c := *pl
	return &c, nil
}

func (f *fakePlans) Approve(ctx context.Context, p security.Principal, id string, in planning.DecideInput) (*planning.Plan, error) {
	if err := f.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	pl, ok := f.plans[id]
	if !ok || pl.OrgID != p.OrgID {
		return nil, planning.ErrNotFound
	}
	f.approvals = append(f.approvals, approval{principal: p, planID: id, in: in})
	if in.Revision != pl.Revision {
		return nil, planning.ErrStale
	}
	pl.Status, pl.DecidedBy, pl.DecisionNote = planning.StatusApproved, p.UserID, in.Note
	c := *pl
	return &c, nil
}

func (f *fakePlans) approved() []approval {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]approval(nil), f.approvals...)
}

// fakeDeployments is the deployments service over a map.
type fakeDeployments struct {
	agentops.DeploymentService
	authz *security.Authorizer

	mu      sync.Mutex
	deps    map[string]*deployments.Deployment
	retried []string
}

func (f *fakeDeployments) Get(ctx context.Context, p security.Principal, id string) (*deployments.Deployment, error) {
	f.mu.Lock()
	d, ok := f.deps[id]
	f.mu.Unlock()
	if !ok || d.OrgID != p.OrgID {
		return nil, deployments.ErrNotFound
	}
	if err := f.authz.Require(ctx, p, security.DeploymentRead, d.ProjectID); err != nil {
		return nil, err
	}
	c := *d
	return &c, nil
}

func (f *fakeDeployments) Retry(ctx context.Context, p security.Principal, id string) (*deployments.Deployment, error) {
	d, err := f.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err = f.authz.Require(ctx, p, security.DeploymentApprove, d.ProjectID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.retried = append(f.retried, id)
	f.mu.Unlock()
	return d, nil
}

func (f *fakeDeployments) retries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.retried...)
}

// recordingAuditor keeps every event.
type recordingAuditor struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *recordingAuditor) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *recordingAuditor) toolCalls() []projects.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []projects.AuditEvent
	for _, e := range a.events {
		if e.Action == "agent.tool.call" {
			out = append(out, e)
		}
	}
	return out
}

// fakeGate answers every promotion with decision or err, and remembers
// what it was asked.
type fakeGate struct {
	mu       sync.Mutex
	decision agentauthz.GateDecision
	err      error
	reqs     []agentauthz.PromotionRequest
}

func (g *fakeGate) Evaluate(_ context.Context, req agentauthz.PromotionRequest) (agentauthz.GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = append(g.reqs, req)
	return g.decision, g.err
}

func (g *fakeGate) asked() []agentauthz.PromotionRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]agentauthz.PromotionRequest(nil), g.reqs...)
}

// world is the registry with every built-in over the fakes, and the
// authorizer over the same.
type world struct {
	t        *testing.T
	members  *memberBook
	authz    *security.Authorizer
	projects *fakeProjects
	plans    *fakePlans
	deps     *fakeDeployments
	audit    *recordingAuditor
	reg      *agentops.Registry
	d        *agentops.Deps
	a        *agentauthz.Authorizer
	shield   *shieldengine.Engine
}

func newWorld(t *testing.T) *world {
	t.Helper()
	m := newMembers()
	authz := newAuthz(t, m)
	w := &world{
		t: t, members: m, authz: authz,
		projects: &fakeProjects{authz: authz, projects: map[string]*projects.Project{}, envs: map[string][]*projects.Environment{}},
		plans:    &fakePlans{authz: authz, plans: map[string]*planning.Plan{}},
		deps:     &fakeDeployments{authz: authz, deps: map[string]*deployments.Deployment{}},
		audit:    &recordingAuditor{},
		reg:      agentops.NewRegistry(),
	}
	w.d = &agentops.Deps{
		Projects: w.projects, Plans: w.plans, Deployments: w.deps, Authz: authz,
		Safety: &agentsafety.Scanner{Engine: fakeScan{}}, Audit: w.audit,
	}
	agentops.Builtins(w.reg, w.d)
	w.a = &agentauthz.Authorizer{Registry: w.reg, Plans: w.plans, Projects: w.projects, Authz: authz, ApprovalWindow: window}
	w.d.Promote = w.a.Promote
	w.reg.BindAuthorizer(w.a)
	return w
}

// shielded returns the authorizer layered under a seeded shield.
func (w *world) shielded() cortex.ToolAuthorizer {
	w.t.Helper()
	if w.shield == nil {
		w.shield = seededShield(w.t)
	}
	return agentauthz.Layered(w.a, w.shield)
}

// planners is who creates plans in each workspace's tests: the person who
// asks for the release.
var planners = map[string]string{orgA: ada, orgB: bob, orgC: cy, orgD: dee}

// plan stores a ready plan of project in its environment env ("production"
// or "staging"), at revision r1, doing actions, created by the
// workspace's planner.
func (w *world) plan(id string, p *projects.Project, env string, actions ...planning.Action) *planning.Plan {
	w.t.Helper()
	pl := &planning.Plan{
		ID: id, OrgID: p.OrgID, ProjectID: p.ID, EnvironmentID: p.ID + "_" + env, Environment: env,
		Status: planning.StatusReady, Revision: "r1", Actions: actions, Executable: true, CreatedBy: planners[p.OrgID],
	}
	w.plans.mu.Lock()
	w.plans.plans[id] = pl
	w.plans.mu.Unlock()
	return pl
}

// deployment stores a deployment of project in env.
func (w *world) deployment(id string, p *projects.Project, env string) {
	d := &deployments.Deployment{ID: id, OrgID: p.OrgID, ProjectID: p.ID, EnvironmentID: p.ID + "_" + env, Environment: env, Status: deployments.StatusFailed}
	w.deps.mu.Lock()
	w.deps.deps[id] = d
	w.deps.mu.Unlock()
}

// as is the agent of org acting for user, or alone when user is "".
func as(org, user string) security.Principal {
	if user == "" {
		return security.AgentPrincipal(org, nil)
	}
	return security.AgentPrincipal(org, &security.Principal{UserID: user, OrgID: org})
}

// subject is a run of p at org's scope.
func subject(p security.Principal, org string) cortex.Subject {
	return cortex.Subject{Scope: agentscope.Scope{OrgID: org}.Cortex(), Principal: p, RunID: cortexid.NewAgentRunID(), AgentID: cortexid.NewAgentID()}
}

// toolCall is a call of name with args, encoded as cortex hands it over.
func toolCall(t *testing.T, name string, args any) llm.ToolCall {
	t.Helper()
	raw := []byte("{}")
	if args != nil {
		var err error
		if raw, err = json.Marshal(args); err != nil {
			t.Fatal(err)
		}
	}
	return llm.ToolCall{ID: "call_" + name, Name: name, Arguments: string(raw)}
}

// authorize asks auth about p calling name with args in p's own workspace.
func (w *world) authorize(auth cortex.ToolAuthorizer, p security.Principal, name string, args any) error {
	w.t.Helper()
	return auth.Authorize(w.t.Context(), subject(p, p.OrgID), toolCall(w.t, name, args))
}

// dispatch is what cortex does with a new call: the registry's
// authorizer (the table) decides, and the handler runs only if it allows.
func (w *world) dispatch(p security.Principal, scopeOrg, name string, args any) (string, error) {
	w.t.Helper()
	return w.reg.Dispatch(w.t.Context(), cortex.Invocation{Subject: subject(p, scopeOrg), Call: toolCall(w.t, name, args)}, false)
}

// invoke is a call cortex resumes after a person approved its checkpoint:
// the authorizer runs again and only its approval is excused, so the
// handler's own checks are what stand.
func (w *world) invoke(p security.Principal, name string, args any) (string, error) {
	w.t.Helper()
	return w.reg.Dispatch(w.t.Context(), cortex.Invocation{Subject: subject(p, p.OrgID), Call: toolCall(w.t, name, args)}, true)
}

// approvalOf returns the approval err asks for, failing the test when it
// asks for none.
func approvalOf(t *testing.T, err error) cortex.ApprovalRequired {
	t.Helper()
	var ar cortex.ApprovalRequired
	if !errors.As(err, &ar) {
		t.Fatalf("got %v, want cortex.ApprovalRequired", err)
	}
	return ar
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
