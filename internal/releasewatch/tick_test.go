package releasewatch

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

const (
	failing      = "4.1% of the candidate's requests failed; the ceiling is 1%."
	thin         = "The candidate served 120 requests; at least 200 are needed to judge it."
	denied       = "Loomplane automation can't approve deployments in this organization; approve the rollback plan yourself"
	firstJudged  = releasehealth.MetricsLag + time.Minute
	watchEnds    = 15 * time.Minute
	watchBounded = 30 * time.Minute
)

func failed() releasehealth.Health {
	return releasehealth.Health{Requests: 1000, Errors: 41, Available: true}
}

func sparse() releasehealth.Health {
	return releasehealth.Health{Requests: 120, Available: true}
}

// watching is a rig with dep_1 of plan_1, which updated api on Render in
// a blue_green environment, armed when it finished.
func watching(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Update))
	r.arm(t, plan, succeeded(plan))
	r.reader.reset()
	return r
}

// candidates answers every candidate read with h and every baseline read
// (a window ending when the deployment started) healthy.
func (r *rig) candidates(h func() releasehealth.Health) {
	r.reader.mu.Lock()
	defer r.reader.mu.Unlock()
	r.reader.answer = func(_ string, w releasehealth.Window) (releasehealth.Health, error) {
		if w.End.Equal(depStart) {
			return healthy(), nil
		}
		return h(), nil
	}
}

// tickAt runs a tick at finished+after and returns the watch.
func (r *rig) tickAt(t *testing.T, after time.Duration) *Watch {
	t.Helper()
	r.clock.set(finished.Add(after))
	if err := r.svc.Tick(t.Context()); err != nil {
		t.Fatalf("tick at +%s: %v", after, err)
	}
	return r.store.only(t)
}

func TestTick_waitsForMetricsLag(t *testing.T) {
	r := watching(t)
	w := r.tickAt(t, firstJudged-time.Second)
	if len(r.reader.calls()) != 0 || w.Status != StatusWatching || w.Services[0].Verdict != "" {
		t.Fatalf("reads %v, watch %+v", r.reader.calls(), w)
	}
}

func TestTick_judgesShortWindowsOfTheWholeService(t *testing.T) {
	r := watching(t)
	now := finished.Add(7 * time.Minute)
	r.tickAt(t, 7*time.Minute)
	candidate, err := metrics.ShortWindow(finished, now.Add(-releasehealth.MetricsLag))
	if err != nil {
		t.Fatal(err)
	}
	span := candidate.End.Sub(candidate.Start)
	baseline, err := metrics.ShortWindow(depStart.Add(-span), depStart)
	if err != nil {
		t.Fatal(err)
	}
	want := []read{
		{"org", "bind_api", "", releasehealth.Window{Start: candidate.Start, End: candidate.End}},
		{"org", "bind_api", "", releasehealth.Window{Start: baseline.Start, End: baseline.End}},
	}
	if got := r.reader.calls(); !slices.Equal(got, want) {
		t.Fatalf("reads %+v, want %+v", got, want)
	}
	if span != 4*time.Minute || baseline.End.Sub(baseline.Start) != span {
		t.Fatalf("candidate %s, baseline %s", span, baseline.End.Sub(baseline.Start))
	}
}

func TestTick_passesAtTheEndOfTheWindow(t *testing.T) {
	r := watching(t)
	for _, after := range []time.Duration{firstJudged, 10 * time.Minute, watchEnds, watchEnds + releasehealth.MetricsLag - time.Second} {
		w := r.tickAt(t, after)
		if w.Status != StatusWatching || w.Services[0].Verdict != string(releasehealth.Pass) || w.Services[0].CheckedAt == nil {
			t.Fatalf("at +%s: %+v", after, w)
		}
	}
	w := r.tickAt(t, watchEnds+releasehealth.MetricsLag)
	if w.Status != StatusPassed || w.Verdict != string(releasehealth.Pass) || w.Unverified || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}
	if want := "Passed: 0.1% errors (baseline 0.1%), p95 100ms (baseline 100ms)."; w.Services[0].Reason != want {
		t.Fatalf("service reason %q", w.Services[0].Reason)
	}
	if e := r.audit.find("release_watch.settle"); e == nil || e.Attributes["status"] != StatusPassed || !e.Principal.IsSystem() {
		t.Fatalf("audit %+v", e)
	}
	reads := len(r.reader.calls())
	r.tickAt(t, watchBounded)
	if len(r.reader.calls()) != reads {
		t.Fatal("a settled watch was judged again")
	}

	// A passed watch that couldn't watch every service stays unverified.
	r = newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	r.binding("site", "vercel")
	plan := release(service("api", "render", planning.Update), service("site", "vercel", planning.Update))
	r.arm(t, plan, succeeded(plan))
	if w = r.tickAt(t, watchEnds+releasehealth.MetricsLag); w.Status != StatusPassed || !w.Unverified {
		t.Fatalf("watch %+v", w)
	}
}

func TestTick_breachCreatesAndApprovesARollback(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	w := r.tickAt(t, firstJudged)
	if len(r.rollbacks.created) != 1 || r.rollbacks.createdFor[0] != "org/dep_1" {
		t.Fatalf("created %+v for %v", r.rollbacks.created, r.rollbacks.createdFor)
	}
	in := r.rollbacks.created[0]
	if in.Trigger != planning.TriggerWatch || in.Reason != "api: "+failing || in.WatchID != w.ID {
		t.Fatalf("rollback input %+v", in)
	}
	if w.Status != StatusRollingBack || w.RollbackPlanID != "plan_rb1" || w.Verdict != string(releasehealth.Fail) || w.Reason != "api: "+failing {
		t.Fatalf("watch %+v", w)
	}
	e := r.audit.find("release_watch.breach")
	if e == nil || !e.Principal.IsSystem() || e.Principal.OrgID != "org" || e.Attributes["watch_id"] != w.ID ||
		e.Attributes["service"] != "api" || e.Attributes["signal"] != failing || e.Attributes["restore_deployment_id"] != "dep_0" ||
		e.Attributes["rollback_plan_id"] != "plan_rb1" || e.ProjectID != "proj" {
		t.Fatalf("breach audit %+v", e)
	}

	// Planning: nothing to do yet.
	w = r.tickAt(t, firstJudged+time.Minute)
	if len(r.rollbacks.approved) != 0 || len(r.rollbacks.created) != 1 || w.Status != StatusRollingBack {
		t.Fatalf("approved %v while planning", r.rollbacks.approved)
	}
	r.plans.setStatus("plan_rb1", planning.StatusReady)
	r.tickAt(t, firstJudged+2*time.Minute)
	// The decision is on its way; the plan still reads ready.
	w = r.tickAt(t, firstJudged+3*time.Minute)
	if !slices.Equal(r.rollbacks.approved, []string{"plan_rb1"}) || w.RollbackApprovedAt == nil {
		t.Fatalf("approved %v, watch %+v", r.rollbacks.approved, w)
	}
	r.plans.setStatus("plan_rb1", planning.StatusApproved)
	w = r.tickAt(t, firstJudged+4*time.Minute)
	if w.Status != StatusRollingBack {
		t.Fatalf("watch %+v before the deployment", w)
	}
	r.deployments.put(&deployments.Deployment{ID: "dep_rb", PlanID: "plan_rb1", Status: deployments.StatusRunning})
	if w = r.tickAt(t, firstJudged+5*time.Minute); w.Status != StatusRollingBack {
		t.Fatalf("watch %+v while it deploys", w)
	}
	r.deployments.put(&deployments.Deployment{ID: "dep_rb", PlanID: "plan_rb1", Status: deployments.StatusSucceeded})
	w = r.tickAt(t, firstJudged+6*time.Minute)
	if w.Status != StatusRolledBack || w.SettledAt == nil || w.Verdict != string(releasehealth.Fail) {
		t.Fatalf("watch %+v", w)
	}
	if len(r.rollbacks.created) != 1 || len(r.rollbacks.approved) != 1 {
		t.Fatalf("created %d, approved %d", len(r.rollbacks.created), len(r.rollbacks.approved))
	}
}

func TestTick_breachLeavesTheRollbackForAPersonWhenAutomationIsDenied(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.status = planning.StatusReady
	r.rollbacks.approveErr = fmt.Errorf("%w (deployment:approve)", security.ErrForbidden)
	r.tickAt(t, firstJudged)
	w := r.tickAt(t, firstJudged+time.Minute)
	if w.Status != StatusRollbackBlocked || w.Reason != denied || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}
	r.tickAt(t, firstJudged+2*time.Minute)
	if len(r.rollbacks.approved) != 1 {
		t.Fatalf("approved %d times", len(r.rollbacks.approved))
	}
	if p, _ := r.plans.GetPlan(t.Context(), "org", "plan_rb1"); p.Status != planning.StatusReady {
		t.Fatalf("plan %s", p.Status)
	}
}

func TestTick_blockedRollbackPlan(t *testing.T) {
	for _, tt := range []struct {
		name, status, want string
		createErr          error
		plan               func(*planning.Plan)
	}{
		{name: "blocked", status: planning.StatusBlocked, want: "Loomplane couldn't roll the release back: api needs the database the release removed.",
			plan: func(p *planning.Plan) {
				p.Problems = []planning.Finding{{Code: "rollback-needs-resource", Message: "api needs the database the release removed."}}
			}},
		{name: "failed", status: planning.StatusFailed, want: "Loomplane couldn't roll the release back: the manifest is gone.",
			plan: func(p *planning.Plan) { p.Error = "the manifest is gone." }},
		{name: "approved with nothing to deploy", status: planning.StatusApproved, want: "Loomplane couldn't roll the release back: its plan has nothing it can deploy.",
			plan: func(p *planning.Plan) { p.Executable = false }},
		{name: "reverted canary", createErr: planning.ErrCanaryReverted, want: "Loomplane couldn't roll the release back: " + planning.ErrCanaryReverted.Error()},
		// A rollback is never watched, so the refusal to roll one back
		// never comes; if it did, it settles like any refusal.
		{name: "refused", createErr: fmt.Errorf("%w: the current release is a rollback; deploy or promote to move forward", planning.ErrInvalid),
			want: "Loomplane couldn't roll the release back: invalid plan request: the current release is a rollback; deploy or promote to move forward"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := watching(t)
			r.candidates(failed)
			r.rollbacks.createErr = tt.createErr
			w := r.tickAt(t, firstJudged)
			if tt.createErr == nil {
				p, _ := r.plans.GetPlan(t.Context(), "org", w.RollbackPlanID)
				p.Status = tt.status
				tt.plan(p)
				r.plans.put(p)
				w = r.tickAt(t, firstJudged+time.Minute)
			}
			if w.Status != StatusRollbackBlocked || w.Reason != tt.want || w.SettledAt == nil {
				t.Fatalf("watch %+v, want reason %q", w, tt.want)
			}
			if len(r.rollbacks.approved) != 0 || len(r.rollbacks.created) != 1 {
				t.Fatalf("created %d, approved %d", len(r.rollbacks.created), len(r.rollbacks.approved))
			}
			if r.audit.find("release_watch.breach") == nil {
				t.Fatalf("audit %v", r.audit.actions())
			}
		})
	}

	// A transient failure to plan the rollback is tried again.
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.createErr = errors.New("connection reset")
	r.clock.set(finished.Add(firstJudged))
	if err := r.svc.Tick(t.Context()); err == nil {
		t.Fatal("a transient failure was swallowed")
	}
	if w := r.store.only(t); w.Status != StatusRollingBack || w.RollbackPlanID != "" {
		t.Fatalf("watch %+v", w)
	}
	r.rollbacks.createErr = nil
	if w := r.tickAt(t, firstJudged+time.Minute); w.RollbackPlanID != "plan_rb2" || len(r.rollbacks.created) != 2 {
		t.Fatalf("watch %+v after %d tries", w, len(r.rollbacks.created))
	}
}

func TestTick_rollbackDeploymentFails(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.status = planning.StatusApproved
	r.tickAt(t, firstJudged)
	r.deployments.put(&deployments.Deployment{ID: "dep_rb", PlanID: "plan_rb1", Status: deployments.StatusFailed, Error: "Render refused the image."})
	w := r.tickAt(t, firstJudged+time.Minute)
	if w.Status != StatusRollbackFailed || w.Reason != "The rollback deployment failed: Render refused the image." || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}
}

func TestTick_inconclusiveSettlesAtTheBoundWithoutRollingBack(t *testing.T) {
	r := watching(t)
	r.candidates(sparse)
	for _, after := range []time.Duration{firstJudged, watchEnds, watchEnds + releasehealth.MetricsLag, watchBounded - time.Second} {
		w := r.tickAt(t, after)
		if w.Status != StatusWatching || w.Services[0].Verdict != string(releasehealth.Inconclusive) || w.Services[0].Reason != thin {
			t.Fatalf("at +%s: %+v", after, w)
		}
	}
	w := r.tickAt(t, watchBounded)
	if w.Status != StatusInconclusive || w.Verdict != string(releasehealth.Inconclusive) || !w.Unverified || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}
	if want := "The watch couldn't decide by its bound: api: " + thin; w.Reason != want {
		t.Fatalf("reason %q, want %q", w.Reason, want)
	}
	if len(r.rollbacks.created) != 0 {
		t.Fatalf("rolled back %+v", r.rollbacks.created)
	}
}

func TestTick_inconclusiveThenPassBeforeTheBound(t *testing.T) {
	r := watching(t)
	r.candidates(sparse)
	for _, after := range []time.Duration{firstJudged, watchEnds, watchEnds + 5*time.Minute} {
		if w := r.tickAt(t, after); w.Status != StatusWatching {
			t.Fatalf("at +%s: %+v", after, w)
		}
	}
	r.candidates(healthy)
	if w := r.tickAt(t, watchEnds+6*time.Minute); w.Status != StatusPassed || w.Unverified {
		t.Fatalf("watch %+v", w)
	}
}

func TestTick_readerErrorsCountAsInconclusive(t *testing.T) {
	r := watching(t)
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		return releasehealth.Health{}, errors.New("monitoring timed out")
	}
	w := r.tickAt(t, firstJudged)
	if w.Status != StatusWatching || w.Services[0].Verdict != string(releasehealth.Inconclusive) || w.Services[0].Reason != "monitoring timed out" {
		t.Fatalf("watch %+v", w)
	}
	if len(r.rollbacks.created) != 0 {
		t.Fatal("a reader error rolled back")
	}

	// A window ShortWindow refuses is inconclusive too: here a bound so
	// close to the start that nothing before it can be judged.
	r = watching(t)
	w = r.store.only(t)
	w.BoundAt = w.StartsAt.Add(2 * time.Minute)
	if err := r.store.Save(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	w = r.tickAt(t, firstJudged)
	if w.Services[0].Verdict != string(releasehealth.Inconclusive) || w.Services[0].Reason != metrics.ErrShortWindow.Error() || len(r.reader.calls()) != 0 {
		t.Fatalf("watch %+v, reads %v", w, r.reader.calls())
	}
	if w.Status != StatusInconclusive || len(r.rollbacks.created) != 0 {
		t.Fatalf("watch %+v", w)
	}
}

// A day-long watch judges its last day at most, the most a release
// window spans.
func TestTick_judgesAtMostADay(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n      watch: 24h\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Update))
	r.arm(t, plan, succeeded(plan))
	r.reader.reset()
	w := r.tickAt(t, 24*time.Hour+10*time.Minute)
	if w.Status != StatusPassed || w.Services[0].Verdict != string(releasehealth.Pass) {
		t.Fatalf("watch %+v", w)
	}
	reads := r.reader.calls()
	if len(reads) != 2 || reads[0].w.End.Sub(reads[0].w.Start) != 24*time.Hour || reads[1].w.End.Sub(reads[1].w.Start) != 24*time.Hour {
		t.Fatalf("reads %+v", reads)
	}
}

func TestTick_aNewerReleaseSupersedesTheWatch(t *testing.T) {
	// A newer release that moved the watched service.
	r := watching(t)
	r.candidates(failed)
	r.plans.movePlan("env_p", "api", "plan_2")
	w := r.tickAt(t, firstJudged)
	if w.Status != StatusSuperseded || w.SettledAt == nil || len(r.rollbacks.created) != 0 || len(r.reader.calls()) != 0 {
		t.Fatalf("watch %+v, rollbacks %v", w, r.rollbacks.created)
	}
	if want := "A newer release changed api before the watch finished, so this one isn't judged or rolled back."; w.Reason != want {
		t.Fatalf("reason %q", w.Reason)
	}

	// A newer release that left the watched service alone: rolling back
	// would undo it too.
	r = watching(t)
	r.candidates(failed)
	at := finished.Add(2 * time.Minute)
	r.history.push(planning.ReleaseRecord{DeploymentID: "dep_2", PlanID: "plan_2", Kind: planning.KindRelease, Status: deployments.StatusRunning, StartedAt: at})
	w = r.tickAt(t, firstJudged)
	if w.Status != StatusSuperseded || len(r.rollbacks.created) != 0 || len(r.reader.calls()) != 0 {
		t.Fatalf("watch %+v, rollbacks %v", w, r.rollbacks.created)
	}
	if want := "A newer release, dep_2, started before the watch finished, so this one isn't judged or rolled back."; w.Reason != want {
		t.Fatalf("reason %q", w.Reason)
	}
}

// A created service has no baseline of its own, so it's judged on the
// thresholds alone.
func TestTick_createdServicesAreJudgedOnTheThresholds(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Create))
	r.arm(t, plan, succeeded(plan))
	r.reader.reset()
	r.candidates(failed)
	w := r.tickAt(t, firstJudged)
	if len(r.reader.calls()) != 1 || w.Status != StatusRollingBack || w.Services[0].Reason != failing {
		t.Fatalf("reads %v, watch %+v", r.reader.calls(), w)
	}

	// An update whose baseline can't be read is judged the same way.
	r = watching(t)
	r.reader.answer = func(_ string, w releasehealth.Window) (releasehealth.Health, error) {
		if w.End.Equal(depStart) {
			return releasehealth.Health{Reason: "No data before the release."}, nil
		}
		return healthy(), nil
	}
	if w = r.tickAt(t, firstJudged); w.Services[0].Verdict != string(releasehealth.Pass) {
		t.Fatalf("watch %+v", w)
	}
}

// A tick that planned the rollback but lost the watch's save before it
// recorded the plan, or overlapped another tick, leaves a plan for the
// watch: the next tick takes it up instead of planning another.
func TestTick_takesUpARollbackPlanAlreadyMade(t *testing.T) {
	r := watching(t)
	w := r.store.only(t)
	w.Status, w.Verdict, w.Reason = StatusRollingBack, string(releasehealth.Fail), "api: "+failing
	if err := r.store.Save(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	r.rollbacks.status = planning.StatusReady
	if _, err := r.rollbacks.CreateAutomaticRollback(t.Context(), "org", "dep_1", planning.AutomaticRollbackInput{Trigger: planning.TriggerWatch, Reason: w.Reason, WatchID: w.ID}); err != nil {
		t.Fatal(err)
	}
	r.tickAt(t, firstJudged)
	r.tickAt(t, firstJudged+time.Minute)
	w = r.tickAt(t, firstJudged+2*time.Minute)
	if len(r.rollbacks.created) != 1 || !slices.Equal(r.rollbacks.approved, []string{"plan_rb1"}) || w.RollbackPlanID != "plan_rb1" || w.RollbackApprovedAt == nil {
		t.Fatalf("created %d, approved %v, watch %+v", len(r.rollbacks.created), r.rollbacks.approved, w)
	}
}

// Planning refuses a rollback when a newer release started after the
// tick judged: that release supersedes the watch.
func TestTick_aNewerReleaseThatStartsMidTickSupersedes(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.createErr = planning.ErrReleaseRunning
	w := r.tickAt(t, firstJudged)
	if w.Status != StatusSuperseded || !w.Unverified || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}

	r = watching(t)
	r.candidates(failed)
	r.rollbacks.createErr = fmt.Errorf("%w: deployment dep_1 isn't the newest in production; roll back the newest release first", planning.ErrInvalid)
	r.rollbacks.onCreate = func() {
		r.history.push(planning.ReleaseRecord{DeploymentID: "dep_2", PlanID: "plan_2", Kind: planning.KindRelease, Status: deployments.StatusSucceeded, StartedAt: finished.Add(4 * time.Minute)})
	}
	w = r.tickAt(t, firstJudged)
	if w.Status != StatusSuperseded || !w.Unverified || w.Reason != "A newer release, dep_2, started before the watch finished, so this one isn't judged or rolled back." {
		t.Fatalf("watch %+v", w)
	}
}

// A tick works under a deadline inside the lease Due claims with, so the
// next minute's tick never takes up a watch this one still works on.
func TestTick_finishesInsideTheClaimLease(t *testing.T) {
	r := watching(t)
	before := time.Now()
	r.tickAt(t, firstJudged)
	r.reader.mu.Lock()
	deadlines := append([]time.Time(nil), r.reader.deadlines...)
	r.reader.mu.Unlock()
	if len(deadlines) == 0 {
		t.Fatal("nothing was read")
	}
	for _, d := range deadlines {
		if d.IsZero() || d.After(before.Add(tickBudget).Add(time.Second)) || tickBudget >= claimAfter {
			t.Fatalf("read with deadline %v, budget %s, lease %s", d, tickBudget, claimAfter)
		}
	}
}

// An approved rollback whose deployment never shows up settles an hour
// after the watch's bound.
func TestTick_aRollbackThatNeverStartsFails(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.status = planning.StatusApproved
	r.tickAt(t, firstJudged)
	if w := r.tickAt(t, watchBounded+time.Hour-time.Second); w.Status != StatusRollingBack {
		t.Fatalf("watch %+v before the hour", w)
	}
	w := r.tickAt(t, watchBounded+time.Hour)
	if w.Status != StatusRollbackFailed || w.Reason != "the rollback deployment never started" || w.SettledAt == nil {
		t.Fatalf("watch %+v", w)
	}
}

// Without deployment:create the automation can't plan the rollback, and
// the reason says a person has to.
func TestTick_automationThatCantCreateDeployments(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.createErr = fmt.Errorf("%w (deployment:create)", security.ErrForbidden)
	w := r.tickAt(t, firstJudged)
	if w.Status != StatusRollbackBlocked || w.Reason != "Loomplane automation can't create deployments in this project; roll the release back yourself" {
		t.Fatalf("watch %+v", w)
	}
}

// The breach is audited once, when the rollback is planned, even if the
// save that records the plan is lost; the tick that takes the plan up
// doesn't audit it again.
func TestTick_auditsABreachOnce(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	lost := false
	r.store.failSave = func(w *Watch) error {
		if w.RollbackPlanID != "" && !lost {
			lost = true
			return errors.New("connection reset")
		}
		return nil
	}
	r.clock.set(finished.Add(firstJudged))
	if err := r.svc.Tick(t.Context()); err == nil {
		t.Fatal("the lost save was swallowed")
	}
	if e := r.audit.find("release_watch.breach"); e == nil || e.Attributes["rollback_plan_id"] != "plan_rb1" {
		t.Fatalf("the breach went unaudited with its save: %v", r.audit.actions())
	}
	w := r.tickAt(t, firstJudged+time.Minute)
	if w.RollbackPlanID != "plan_rb1" || len(r.rollbacks.created) != 1 {
		t.Fatalf("watch %+v, created %d", w, len(r.rollbacks.created))
	}
	r.tickAt(t, firstJudged+2*time.Minute)
	breaches := 0
	for _, a := range r.audit.actions() {
		if a == "release_watch.breach" {
			breaches++
		}
	}
	if breaches != 1 {
		t.Fatalf("audited %d breaches: %v", breaches, r.audit.actions())
	}
}

// Loomplane approved the rollback, then lost the save that says so: the
// next tick finds the plan approved and follows it without approving
// again.
func TestTick_followsAnApprovalWhoseSaveWasLost(t *testing.T) {
	r := watching(t)
	r.candidates(failed)
	r.rollbacks.status = planning.StatusReady
	r.tickAt(t, firstJudged)
	r.store.failSave = func(w *Watch) error {
		if w.RollbackApprovedAt != nil {
			return errors.New("connection reset")
		}
		return nil
	}
	r.clock.set(finished.Add(firstJudged + time.Minute))
	if err := r.svc.Tick(t.Context()); err == nil {
		t.Fatal("the lost save was swallowed")
	}
	r.store.failSave = nil
	if w := r.store.only(t); w.RollbackApprovedAt != nil || len(r.rollbacks.approved) != 1 {
		t.Fatalf("watch %+v, approved %v", w, r.rollbacks.approved)
	}
	// The decision reached the plan's workflow.
	r.plans.setStatus("plan_rb1", planning.StatusApproved)
	r.deployments.put(&deployments.Deployment{ID: "dep_rb", PlanID: "plan_rb1", Status: deployments.StatusSucceeded})
	w := r.tickAt(t, firstJudged+2*time.Minute)
	if w.Status != StatusRolledBack || len(r.rollbacks.approved) != 1 {
		t.Fatalf("watch %+v, approved %v", w, r.rollbacks.approved)
	}
}
