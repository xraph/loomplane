package releaseprep

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	dispatchengine "github.com/xraph/dispatch/engine"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func TestPrepareNeedsAPerson(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for name, p := range map[string]security.Principal{
		"the agent alone":     security.AgentPrincipal("org_a", nil),
		"the agent for a dev": security.AgentPrincipal("org_a", &dev),
		"automation":          security.SystemPrincipal("org_a"),
		"nobody":              {OrgID: "org_a"},
	} {
		if _, err := r.svc.Prepare(ctx, p, "proj_a", PrepareInput{}, ViaDashboard); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("%s: %v, want ErrForbidden", name, err)
		}
	}
	if n := r.store.count(); n != 0 {
		t.Fatalf("stored %d reports", n)
	}
	if jobs := r.pendingJobs(t); len(jobs) != 0 {
		t.Fatalf("queued %d jobs", len(jobs))
	}
}

func TestPrepareNeedsDeploymentCreate(t *testing.T) {
	r := newRig(t)
	r.w.authz.refuse = map[string]bool{"usr_vi " + security.DeploymentCreate.Name(): true}
	if _, err := r.svc.Prepare(context.Background(), vi, "proj_a", PrepareInput{}, ViaDashboard); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer: %v, want ErrForbidden", err)
	}
	if n := r.store.count(); n != 0 {
		t.Fatalf("stored %d reports", n)
	}
	// Every check names the project: none checks the whole workspace.
	for _, c := range r.w.authz.checks {
		if c.resource != "proj_a" {
			t.Fatalf("%s checked on %q, want the project", c.perm.Name(), c.resource)
		}
	}
	r.w.authz.refuse = map[string]bool{"usr_vi " + security.AgentUse.Name(): true}
	if _, err := r.svc.Prepare(context.Background(), vi, "proj_a", PrepareInput{}, ViaDashboard); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("no agent:use: %v, want ErrForbidden", err)
	}
}

func TestPrepareRefusesWhenTheAgentIsOff(t *testing.T) {
	r := newRig(t)
	r.w.budgets.off = true
	if _, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard); !errors.Is(err, agentsettings.ErrDisabled) {
		t.Fatalf("%v, want ErrDisabled", err)
	}
	if n := r.store.count(); n != 0 {
		t.Fatalf("stored %d reports", n)
	}
}

func TestPrepareRefusesAnUnknownVia(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, "cron"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, want ErrInvalid", err)
	}
}

func TestPrepareResolvesEnvironmentsByKind(t *testing.T) {
	ctx := context.Background()
	env := func(id, name, kind string) *projects.Environment {
		return &projects.Environment{ID: id, OrgID: "org_a", ProjectID: "proj_a", Name: name, Kind: kind}
	}
	stg := env("env_stg", "staging", projects.KindStaging)
	qa := env("env_qa", "qa", projects.KindStaging)
	prd := env("env_prd", "production", projects.KindProduction)
	prv := env("env_prv", "preview", projects.KindPreview)

	r := newRig(t)
	r.w.projects.envs = []*projects.Environment{prv, stg, prd}
	got, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if got.FromEnvironmentID != "env_stg" || got.FromEnvironment != "staging" || got.ToEnvironmentID != "env_prd" || got.ToEnvironment != "production" {
		t.Fatalf("resolved %s/%s -> %s/%s", got.FromEnvironmentID, got.FromEnvironment, got.ToEnvironmentID, got.ToEnvironment)
	}

	cases := []struct {
		name string
		envs []*projects.Environment
		in   PrepareInput
		want error
		says []string
	}{
		{"from a preview environment", []*projects.Environment{prv, stg, prd}, PrepareInput{From: "preview"}, ErrInvalid, []string{"preview", projects.KindPreview}},
		{"to a staging environment", []*projects.Environment{stg, qa, prd}, PrepareInput{From: "qa", To: "staging"}, ErrInvalid, []string{"staging"}},
		{"from an unknown environment", []*projects.Environment{stg, prd}, PrepareInput{From: "nowhere"}, ErrInvalid, []string{"nowhere"}},
		{"no staging", []*projects.Environment{prv, prd}, PrepareInput{}, ErrNoStaging, nil},
		{"no production", []*projects.Environment{stg, prv}, PrepareInput{}, ErrNoProduction, nil},
		{"two staging, no name", []*projects.Environment{stg, qa, prd}, PrepareInput{}, ErrAmbiguous, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cr := newRig(t)
			cr.w.projects.envs = c.envs
			_, cerr := cr.svc.Prepare(ctx, dev, "proj_a", c.in, ViaDashboard)
			if !errors.Is(cerr, c.want) {
				t.Fatalf("%v, want %v", cerr, c.want)
			}
			for _, s := range c.says {
				if !strings.Contains(cerr.Error(), s) {
					t.Fatalf("%q doesn't name %q", cerr, s)
				}
			}
			if n := cr.store.count(); n != 0 {
				t.Fatalf("stored %d reports", n)
			}
		})
	}

	// Two staging environments resolve by name.
	r = newRig(t)
	r.w.projects.envs = []*projects.Environment{stg, qa, prd}
	got, err = r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{From: "qa"}, ViaDashboard)
	if err != nil || got.FromEnvironmentID != "env_qa" {
		t.Fatalf("named qa: %+v %v", got, err)
	}
}

func TestPrepareNeverSupersedesAPersonsPlan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	open := r.openPlan(planning.KindRelease, "", "usr_ada")

	_, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{Scope: []string{"api"}}, ViaDashboard)
	if !errors.Is(err, ErrOpenPlan) {
		t.Fatalf("%v, want ErrOpenPlan", err)
	}
	if !strings.Contains(err.Error(), open.ID) || !strings.Contains(err.Error(), "usr_ada") {
		t.Fatalf("%q doesn't name the plan and who made it", err)
	}
	if r.w.plans.creates != 0 {
		t.Fatalf("Plans.Create called %d times", r.w.plans.creates)
	}
	if n := r.store.count(); n != 0 {
		t.Fatalf("stored %d reports", n)
	}
	if jobs := r.pendingJobs(t); len(jobs) != 0 {
		t.Fatalf("queued %d jobs", len(jobs))
	}
	if r.w.plans.open[0].Status != planning.StatusReady {
		t.Fatalf("the open plan is %s", r.w.plans.open[0].Status)
	}

	// The agent's own plan is named as the agent.
	r.openPlan(planning.KindRelease, "", security.AgentSubject("org_a"))
	_, err = r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{}, ViaDashboard)
	if !errors.Is(err, ErrOpenPlan) || !strings.Contains(err.Error(), security.AgentActorName) {
		t.Fatalf("%v, want ErrOpenPlan naming %s", err, security.AgentActorName)
	}

	// A planning plan is open too.
	r.openPlan(planning.KindRelease, "", "usr_ada").Status = planning.StatusPlanning
	r.w.plans.open[0].Status = planning.StatusPlanning
	if _, err = r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{}, ViaDashboard); !errors.Is(err, ErrOpenPlan) {
		t.Fatalf("planning plan: %v, want ErrOpenPlan", err)
	}

	// A person who says replace gets a report; the job makes the plan.
	r.openPlan(planning.KindRelease, "", "usr_ada")
	got, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{Replace: true}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanID != "" || got.ReusedPlan {
		t.Fatalf("replace reused plan %q", got.PlanID)
	}
	if jobs := r.pendingJobs(t); len(jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(jobs))
	}
	if r.w.plans.creates != 0 {
		t.Fatalf("Prepare made a plan itself")
	}
	if a := r.audit.named("release.prepare"); len(a) != 1 || a[0].Attributes["replace"] != true {
		t.Fatalf("audit %+v", a)
	}
}

func TestPrepareIgnoresAClosedPlan(t *testing.T) {
	r := newRig(t)
	r.openPlan(planning.KindRelease, "", "usr_ada").Status = planning.StatusApproved
	r.w.plans.open[0].Status = planning.StatusApproved
	if _, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard); err != nil {
		t.Fatalf("a decided plan isn't open: %v", err)
	}
}

func TestPrepareReusesAMatchingOpenPromotion(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	open := r.openPlan(planning.KindPromotion, "staging", "usr_ada")
	got, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{Scope: []string{"api"}}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanID != open.ID || !got.ReusedPlan {
		t.Fatalf("plan %q reused %v", got.PlanID, got.ReusedPlan)
	}
	if stored := r.store.stored(t, got.ID); stored.PlanID != open.ID || !stored.ReusedPlan {
		t.Fatalf("stored plan %q reused %v", stored.PlanID, stored.ReusedPlan)
	}
	if r.w.plans.creates != 0 {
		t.Fatalf("Plans.Create called %d times", r.w.plans.creates)
	}

	refused := []struct {
		name  string
		setup func(r *rig)
		in    PrepareInput
	}{
		{"pins moved", func(r *rig) { r.w.plans.pinsMatch = false }, PrepareInput{Scope: []string{"api"}}},
		{"another scope", func(*rig) {}, PrepareInput{Scope: []string{"api", "web"}}},
		{"every source, the plan holds web", func(*rig) {}, PrepareInput{}},
		{"from another environment", func(r *rig) { r.w.plans.open[0].FromEnvironmentID = "env_qa" }, PrepareInput{Scope: []string{"api"}}},
		{"not a promotion", func(r *rig) { r.w.plans.open[0].Kind = planning.KindRelease }, PrepareInput{Scope: []string{"api"}}},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			cr := newRig(t)
			cr.openPlan(planning.KindPromotion, "staging", "usr_ada")
			c.setup(cr)
			if _, cerr := cr.svc.Prepare(ctx, dev, "proj_a", c.in, ViaDashboard); !errors.Is(cerr, ErrOpenPlan) {
				t.Fatalf("%v, want ErrOpenPlan", cerr)
			}
		})
	}

	// Scope is compared as a set.
	r = newRig(t)
	r.openPlan(planning.KindPromotion, "staging", "usr_ada").Sources[1].InScope = true
	r.w.plans.open[0].Sources[1].InScope = true
	got, err = r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{Scope: []string{"web", " api ", "api"}}, ViaDashboard)
	if err != nil || !got.ReusedPlan || !slices.Equal(got.Scope, []string{"api", "web"}) {
		t.Fatalf("set scope: %+v %v", got, err)
	}
	// So is "every source" against a plan of every source.
	r = newRig(t)
	r.openPlan(planning.KindPromotion, "staging", "usr_ada")
	r.w.plans.open[0].Sources[1].InScope = true
	if got, err = r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{}, ViaDashboard); err != nil || !got.ReusedPlan {
		t.Fatalf("every source: %+v %v", got, err)
	}
}

func TestPrepareRefusesABadScope(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{Scope: []string{"api", " "}}, ViaDashboard); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, want ErrInvalid", err)
	}
}

func TestPrepareOneAtATime(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	busy := r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
	_, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{}, ViaDashboard)
	if !errors.Is(err, ErrInProgress) || !strings.Contains(err.Error(), busy.ID) {
		t.Fatalf("%v, want ErrInProgress naming %s", err, busy.ID)
	}
	if n := r.store.count(); n != 1 {
		t.Fatalf("stored %d reports", n)
	}

	// Its task completed without a decision: it's settled first, and the
	// new report is stored.
	r.runs.byRun["arun_1"].Status = agentruns.StatusCompleted
	got, err := r.svc.Prepare(ctx, dev, "proj_a", PrepareInput{Scope: []string{"api"}}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	old := r.store.stored(t, busy.ID)
	if old.Status != StatusDone || old.State != StateStopped {
		t.Fatalf("old report %s/%s, want done/stopped", old.Status, old.State)
	}
	if got.ID == busy.ID || r.store.stored(t, got.ID).Status != StatusPlanning {
		t.Fatalf("new report %+v", got)
	}
	if n := len(r.notices.all()); n != 1 {
		t.Fatalf("%d notices for the settled report, want 1", n)
	}
}

func TestPrepareEnqueuesTheJob(t *testing.T) {
	r := newRig(t)
	got, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{Scope: []string{"api"}}, ViaCLI)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPlanning || got.State != StatePlanning || got.RequestedBy != "usr_dev" || got.Via != ViaCLI || got.OrgID != "org_a" {
		t.Fatalf("report %+v", got)
	}
	jobs := r.pendingJobs(t)
	if len(jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(jobs))
	}
	if p := payloadOf(t, jobs[0]); p != (JobPayload{OrgID: "org_a", ReportID: got.ID}) {
		t.Fatalf("payload %+v", p)
	}
	if jobs[0].Queue != agentruns.SweepQueue {
		t.Fatalf("queue %q, want %q", jobs[0].Queue, agentruns.SweepQueue)
	}
	audits := r.audit.named("release.prepare")
	if len(audits) != 1 {
		t.Fatalf("audit %+v", r.audit.events)
	}
	a := audits[0]
	if a.ProjectID != "proj_a" || a.Principal.UserID != "usr_dev" || a.Attributes["report_id"] != got.ID || a.Attributes["from"] != "staging" ||
		a.Attributes["to"] != "production" || a.Attributes["via"] != ViaCLI || a.Attributes["replace"] != false ||
		!slices.Equal(a.Attributes["scope"].([]string), []string{"api"}) {
		t.Fatalf("audit %+v", a)
	}
}

func TestPrepareSettlesWhenTheJobCannotBeQueued(t *testing.T) {
	r := newRig(t)
	r.svc.d.Jobs = func() *dispatchengine.Engine { return nil }
	_, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard)
	if err == nil {
		t.Fatal("prepared with no job engine")
	}
	reports, _ := r.store.List(context.Background(), "org_a", "proj_a", 10)
	if len(reports) != 1 || reports[0].Status != StatusFailed || reports[0].State != StateFailed || reports[0].StateReason == "" {
		t.Fatalf("reports %+v", reports)
	}
	// The environment is free again.
	r.svc.d.Jobs = func() *dispatchengine.Engine { return r.jobs }
	if _, err = r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard); err != nil {
		t.Fatal(err)
	}
}

func TestCancelLeavesThePlan(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	got, err := r.svc.Cancel(context.Background(), dev, rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCancelled || got.State != StateCancelled || !strings.Contains(got.StateReason, "usr_dev") {
		t.Fatalf("report %s/%s %q", got.Status, got.State, got.StateReason)
	}
	// The requester can approve too, so the run is stopped the way an
	// approver stops it, needing nothing else of them.
	if !slices.Equal(r.runs.cancelTasks, []string{"org_a atask_1"}) || len(r.runs.cancels) != 0 {
		t.Fatalf("cancels %v, cancel tasks %v", r.runs.cancels, r.runs.cancelTasks)
	}
	if r.planStore.plans["plan_a"].Status != planning.StatusReady {
		t.Fatalf("the plan is %s", r.planStore.plans["plan_a"].Status)
	}
	if a := r.audit.named("release.cancel"); len(a) != 1 || a[0].Attributes["report_id"] != rep.ID || a[0].ProjectID != "proj_a" {
		t.Fatalf("audit %+v", a)
	}
	// A cancelled report can't be cancelled again.
	if _, err = r.svc.Cancel(context.Background(), dev, rep.ID); !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("again: %v, want ErrNotCancelable", err)
	}
}

// A requester who can approve but no longer holds agent:use still stops
// the run: approve is what let them cancel, so the run is cancelled the way
// an approver cancels it, not through 3a's requester path that needs
// agent:use.
func TestCancelByAnApprovingRequesterStopsTheRun(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	r.w.authz.refuse = map[string]bool{"usr_dev " + security.AgentUse.Name(): true}
	got, err := r.svc.Cancel(context.Background(), dev, rep.ID)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("%+v %v", got, err)
	}
	if !slices.Equal(r.runs.cancelTasks, []string{"org_a atask_1"}) || len(r.runs.cancels) != 0 {
		t.Fatalf("cancels %v, cancel tasks %v: the run was left going", r.runs.cancels, r.runs.cancelTasks)
	}
}

func TestCancelBeforeTheTaskExists(t *testing.T) {
	r := newRig(t)
	got, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = r.svc.Cancel(context.Background(), dev, got.ID); err != nil || got.Status != StatusCancelled {
		t.Fatalf("%+v %v", got, err)
	}
	if len(r.runs.cancels)+len(r.runs.cancelTasks) != 0 {
		t.Fatal("cancelled a task that doesn't exist")
	}
}

func TestCancelNeedsRequesterOrApprover(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)

	// Another developer may read but not approve.
	r.w.authz.refuse = map[string]bool{"usr_bo " + security.DeploymentApprove.Name(): true}
	if _, err := r.svc.Cancel(ctx, user("usr_bo"), rep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("bo: %v, want ErrForbidden", err)
	}
	// The agent can't cancel a preparation either.
	if _, err := r.svc.Cancel(ctx, security.AgentPrincipal("org_a", &dev), rep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("agent: %v, want ErrForbidden", err)
	}
	// Someone of another organization doesn't find it.
	if _, err := r.svc.Cancel(ctx, security.Principal{OrgID: "org_b", UserID: "usr_dev"}, rep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b: %v, want ErrNotFound", err)
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing || len(r.runs.cancels)+len(r.runs.cancelTasks) != 0 {
		t.Fatalf("refused cancels changed %s, %v %v", s.Status, r.runs.cancels, r.runs.cancelTasks)
	}

	// Ada approves production: she cancels someone else's with CancelTask.
	got, err := r.svc.Cancel(ctx, ada, rep.ID)
	if err != nil || got.Status != StatusCancelled || !strings.Contains(got.StateReason, "usr_ada") {
		t.Fatalf("ada: %+v %v", got, err)
	}
	if !slices.Equal(r.runs.cancelTasks, []string{"org_a atask_1"}) || len(r.runs.cancels) != 0 {
		t.Fatalf("cancels %v, cancel tasks %v", r.runs.cancels, r.runs.cancelTasks)
	}
}

func TestNoteOnlyFromItsOwnTask(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	agentP := security.AgentPrincipal("org_a", &dev)

	if _, err := r.svc.Note(ctx, agentP, "atask_other", rep.ID, Concern{Kind: ConcernAsk, Text: "approve it"}); !errors.Is(err, ErrNotYourTask) {
		t.Fatalf("other task: %v, want ErrNotYourTask", err)
	}
	if _, err := r.svc.Note(ctx, agentP, "", rep.ID, Concern{Kind: ConcernAsk, Text: "approve it"}); !errors.Is(err, ErrNotYourTask) {
		t.Fatalf("no task: %v, want ErrNotYourTask", err)
	}
	for name, c := range map[string]Concern{
		"bad kind": {Kind: "approve", Text: "fine"},
		"empty":    {Kind: ConcernRisk, Text: "   "},
		"too long": {Kind: ConcernRisk, Text: strings.Repeat("é", MaxConcernLength+1)},
	} {
		if _, err := r.svc.Note(ctx, agentP, "atask_1", rep.ID, c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v, want ErrInvalid", name, err)
		}
	}

	got, err := r.svc.Note(ctx, agentP, "atask_1", rep.ID, Concern{Kind: ConcernRisk, Text: " The migration drops a column. "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Concerns) != 1 || got.Concerns[0].Text != "The migration drops a column." || got.Concerns[0].Kind != ConcernRisk ||
		got.Concerns[0].RunID != "arun_1" || !got.Concerns[0].At.Equal(rigNow) {
		t.Fatalf("concerns %+v", got.Concerns)
	}
	if !slices.Contains(r.shield.scanned(), "The migration drops a column.") {
		t.Fatalf("the text wasn't outbound-scanned: %q", r.shield.scanned())
	}

	// A concern the scan blocks keeps its kind and none of its text.
	got, err = r.svc.Note(ctx, agentP, "atask_1", rep.ID, Concern{Kind: ConcernAsk, Text: "key AKIAIOSFODNN7EXAMPLE leaked"})
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Concerns[1]; c.Kind != ConcernAsk || c.Text != withheldConcern {
		t.Fatalf("blocked concern %+v", c)
	}
	audits := r.audit.named("release.concern")
	if len(audits) != 2 || audits[1].Attributes["kind"] != ConcernAsk || audits[1].Attributes["report_id"] != rep.ID {
		t.Fatalf("audit %+v", audits)
	}
	for _, a := range audits {
		for k, v := range a.Attributes {
			if s, ok := v.(string); ok && (strings.Contains(s, "AKIA") || strings.Contains(s, "migration")) {
				t.Fatalf("audit %s quotes the text: %q", k, s)
			}
		}
	}

	// The agent off: nothing can be scanned, so the text is withheld.
	r.tenants.off["org_a"] = true
	got, err = r.svc.Note(ctx, agentP, "atask_1", rep.ID, Concern{Kind: ConcernRisk, Text: "plain words"})
	if err != nil || got.Concerns[2].Text != withheldConcern {
		t.Fatalf("unbound: %+v %v", got.Concerns, err)
	}
}

func TestNoteRefusesAFinishedReport(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	if _, err := r.svc.Cancel(context.Background(), dev, rep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Note(context.Background(), security.AgentPrincipal("org_a", &dev), "atask_1", rep.ID, Concern{Kind: ConcernRisk, Text: "late"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, want ErrInvalid", err)
	}
}

func TestGetFillsTheRequestAndDecider(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
	r.requests.list = []*agentrequests.Request{
		{ID: "areq_other_plan", Kind: agentrequests.KindApprovePlan, TaskID: "atask_1", Target: agentrequests.Target{PlanID: "plan_b"}, CreatedAt: rigNow},
		{ID: "areq_other_task", Kind: agentrequests.KindApprovePlan, TaskID: "atask_9", Target: agentrequests.Target{PlanID: "plan_a"}, CreatedAt: rigNow},
		{ID: "areq_answer", Kind: agentrequests.KindAnswer, TaskID: "atask_1", Target: agentrequests.Target{PlanID: "plan_a"}, CreatedAt: rigNow},
		{ID: "areq_old", Kind: agentrequests.KindApprovePlan, TaskID: "atask_1", Target: agentrequests.Target{PlanID: "plan_a"}, CreatedAt: rigNow.Add(-1)},
		{ID: "areq_new", Kind: agentrequests.KindApprovePlan, TaskID: "atask_1", Target: agentrequests.Target{PlanID: "plan_a"}, CreatedAt: rigNow},
	}
	got, err := r.svc.Get(ctx, dev, rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "areq_new" || got.State != StateWaiting {
		t.Fatalf("request %q state %q", got.RequestID, got.State)
	}
	if f := r.requests.filters[0]; f.Status != "all" || f.ProjectID != "proj_a" || f.Limit != 100 {
		t.Fatalf("filter %+v", f)
	}
	if c := r.requests.callers[0]; c.UserID != "usr_dev" || c.IsAgent() {
		t.Fatalf("requests read as %+v", c)
	}

	// Ada approved: Get names her and reads approved.
	r.w.plans.byID["plan_a"].Status = planning.StatusApproved
	r.w.plans.byID["plan_a"].DecidedBy = "usr_ada"
	got, err = r.svc.Get(ctx, dev, rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DecidedBy != "usr_ada" || got.State != StateApproved || got.StateReason != "approved by usr_ada" {
		t.Fatalf("decided %q state %q %q", got.DecidedBy, got.State, got.StateReason)
	}
	if r.store.stored(t, rep.ID).State != StateReviewing {
		t.Fatal("Get wrote the state")
	}
}

func TestGetNeedsDeploymentRead(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	r.w.authz.refuse = map[string]bool{"usr_out " + security.DeploymentRead.Name(): true}
	if _, err := r.svc.Get(context.Background(), user("usr_out"), rep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("%v, want ErrForbidden", err)
	}
	if _, err := r.svc.Get(context.Background(), security.Principal{OrgID: "org_b", UserID: "usr_dev"}, rep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b: %v, want ErrNotFound", err)
	}
	last := r.w.authz.checks[len(r.w.authz.checks)-1]
	if last.perm != security.DeploymentRead || last.resource != "proj_a" {
		t.Fatalf("checked %s on %q", last.perm.Name(), last.resource)
	}
}

func TestGetSkipsTheRequestItCannotRead(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
	r.requests.err = security.ErrForbidden
	got, err := r.svc.Get(context.Background(), dev, rep.ID)
	if err != nil || got.RequestID != "" || got.State != StateWaiting {
		t.Fatalf("%+v %v", got, err)
	}
	// The agent's read never asks the requests, which are for people.
	r.requests.err = nil
	if _, err = r.svc.Get(context.Background(), security.AgentPrincipal("org_a", &dev), rep.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(r.requests.callers); n != 1 {
		t.Fatalf("requests read %d times", n)
	}
}

func TestGetKeepsASettledReportsState(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.w.plans.byID["plan_a"].Status = planning.StatusApproved
	r.w.plans.byID["plan_a"].DecidedBy = "usr_ada"
	got, err := r.svc.Get(context.Background(), dev, rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateStopped || got.DecidedBy != "usr_ada" {
		t.Fatalf("state %q decided by %q", got.State, got.DecidedBy)
	}
}

func TestListBoundsItsLimit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, limit := range []int{-1, 51} {
		if _, err := r.svc.List(ctx, dev, "proj_a", limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit %d: %v, want ErrInvalid", limit, err)
		}
	}
	r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
	got, err := r.svc.List(ctx, dev, "proj_a", 0)
	if err != nil || len(got) != 1 || got[0].State != StateWaiting {
		t.Fatalf("%+v %v", got, err)
	}
	r.w.authz.refuse = map[string]bool{"usr_out " + security.DeploymentRead.Name(): true}
	if _, err = r.svc.List(ctx, user("usr_out"), "proj_a", 0); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("%v, want ErrForbidden", err)
	}
}

// A project of another workspace lists as not found, as planning's list
// does, rather than as an empty list: the permission check names a
// project ID, and only the store knows which workspace it belongs to.
func TestListKeepsToItsWorkspacesProjects(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
	r.places.goneProj = map[string]bool{"proj_elsewhere": true}
	if got, err := r.svc.List(ctx, dev, "proj_elsewhere", 0); !errors.Is(err, projects.ErrNotFound) || got != nil {
		t.Fatalf("another workspace's project: %+v %v, want projects.ErrNotFound", got, err)
	}
	r.places.mu.Lock()
	orgs := slices.Clone(r.places.lastOrgIDs)
	r.places.mu.Unlock()
	if len(orgs) != 1 || orgs[0] != dev.OrgID {
		t.Fatalf("the project was read in %v", orgs)
	}
	if got, err := r.svc.List(ctx, dev, "proj_a", 0); err != nil || len(got) != 1 {
		t.Fatalf("its own project: %+v %v", got, err)
	}
}

func TestSettleNotifiesOncePerState(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionPromote)
	r.planStore.plans["plan_a"].Status = planning.StatusApproved
	r.planStore.plans["plan_a"].DecidedBy = security.AgentSubject("org_a")

	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	s := r.store.stored(t, rep.ID)
	if s.Status != StatusDone || s.State != StatePromoted || s.StateReason == "" || s.FinishedAt == nil {
		t.Fatalf("settled %s/%s %q", s.Status, s.State, s.StateReason)
	}
	notices := r.notices.all()
	if len(notices) != 1 {
		t.Fatalf("%d notices, want 1", len(notices))
	}
	n := notices[0]
	// The requester and the project's team, each once, sorted.
	if !slices.Equal(n.Recipients, []string{"usr_ada", "usr_dev", "usr_lee"}) {
		t.Fatalf("recipients %v", n.Recipients)
	}
	if r.teams.calls[0] != [4]string{"org_a", "proj_a", "", ""} {
		t.Fatalf("resolved %v", r.teams.calls)
	}
	if n.Kind != notifications.KindAgentRelease || n.SubjectID != rep.ID+":"+StatePromoted || n.OrgID != "org_a" ||
		n.ProjectID != "proj_a" || n.Path != "/release-reports/"+rep.ID || n.Need != security.DeploymentRead.Name() {
		t.Fatalf("notice %+v", n)
	}
	if !strings.HasPrefix(n.Title, "Release to production: ") || n.Body == "" {
		t.Fatalf("title %q body %q", n.Title, n.Body)
	}
	if n.Metadata["release_report_id"] != rep.ID || n.Metadata["plan_id"] != "plan_a" || n.Metadata["state"] != StatePromoted || len(n.Metadata) != 3 {
		t.Fatalf("metadata %v", n.Metadata)
	}
	if n.Push != nil {
		t.Fatal("a push with no push mapper")
	}

	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(r.notices.all()); n != 1 {
		t.Fatalf("second settle sent %d notices in all", n)
	}
}

func TestSettleNotifiesOnlyWhatNeedsSomeone(t *testing.T) {
	cases := []struct {
		plan, decidedBy, task, decision string
		state                           string
		notify                          bool
	}{
		{planning.StatusApproved, "usr_ada", agentruns.StatusCompleted, DecisionAsk, StateApproved, false},
		{planning.StatusRejected, "usr_ada", agentruns.StatusCompleted, DecisionAsk, StateRejected, false},
		{planning.StatusExpired, "", agentruns.StatusCompleted, DecisionAsk, StateExpired, false},
		{planning.StatusSuperseded, "", agentruns.StatusCompleted, DecisionAsk, StateSuperseded, false},
		{planning.StatusStale, security.AgentSubject("org_a"), agentruns.StatusCompleted, DecisionPromote, StateStale, true},
		{planning.StatusBlocked, "", agentruns.StatusCompleted, DecisionNone, StateBlocked, true},
		{planning.StatusReady, "", agentruns.StatusCompleted, DecisionAsk, StateStopped, true},
		{planning.StatusReady, "", agentruns.StatusWaiting, DecisionAsk, StateWaiting, false},
		{planning.StatusReady, "", agentruns.StatusRunning, DecisionPromote, StatePromoting, false},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			r := newRig(t)
			rep := r.reviewing(t, c.task, c.decision)
			r.planStore.plans["plan_a"].Status, r.planStore.plans["plan_a"].DecidedBy = c.plan, c.decidedBy
			if err := r.svc.Settle(context.Background()); err != nil {
				t.Fatal(err)
			}
			s := r.store.stored(t, rep.ID)
			if Terminal(c.state) && (s.State != c.state || s.Status != StatusDone) {
				t.Fatalf("settled %s/%s, want done/%s", s.Status, s.State, c.state)
			}
			if !Terminal(c.state) && (s.State != StateReviewing || s.Status != StatusReviewing) {
				t.Fatalf("an open report moved to %s/%s", s.Status, s.State)
			}
			if got := len(r.notices.all()) == 1; got != c.notify {
				t.Fatalf("notified %v, want %v", got, c.notify)
			}
		})
	}
}

// Review Focus 5: the workspace turns its agent off while the agent
// reviews. 3a cancels the task with no reason of its own; Settle reads the
// plan and task without the agent's roles, names why, and leaves the plan
// ready for a person.
func TestSettleWhenTheAgentIsTurnedOff(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionPromote)
	r.runs.cancelAll("org_a")
	r.w.budgets.off = true
	r.tenants.off["org_a"] = true

	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	s := r.store.stored(t, rep.ID)
	if s.Status != StatusDone || s.State != StateStopped {
		t.Fatalf("settled %s/%s, want done/stopped", s.Status, s.State)
	}
	if !strings.Contains(s.StateReason, agentsettings.ErrDisabled.Error()) || !strings.Contains(s.StateReason, waitsForAPerson) {
		t.Fatalf("reason %q", s.StateReason)
	}
	if p := r.planStore.plans["plan_a"]; p.Status != planning.StatusReady || p.DecidedBy != "" {
		t.Fatalf("the plan is %s by %q", p.Status, p.DecidedBy)
	}
	// Nothing read as the agent, or as anyone.
	if r.w.plans.reads != 0 {
		t.Fatalf("read plans as a principal %d times", r.w.plans.reads)
	}
	if r.planStore.reads == 0 || r.runs.byRunCalls == 0 {
		t.Fatal("didn't read the plan store and the task by its run")
	}
	// The notice is Loomplane's own words: nothing could be scanned.
	notices := r.notices.all()
	if len(notices) != 1 || notices[0].Body != sentence(s.StateReason) {
		t.Fatalf("notices %+v", notices)
	}
	if len(r.shield.scanned()) != 0 {
		t.Fatalf("scanned %q with the agent off", r.shield.scanned())
	}
}

func TestSettleWhenTheBudgetRunsOut(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusFailed, DecisionNone)
	r.runs.byRun["arun_1"].Error = agentsettings.ErrBudgetExhausted.Error()
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.store.stored(t, rep.ID)
	if s.State != StateStopped || !strings.Contains(s.StateReason, agentsettings.ErrBudgetExhausted.Error()) {
		t.Fatalf("settled %s %q", s.State, s.StateReason)
	}
	if r.planStore.plans["plan_a"].Status != planning.StatusReady {
		t.Fatal("the plan moved")
	}
}

// Owner Q1: a requester without deployment:approve gets the review; the
// plan waits for someone who can approve, and their team hears of it.
func TestSettleARequesterWhoCannotApprove(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionNone)
	r.runs.byRun["arun_1"].ReleasePlanID, r.runs.byRun["arun_1"].ReleaseRevision = "", ""
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.store.stored(t, rep.ID)
	if s.State != StateStopped || s.StateReason != reasonCantApprove {
		t.Fatalf("settled %s %q", s.State, s.StateReason)
	}
	notices := r.notices.all()
	if len(notices) != 1 || !slices.Equal(notices[0].Recipients, []string{"usr_ada", "usr_dev", "usr_lee"}) {
		t.Fatalf("notices %+v", notices)
	}
}

// Task 2's M5: there are no foreign keys, so a report can outlive its
// project, environment or plan. Settle closes it with a reason of
// Loomplane's own.
func TestSettleClosesReportsWhoseRecordsAreGone(t *testing.T) {
	cases := []struct {
		name   string
		gone   func(r *rig)
		reason string
	}{
		{"plan", func(r *rig) { delete(r.planStore.plans, "plan_a") }, reasonNoPlan},
		{"project", func(r *rig) { r.places.goneProj = map[string]bool{"proj_a": true} }, reasonNoProject},
		{"target environment", func(r *rig) { r.places.goneEnv = map[string]bool{"env_prd": true} }, reasonNoEnvironment},
		{"source environment", func(r *rig) { r.places.goneEnv = map[string]bool{"env_stg": true} }, reasonNoEnvironment},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			rep := r.reviewing(t, agentruns.StatusWaiting, DecisionAsk)
			c.gone(r)
			if err := r.svc.Settle(context.Background()); err != nil {
				t.Fatal(err)
			}
			s := r.store.stored(t, rep.ID)
			if s.State != StateFailed || s.StateReason != c.reason || Terminal(s.State) == false || s.Status == StatusReviewing {
				t.Fatalf("settled %s/%s %q", s.Status, s.State, s.StateReason)
			}
			if len(r.notices.all()) != 1 {
				t.Fatalf("%d notices", len(r.notices.all()))
			}
		})
	}

	// A planning report whose project is gone is closed too: no job will.
	r := newRig(t)
	got, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	r.places.goneProj = map[string]bool{"proj_a": true}
	if err = r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.store.stored(t, got.ID); s.Status != StatusFailed || s.State != StateFailed || s.StateReason != reasonNoProject {
		t.Fatalf("planning report %s/%s %q", s.Status, s.State, s.StateReason)
	}
}

func TestSettleLeavesAReportWhenItCannotRead(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	r.planStore.err = errors.New("connection refused")
	if err := r.svc.Settle(context.Background()); err == nil {
		t.Fatal("no error for an unreadable plan")
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing || len(r.notices.all()) != 0 {
		t.Fatalf("settled %s on an unreadable plan", s.Status)
	}
	r.planStore.err = nil
	r.places.err = errors.New("connection refused")
	if err := r.svc.Settle(context.Background()); err == nil {
		t.Fatal("no error for an unreadable project")
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing {
		t.Fatalf("settled %s on an unreadable project", s.Status)
	}
}

// A notice that fails leaves the report unsettled, so the next sweep
// sends it and settles.
func TestSettleRetriesAFailedNotice(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	r.notices.err = errors.New("outbox down")
	if err := r.svc.Settle(context.Background()); err == nil {
		t.Fatal("no error for a failed notice")
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing {
		t.Fatalf("settled %s before its notice went", s.Status)
	}
	r.notices.err = nil
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.store.stored(t, rep.ID); s.State != StateStopped || len(r.notices.all()) != 1 {
		t.Fatalf("settled %s with %d notices", s.State, len(r.notices.all()))
	}
}

func TestSettleNoticeQuotesConcernsOnlyWhenScanned(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	_ = r.store.AddConcern(ctx, "org_a", rep.ID, Concern{Kind: ConcernAsk, Text: "The migration drops users.email.", RunID: "arun_1", At: rigNow})
	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	n := r.notices.all()[0]
	if !strings.Contains(n.Body, "The migration drops users.email.") || !strings.HasPrefix(n.Body, sentence(reasonUndecided)) {
		t.Fatalf("body %q", n.Body)
	}
	if !slices.Contains(r.shield.scanned(), n.Body) {
		t.Fatalf("the body wasn't scanned as sent: %q", r.shield.scanned())
	}

	// A concern the scan blocks: the plain notice goes.
	r = newRig(t)
	rep = r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	_ = r.store.AddConcern(ctx, "org_a", rep.ID, Concern{Kind: ConcernRisk, Text: "uses AKIAIOSFODNN7EXAMPLE", RunID: "arun_1", At: rigNow})
	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	n = r.notices.all()[0]
	if n.Body != sentence(reasonUndecided) || strings.Contains(n.Body, "AKIA") {
		t.Fatalf("body %q", n.Body)
	}
}

func TestSettlePushIsOptional(t *testing.T) {
	r := newRig(t)
	push := &fakePush{}
	r.svc.d.Push = push
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.notices.all(); len(n) != 1 || n[0].Push == nil || n[0].Push.Collapse != "release:"+rep.ID {
		t.Fatalf("notices %+v", n)
	}
	if !slices.Equal(push.states, []string{rep.ID + ":" + StateStopped}) {
		t.Fatalf("push asked %v", push.states)
	}

	// A push that can't be built doesn't hold the notice back.
	r = newRig(t)
	r.svc.d.Push = &fakePush{err: errors.New("no mapper")}
	r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := r.notices.all(); len(n) != 1 || n[0].Push != nil {
		t.Fatalf("notices %+v", n)
	}
}

func TestLookupReadsWithoutAPrincipal(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	got, err := r.svc.Lookup(context.Background(), "org_a", rep.ID)
	if err != nil || got.ID != rep.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = r.svc.Lookup(context.Background(), "org_b", rep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b: %v", err)
	}
}

func TestServiceSharesItsCollectorWithTheGate(t *testing.T) {
	r := newRig(t)
	if r.svc.Gate().Collector != r.svc.Collector() || r.svc.Gate().Store != r.store {
		t.Fatal("the gate reads through another collector or store")
	}
	w := greenWorld()
	r.svc.SetSources(w.sources())
	if r.svc.Collector().Sources.Plans != w.plans {
		t.Fatal("SetSources didn't reach the collector")
	}
}

// Fix round 1, I1: the agent promoted, and a cancel arrives before the
// sweep. The outcome stands: the cancel is refused, the report settles
// promoted, and the promoted notice goes.
func TestCancelRefusesAnOutcomeThatHappened(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionPromote)
	r.planStore.plans["plan_a"].Status = planning.StatusApproved
	r.planStore.plans["plan_a"].DecidedBy = security.AgentSubject("org_a")
	r.w.plans.byID["plan_a"].Status = planning.StatusApproved
	r.w.plans.byID["plan_a"].DecidedBy = security.AgentSubject("org_a")

	_, err := r.svc.Cancel(context.Background(), dev, rep.ID)
	if !errors.Is(err, ErrNotCancelable) || !strings.Contains(err.Error(), StatePromoted) {
		t.Fatalf("%v, want ErrNotCancelable naming %s", err, StatePromoted)
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusDone || s.State != StatePromoted {
		t.Fatalf("report %s/%s, want done/promoted", s.Status, s.State)
	}
	notices := r.notices.all()
	if len(notices) != 1 || notices[0].SubjectID != rep.ID+":"+StatePromoted {
		t.Fatalf("notices %+v", notices)
	}
	if len(r.runs.cancels)+len(r.runs.cancelTasks) != 0 || len(r.audit.named("release.cancel")) != 0 {
		t.Fatalf("cancelled %v %v", r.runs.cancels, r.runs.cancelTasks)
	}
	// A stopped run is an outcome too.
	r = newRig(t)
	rep = r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	if _, err = r.svc.Cancel(context.Background(), ada, rep.ID); !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("stopped: %v", err)
	}
	if s := r.store.stored(t, rep.ID); s.State != StateStopped {
		t.Fatalf("report %s", s.State)
	}
}

// Fix round 1, I2: a cancel that lands between the sweep's derive and its
// settle wins cleanly, and no notice goes for the state that lost.
func TestSettleLosesToACancelCleanly(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	r.store.beforeFinish = func() {
		if err := r.store.Settle(context.Background(), "org_a", rep.ID, StateReviewing, StatusCancelled, StateCancelled, "cancelled by usr_dev", "", rigNow); err != nil {
			t.Error(err)
		}
	}
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.store.stored(t, rep.ID); s.State != StateCancelled {
		t.Fatalf("report %s", s.State)
	}
	if n := r.notices.all(); len(n) != 0 {
		t.Fatalf("notices %+v", n)
	}
}

// A failed notice leaves the report unsettled and un-notified.
func TestSettleFailedNoticeLeavesNoMark(t *testing.T) {
	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusCompleted, DecisionAsk)
	r.notices.err = errors.New("outbox down")
	_ = r.svc.Settle(context.Background())
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing || s.State != StateReviewing || s.FinishedAt != nil {
		t.Fatalf("report %s/%s", s.Status, s.State)
	}
	if n := r.store.notified[rep.ID]; n != "" {
		t.Fatalf("marked %q", n)
	}
}

// Fix round 1, I3: the plan the person agreed to replace travels with the
// job, so the job supersedes that plan and no other.
func TestPrepareCarriesThePlanItMaySupersede(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(r *rig)
		in    PrepareInput
		want  string
	}{
		{"replace against a person's plan", func(r *rig) { r.openPlan(planning.KindRelease, "", "usr_ada") }, PrepareInput{Replace: true}, "plan_open"},
		{"no open plan", func(*rig) {}, PrepareInput{}, ""},
		{"replace with no open plan", func(*rig) {}, PrepareInput{Replace: true}, ""},
		{"a decided plan", func(r *rig) {
			r.openPlan(planning.KindRelease, "", "usr_ada").Status = planning.StatusApproved
			r.w.plans.open[0].Status = planning.StatusApproved
		}, PrepareInput{Replace: true}, ""},
		{"a reused plan", func(r *rig) { r.openPlan(planning.KindPromotion, "staging", "usr_ada") }, PrepareInput{Scope: []string{"api"}, Replace: true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cr := newRig(t)
			c.setup(cr)
			got, err := cr.svc.Prepare(ctx, dev, "proj_a", c.in, ViaDashboard)
			if err != nil {
				t.Fatal(err)
			}
			jobs := cr.pendingJobs(t)
			if len(jobs) != 1 {
				t.Fatalf("%d jobs", len(jobs))
			}
			if p := payloadOf(t, jobs[0]); p != (JobPayload{OrgID: "org_a", ReportID: got.ID, Supersedes: c.want}) {
				t.Fatalf("payload %+v, want supersedes %q", p, c.want)
			}
			if a := cr.audit.named("release.prepare"); len(a) != 1 || a[0].Attributes["supersedes"] != c.want {
				t.Fatalf("audit %+v", a)
			}
		})
	}
}

// Fix round 1, I4: cancelling takes deployment:approve, or being the
// requester with agent:use. A read-only key holds neither.
func TestCancelNeedsAgentUseOrApprove(t *testing.T) {
	ctx := context.Background()
	readOnly := dev
	readOnly.ReadOnly = true

	r := newRig(t)
	rep := r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	if _, err := r.svc.Cancel(ctx, readOnly, rep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only key: %v, want ErrForbidden", err)
	}
	r.w.authz.refuse = map[string]bool{
		"usr_dev " + security.AgentUse.Name(): true, "usr_dev " + security.DeploymentApprove.Name(): true,
	}
	if _, err := r.svc.Cancel(ctx, dev, rep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("requester without agent:use: %v, want ErrForbidden", err)
	}
	if s := r.store.stored(t, rep.ID); s.Status != StatusReviewing || len(r.runs.cancels)+len(r.runs.cancelTasks) != 0 {
		t.Fatalf("refused cancels changed %s, %v %v", s.Status, r.runs.cancels, r.runs.cancelTasks)
	}
	// The requester with agent:use, who can't approve, cancels.
	r.w.authz.refuse = map[string]bool{"usr_dev " + security.DeploymentApprove.Name(): true}
	if got, err := r.svc.Cancel(ctx, dev, rep.ID); err != nil || got.Status != StatusCancelled {
		t.Fatalf("requester: %+v %v", got, err)
	}
	for _, c := range r.w.authz.checks {
		if c.resource != "proj_a" {
			t.Fatalf("%s checked on %q", c.perm.Name(), c.resource)
		}
	}
	// An approver without agent:use cancels someone else's.
	r = newRig(t)
	rep = r.reviewing(t, agentruns.StatusRunning, DecisionNone)
	r.w.authz.refuse = map[string]bool{"usr_ada " + security.AgentUse.Name(): true}
	if got, err := r.svc.Cancel(ctx, ada, rep.ID); err != nil || got.Status != StatusCancelled {
		t.Fatalf("approver: %+v %v", got, err)
	}
}

// Fix round 1, M1: a preparation whose job never ran (lost while AI was
// off, or a crash between storing it and queueing the job) stops holding
// production after an hour.
func TestSettleStopsAPreparationWhoseJobNeverRan(t *testing.T) {
	r := newRig(t)
	got, err := r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard)
	if err != nil {
		t.Fatal(err)
	}
	r.advance(59 * time.Minute)
	if err = r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.store.stored(t, got.ID); s.Status != StatusPlanning {
		t.Fatalf("settled early: %s", s.Status)
	}
	r.advance(2 * time.Minute)
	if err = r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.store.stored(t, got.ID)
	if s.Status == StatusPlanning || s.State != StateStopped || s.StateReason != reasonJobNeverRan {
		t.Fatalf("report %s/%s %q", s.Status, s.State, s.StateReason)
	}
	if n := r.notices.all(); len(n) != 1 || n[0].SubjectID != got.ID+":"+StateStopped {
		t.Fatalf("notices %+v", n)
	}
	// Production is free again.
	if _, err = r.svc.Prepare(context.Background(), dev, "proj_a", PrepareInput{}, ViaDashboard); err != nil {
		t.Fatal(err)
	}
}
