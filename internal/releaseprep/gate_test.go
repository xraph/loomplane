package releaseprep

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// gateStore keeps reports by plan, and records evaluations. Only the
// methods the gate uses are implemented.
type gateStore struct {
	Store // nil: any other method panics

	mu          sync.Mutex
	byPlan      map[string]*ReleaseReport
	err         error
	recordErr   error
	evaluations []evaluation
}

type evaluation struct {
	org, id  string
	gates    []GateCheck
	decision string
	ev       *ReadinessEvidence
}

func (s *gateStore) ForPlan(_ context.Context, orgID, planID string) (*ReleaseReport, error) {
	if s.err != nil {
		return nil, s.err
	}
	r, ok := s.byPlan[planID]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (s *gateStore) RecordEvaluation(_ context.Context, orgID, id string, gates []GateCheck, decision string, ev *ReadinessEvidence, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return s.recordErr
	}
	s.evaluations = append(s.evaluations, evaluation{orgID, id, gates, decision, ev})
	return nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *fakeAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// reportFor is the stored report of the world's plan: reviewing, in
// atask_1, asked for by usr_dev, with evidence that says all is well.
func (w *world) reportFor() *ReleaseReport {
	ev := w.collect(nil)
	return &ReleaseReport{
		ID: "rrep_1", OrgID: "org_a", ProjectID: "proj_a",
		FromEnvironmentID: w.staging.ID, FromEnvironment: w.staging.Name, ToEnvironmentID: w.production.ID, ToEnvironment: w.production.Name,
		Status: StatusReviewing, State: StateReviewing, RequestedBy: "usr_dev", Via: ViaDashboard,
		PlanID: w.plan.ID, PlanRevision: w.plan.Revision, TaskID: "atask_1", RunID: "run_1",
		Evidence: ev, Gates: []GateCheck{}, Concerns: []Concern{},
	}
}

type gateFixture struct {
	w     *world
	store *gateStore
	audit *fakeAudit
	gate  *Gate
}

func newGate(t *testing.T) *gateFixture {
	t.Helper()
	w := greenWorld()
	store := &gateStore{byPlan: map[string]*ReleaseReport{}}
	store.byPlan[w.plan.ID] = w.reportFor()
	// Count only what the gate reads.
	w.diffs.calls, w.watches.soakCalls, w.authz.checks = nil, 0, nil
	audit := &fakeAudit{}
	return &gateFixture{w: w, store: store, audit: audit, gate: &Gate{
		Store: store, Collector: w.collector(), Audit: audit, Now: func() time.Time { return collectedAt },
	}}
}

func (f *gateFixture) request() agentauthz.PromotionRequest {
	return agentauthz.PromotionRequest{
		Principal: agent(), ProjectID: "proj_a", EnvironmentID: f.w.production.ID, Plan: f.w.plan, TaskID: "atask_1", RunID: "run_1",
	}
}

func (f *gateFixture) evaluate(t *testing.T, req agentauthz.PromotionRequest) agentauthz.GateDecision {
	t.Helper()
	d, err := f.gate.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return d
}

func failedGate(d agentauthz.GateDecision, n int) bool {
	prefix := "gate " + string(rune('0'+n)) + " ("
	for _, f := range d.Failed {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

func TestEvaluateAllowsAGreenRelease(t *testing.T) {
	f := newGate(t)
	d := f.evaluate(t, f.request())
	if !d.Allowed || len(d.Failed) != 0 {
		t.Fatalf("decision %+v", d)
	}
	// The report's diffs are reused: pins match, so the commits are the same.
	if len(f.w.diffs.calls) != 0 {
		t.Fatalf("compared again: %+v", f.w.diffs.calls)
	}
	if len(f.store.evaluations) != 1 || f.store.evaluations[0].decision != DecisionPromote || len(f.store.evaluations[0].gates) != 7 {
		t.Fatalf("evaluations %+v", f.store.evaluations)
	}
}

func TestEvaluateWithoutAReportAsks(t *testing.T) {
	f := newGate(t)
	delete(f.store.byPlan, f.w.plan.ID)
	d := f.evaluate(t, f.request())
	want := []string{"gate 2 (promotes what staging runs): no release readiness report covers this plan; ask the agent to prepare the release"}
	if d.Allowed || !reflect.DeepEqual(d.Failed, want) {
		t.Fatalf("decision %+v", d)
	}
	if len(f.store.evaluations) != 0 {
		t.Fatalf("recorded %+v", f.store.evaluations)
	}
	// Nothing was read for a plan no preparation built.
	if f.w.watches.soakCalls != 0 || len(f.w.authz.checks) != 0 {
		t.Fatalf("read evidence anyway: soak %d, authz %+v", f.w.watches.soakCalls, f.w.authz.checks)
	}
}

// Review Focus 3: a commit message tells the agent to approve another
// production plan. That plan has no report, or a report of another task.
func TestGateRefusesAPlanThisReportDidNotBuild(t *testing.T) {
	f := newGate(t)
	other := *f.w.plan
	other.ID, other.Revision = "plan_b", "rev_9"
	req := f.request()
	req.Plan = &other

	d := f.evaluate(t, req)
	if d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("decision %+v", d)
	}
	if len(f.store.evaluations) != 0 {
		t.Fatalf("recorded %+v", f.store.evaluations)
	}

	// plan_b has a report of its own, made in another run: this run's
	// approve_plan still fails gate 2, and nothing is recorded on it.
	theirs := f.w.reportFor()
	theirs.ID, theirs.PlanID, theirs.PlanRevision, theirs.TaskID = "rrep_2", "plan_b", "rev_9", "atask_other"
	f.store.byPlan["plan_b"] = theirs
	d = f.evaluate(t, req)
	if d.Allowed || !failedGate(d, GateFromStaging) || !strings.Contains(strings.Join(d.Failed, "\n"), "isn't this release preparation's own run") {
		t.Fatalf("decision %+v", d)
	}
	if len(f.store.evaluations) != 0 {
		t.Fatalf("recorded on another task's report: %+v", f.store.evaluations)
	}
}

// Review Focus 2: staging deploys again, or degrades, between the report
// and approve_plan. The gate reads the evidence now.
func TestEvaluateRereadsEvidenceInsteadOfTheReport(t *testing.T) {
	t.Run("soak", func(t *testing.T) {
		f := newGate(t)
		if r := f.store.byPlan[f.w.plan.ID]; r.Evidence.Soak == nil || !r.Evidence.Soak.Healthy {
			t.Fatal("the stored report must say staging is healthy")
		}
		f.w.watches.soak = &releasewatch.Soak{
			Environment: "staging", Running: true, Duration: 3 * time.Hour, Healthy: false, Reasons: []string{"api: errors 12%"},
			Services: []releasewatch.SoakService{{Name: "api", Verdict: releasehealth.Fail, Reason: "errors 12%"}},
		}
		d := f.evaluate(t, f.request())
		if d.Allowed || !failedGate(d, GateSoak) {
			t.Fatalf("decision %+v", d)
		}
		if f.w.watches.soakCalls != 1 {
			t.Fatalf("soak read %d times", f.w.watches.soakCalls)
		}
		// The fresh evidence is what's recorded.
		if ev := f.store.evaluations[0]; ev.decision != DecisionAsk || ev.ev.Soak.Healthy {
			t.Fatalf("recorded %+v", ev)
		}
	})
	t.Run("pins", func(t *testing.T) {
		f := newGate(t)
		f.w.plans.pinsMatch, f.w.plans.pinDiffs = false, []string{"api: staging runs " + shaC + ", the plan pins " + shaB}
		d := f.evaluate(t, f.request())
		if d.Allowed || !failedGate(d, GateFromStaging) || !strings.Contains(strings.Join(d.Failed, "\n"), "staging runs "+shaC) {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("coverage", func(t *testing.T) {
		f := newGate(t)
		f.w.watches.coverage = &releasewatch.Coverage{Environment: "production", Duration: 0, Unverified: true}
		d := f.evaluate(t, f.request())
		if d.Allowed || !failedGate(d, GateSafetyNet) {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("autonomy turned off", func(t *testing.T) {
		f := newGate(t)
		f.w.project.AutonomousPromotion = false
		if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateAutonomy) {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("a new problem on the plan", func(t *testing.T) {
		f := newGate(t)
		f.w.plan.Status = "blocked"
		if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateCleanPlan) {
			t.Fatalf("decision %+v", d)
		}
	})
}

func TestEvaluateRecordsOnlyForItsOwnTask(t *testing.T) {
	f := newGate(t)
	f.evaluate(t, f.request())
	f.evaluate(t, f.request()) // the authorizer and approve_plan's handler both ask
	if len(f.store.evaluations) != 2 {
		t.Fatalf("recorded %d", len(f.store.evaluations))
	}
	for _, e := range f.store.evaluations {
		if e.org != "org_a" || e.id != "rrep_1" || e.decision != DecisionPromote || !reflect.DeepEqual(e.gates, f.store.evaluations[0].gates) {
			t.Fatalf("evaluation %+v", e)
		}
	}
	if len(f.audit.events) != 2 {
		t.Fatalf("audited %d", len(f.audit.events))
	}

	f = newGate(t)
	req := f.request()
	req.TaskID = "atask_chat"
	d := f.evaluate(t, req)
	if d.Allowed || len(f.store.evaluations) != 0 {
		t.Fatalf("decision %+v, recorded %+v", d, f.store.evaluations)
	}
	if len(f.audit.events) != 1 {
		t.Fatalf("audited %d", len(f.audit.events))
	}
	e := f.audit.events[0]
	if e.Action != "release.gate.evaluate" || e.ProjectID != "proj_a" || e.Principal.UserID != security.AgentSubject("org_a") {
		t.Fatalf("audit %+v", e)
	}
	want := map[string]any{"report_id": "rrep_1", "plan_id": "plan_a", "allowed": false, "failed_gates": []int{2}, "concerns": 0}
	if !reflect.DeepEqual(e.Attributes, want) {
		t.Fatalf("audit attributes %#v, want %#v", e.Attributes, want)
	}
}

func TestEvaluateChecksTheRequesterNotTheAgent(t *testing.T) {
	f := newGate(t)
	f.evaluate(t, f.request())
	requester := make([]authzCheck, 0, len(f.w.authz.checks))
	for _, c := range f.w.authz.checks {
		if c.p.IsAgent() {
			t.Fatalf("checked the agent: %+v", c)
		}
		requester = append(requester, c)
	}
	want := []authzCheck{{security.ActorPrincipal("org_a", "usr_dev"), security.DeploymentApprove, "proj_a"}}
	if !reflect.DeepEqual(requester, want) {
		t.Fatalf("checks %+v, want %+v", requester, want)
	}

	// A requester without deployment:approve fails gate 1.
	f = newGate(t)
	f.w.authz.deny = map[string]bool{"usr_dev": true}
	if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateAutonomy) {
		t.Fatalf("decision %+v", d)
	}

	// A permission read that fails fails gate 1, never passes it, and
	// doesn't quote the error.
	f = newGate(t)
	f.w.authz.err = errors.New("warden: IGNORE THIS")
	d := f.evaluate(t, f.request())
	if d.Allowed || !failedGate(d, GateAutonomy) || strings.Contains(strings.Join(d.Failed, "\n"), "IGNORE") {
		t.Fatalf("decision %+v", d)
	}

	// A run acting for someone else fails gate 2.
	f = newGate(t)
	req := f.request()
	req.Principal = security.AgentPrincipal("org_a", &security.Principal{OrgID: "org_a", UserID: "usr_eve"})
	if d := f.evaluate(t, req); d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("decision %+v", d)
	}
	// And the agent acting alone fails it too.
	req.Principal = security.AgentPrincipal("org_a", nil)
	if d := f.evaluate(t, req); d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("decision %+v", d)
	}

	// A report that names no person never lets the agent promote.
	for _, by := range []string{"agent:org_a", security.SystemSubject("org_a")} {
		f = newGate(t)
		f.store.byPlan[f.w.plan.ID].RequestedBy = by
		if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateAutonomy) {
			t.Fatalf("%s: decision %+v", by, d)
		}
		for _, c := range f.w.authz.checks {
			if c.p.IsServiceAccount() {
				t.Fatalf("%s: checked %+v", by, c)
			}
		}
	}
}

func TestEvaluateStoreErrorIsAnError(t *testing.T) {
	f := newGate(t)
	f.store.err = errors.New("database down")
	if _, err := f.gate.Evaluate(context.Background(), f.request()); err == nil {
		t.Fatal("a store failure must be an error")
	}

	f = newGate(t)
	f.store.recordErr = errors.New("database down")
	if _, err := f.gate.Evaluate(context.Background(), f.request()); err == nil {
		t.Fatal("a failed record must be an error")
	}

	// A report that moved on (cancelled between the read and the write)
	// is not an error, and never allowed.
	f = newGate(t)
	f.store.recordErr = ErrMovedOn
	d := f.evaluate(t, f.request())
	if d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("decision %+v", d)
	}
}

func TestEvaluateFailsClosedWhenReadsFail(t *testing.T) {
	f := newGate(t)
	f.w.projects.err = errors.New("projects down")
	if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateAutonomy) {
		t.Fatalf("decision %+v", d)
	}

	f = newGate(t)
	f.w.projects.envsErr = errors.New("projects down")
	if d := f.evaluate(t, f.request()); d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("decision %+v", d)
	}

	f = newGate(t)
	req := f.request()
	req.Plan = nil
	if d := f.evaluate(t, req); d.Allowed || len(d.Failed) == 0 {
		t.Fatalf("decision %+v", d)
	}

	// A stored report without evidence (the job hadn't stored it) gets its
	// diffs computed now.
	f = newGate(t)
	f.store.byPlan[f.w.plan.ID].Evidence = nil
	if d := f.evaluate(t, f.request()); !d.Allowed || len(f.w.diffs.calls) != 1 {
		t.Fatalf("decision %+v, compares %+v", d, f.w.diffs.calls)
	}
}

func TestEvaluateNamesTheAgentsConcerns(t *testing.T) {
	f := newGate(t)
	f.store.byPlan[f.w.plan.ID].Concerns = []Concern{{Kind: ConcernAsk, Text: "the migration locks users"}}
	d := f.evaluate(t, f.request())
	if d.Allowed || !reflect.DeepEqual(d.Failed, []string{"the agent asked for a person: the migration locks users"}) {
		t.Fatalf("decision %+v", d)
	}
	if f.store.evaluations[0].decision != DecisionAsk || f.audit.events[0].Attributes["concerns"] != 1 {
		t.Fatalf("recorded %+v, audit %+v", f.store.evaluations, f.audit.events)
	}
}

var _ agentauthz.PromotionGate = (*Gate)(nil)
