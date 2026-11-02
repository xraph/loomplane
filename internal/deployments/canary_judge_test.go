package deployments

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// healthKey names one reading: a binding's revision.
type healthKey struct{ binding, revision string }

type healthRead struct {
	org string
	healthKey
	w releasehealth.Window
}

// fakeHealth answers each reading from a table, and keeps what it was asked.
type fakeHealth struct {
	mu    sync.Mutex
	by    map[healthKey]releasehealth.Health
	errs  map[healthKey]error
	reads []healthRead
}

func (f *fakeHealth) Health(_ context.Context, orgID, bindingID, revision string, w releasehealth.Window) (releasehealth.Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := healthKey{bindingID, revision}
	f.reads = append(f.reads, healthRead{orgID, k, w})
	if err := f.errs[k]; err != nil {
		return releasehealth.Health{}, err
	}
	return f.by[k], nil
}

func healthy(requests, errs float64) releasehealth.Health {
	return releasehealth.Health{Requests: requests, Errors: errs, LatencyMS: 100, Statistic: "p95", Available: true}
}

// both sets a service's baseline and candidate readings.
func (f *fakeHealth) both(name string, baseline, candidate releasehealth.Health) {
	f.by[healthKey{"bind_" + name, name + "-00001"}] = baseline
	f.by[healthKey{"bind_" + name, name + "-00002"}] = candidate
}

var heldWindow = releasehealth.Window{
	Start: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 26, 12, 10, 0, 0, time.UTC),
}

// judgeSetup starts a canary with every candidate up and 5% on step 0,
// and a reader the judgement reads through.
func judgeSetup(t *testing.T, withWebToo bool) (*Runner, *memStore, *fakeHealth, string) {
	t.Helper()
	plan := canaryPlan()
	if withWebToo {
		plan = withWeb(plan)
	}
	r, store, _, _ := newCanaryRunner(t, plan)
	h := &fakeHealth{by: map[healthKey]releasehealth.Health{}, errs: map[healthKey]error{}}
	r.health = h
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(t.Context(), plan.OrgID, id, 0, 5); err != nil {
		t.Fatal(err)
	}
	return r, store, h, id
}

func TestJudgeActivityReadsTheHeldWindow(t *testing.T) {
	r, _, h, id := judgeSetup(t, true)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.both("web", healthy(1000, 1), healthy(300, 1))
	if _, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow); err != nil {
		t.Fatal(err)
	}
	want := map[healthKey]bool{
		{"bind_api", "api-00001"}: true, {"bind_api", "api-00002"}: true,
		{"bind_web", "web-00001"}: true, {"bind_web", "web-00002"}: true,
	}
	if len(h.reads) != len(want) {
		t.Fatalf("%d reads, want %d: %+v", len(h.reads), len(want), h.reads)
	}
	for _, read := range h.reads {
		if !want[read.healthKey] || read.w != heldWindow || read.org != "org_1" {
			t.Fatalf("unexpected read %+v", read)
		}
		delete(want, read.healthKey)
	}
}

func TestJudgeActivityPass(t *testing.T) {
	r, store, h, id := judgeSetup(t, true)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.both("web", healthy(1000, 1), healthy(300, 1))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 2, heldWindow)
	if err != nil || j.Verdict != "pass" {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	vs, _ := store.Verdicts(t.Context(), "org_1", id)
	if len(vs) != 2 {
		t.Fatalf("%d verdicts", len(vs))
	}
	for _, v := range vs {
		if v.Step != 0 || v.Attempt != 2 || v.Weight != 5 || v.Verdict != releasehealth.Pass ||
			v.Thresholds != releasehealth.DefaultThresholds() || v.WindowStart != heldWindow.Start || v.WindowEnd != heldWindow.End ||
			v.ID != VerdictID(id, 0, v.ActionID, 0, 2) || v.ProjectID != "proj_1" || v.DecidedAt.IsZero() || v.Candidate.Requests != 300 {
			t.Fatalf("verdict %+v", v)
		}
	}
}

func TestJudgeActivityAnyFailFails(t *testing.T) {
	r, _, h, id := judgeSetup(t, true)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.both("web", healthy(1000, 1), healthy(300, 60))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || j.Verdict != "fail" || !strings.HasPrefix(j.Reason, "web: ") {
		t.Fatalf("judgement %+v, %v", j, err)
	}
}

func TestJudgeActivityInconclusiveBeatsPass(t *testing.T) {
	r, _, h, id := judgeSetup(t, true)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.both("web", healthy(1000, 1), healthy(12, 0))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || j.Verdict != "inconclusive" || !strings.HasPrefix(j.Reason, "web: ") {
		t.Fatalf("judgement %+v, %v", j, err)
	}
}

func TestJudgeActivityUnavailableIsInconclusive(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	reason := "Give the service account the Monitoring Viewer role to judge a canary."
	h.both("api", healthy(1000, 1), releasehealth.Health{Reason: reason})
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || j.Verdict != "inconclusive" || !strings.Contains(j.Reason, reason) {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	if vs, _ := store.Verdicts(t.Context(), "org_1", id); len(vs) != 1 || vs[0].Candidate.Reason != reason {
		t.Fatalf("verdicts %+v", vs)
	}
}

func TestJudgeActivityReaderErrorIsInconclusive(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.errs[healthKey{"bind_api", "api-00002"}] = errors.New("connection reset")
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	want := "Loomplane couldn't read api's metrics: connection reset"
	if err != nil || j.Verdict != "inconclusive" || !strings.Contains(j.Reason, want) {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	if vs, _ := store.Verdicts(t.Context(), "org_1", id); len(vs) != 1 || vs[0].Verdict != releasehealth.Inconclusive {
		t.Fatalf("verdicts %+v", vs)
	}
}

func TestJudgeAtFullWeightUsesTheLastBaseline(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	if err := store.RecordVerdict(t.Context(), &CanaryVerdict{OrgID: "org_1", DeploymentID: id, ActionID: "service:api", Service: "api",
		Step: 2, Weight: 50, Verdict: releasehealth.Pass, Baseline: healthy(900, 0.9), Candidate: healthy(900, 1)}); err != nil {
		t.Fatal(err)
	}
	h.both("api", healthy(0, 0), healthy(500, 2))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 3, 0, heldWindow)
	if err != nil || j.Verdict != "pass" || !strings.Contains(j.Reason, "baseline from 50%") {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	vs, _ := store.Verdicts(t.Context(), "org_1", id)
	last := vs[len(vs)-1]
	if last.Step != 3 || last.Weight != 100 || last.Baseline.Requests != 900 {
		t.Fatalf("verdict %+v", last)
	}
}

func TestJudgeActivityIsIdempotent(t *testing.T) {
	r, store, h, id := judgeSetup(t, true)
	h.both("api", healthy(1000, 1), healthy(300, 1))
	h.both("web", healthy(1000, 1), healthy(300, 60))
	first, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 1, heldWindow)
	if err != nil {
		t.Fatal(err)
	}
	// The metrics changed since; the stored verdicts answer.
	h.both("web", healthy(1000, 1), healthy(300, 1))
	second, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 1, heldWindow)
	if err != nil || second != first {
		t.Fatalf("second %+v, %v; first %+v", second, err, first)
	}
	if vs, _ := store.Verdicts(t.Context(), "org_1", id); len(vs) != 2 {
		t.Fatalf("%d verdicts", len(vs))
	}
}

func TestJudgeSkipsAServiceAnEarlierAttemptPromoted(t *testing.T) {
	r, store, h, id := judgeSetup(t, true)
	h.both("web", healthy(1000, 1), healthy(300, 1))
	api := getDeployment(t, store, id).Step("service:api")
	api.Status = StepSucceeded
	if err := store.UpdateStep(t.Context(), "org_1", id, *api); err != nil {
		t.Fatal(err)
	}
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 1, heldWindow)
	if err != nil || j.Verdict != "pass" {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	if vs, _ := store.Verdicts(t.Context(), "org_1", id); len(vs) != 1 || vs[0].ActionID != "service:web" {
		t.Fatalf("verdicts %+v", vs)
	}
}

func TestRevertAfterAPromotionIsPromoteFailed(t *testing.T) {
	plan := withWeb(canaryPlan())
	r, store, _, _ := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(t.Context(), plan.OrgID, id, 0, 5); err != nil {
		t.Fatal(err)
	}
	api := getDeployment(t, store, id).Step("service:api")
	api.Status = StepSucceeded
	if err = store.UpdateStep(t.Context(), plan.OrgID, id, *api); err != nil {
		t.Fatal(err)
	}
	if err = r.CanaryRevert(t.Context(), plan.OrgID, id, "The canary failed at 5%: web: too slow"); err != nil {
		t.Fatal(err)
	}
	d := getDeployment(t, store, id)
	if d.Canary.Status != CanaryPromoteFailed || d.Step("service:api").Status != StepSucceeded || d.Step("service:web").Status != StepFailed {
		t.Fatalf("canary %s, api %s, web %s", d.Canary.Status, d.Step("service:api").Status, d.Step("service:web").Status)
	}
	if c := d.Canary; c.Weight != 0 {
		t.Fatalf("weight %d after the revert", c.Weight)
	}
	if err = r.CanaryRevert(t.Context(), plan.OrgID, id, "again"); err != nil {
		t.Fatalf("a repeated revert: %v", err)
	}
}

func TestFinishAfterAPromotionSettlesFromWhatsLeft(t *testing.T) {
	promoteAPI := func(t *testing.T, store *memStore, id string) {
		t.Helper()
		api := getDeployment(t, store, id).Step("service:api")
		api.Status = StepSucceeded
		if err := store.UpdateStep(t.Context(), "org_1", id, *api); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("split traffic is a failed revert", func(t *testing.T) {
		plan := withWeb(canaryPlan())
		r, store, _, _ := newCanaryRunner(t, plan)
		id, err := startCanary(t, r, plan)
		if err != nil {
			t.Fatal(err)
		}
		promoteAPI(t, store, id)
		if err = r.Finish(t.Context(), plan.OrgID, id, "The workflow stopped"); err != nil {
			t.Fatal(err)
		}
		if c := getDeployment(t, store, id).Canary; c.Status != CanaryRevertFailed {
			t.Fatalf("canary %s", c.Status)
		}
	})
	t.Run("nothing up is a failed promotion", func(t *testing.T) {
		plan := withWeb(canaryPlan())
		r, store, _, _ := newCanaryRunner(t, plan)
		id, err := startCanary(t, r, plan)
		if err != nil {
			t.Fatal(err)
		}
		promoteAPI(t, store, id)
		c := getDeployment(t, store, id).Canary
		c.service("service:web").Candidate = ""
		c.Status, c.Step, c.Weight = CanaryRamping, 0, 5
		if err = store.SetCanary(t.Context(), plan.OrgID, id, c); err != nil {
			t.Fatal(err)
		}
		if err = r.Finish(t.Context(), plan.OrgID, id, "The workflow stopped"); err != nil {
			t.Fatal(err)
		}
		if c = getDeployment(t, store, id).Canary; c.Status != CanaryPromoteFailed || c.Weight != 0 {
			t.Fatalf("canary %s at %d%%", c.Status, c.Weight)
		}
	})
}

// racingStore has another attempt's verdict in place by the time the
// judgement records its own.
type racingStore struct {
	*memStore
	other CanaryVerdict
}

func (s *racingStore) RecordVerdict(ctx context.Context, v *CanaryVerdict) error {
	if v.ActionID == s.other.ActionID {
		o := s.other
		o.OrgID, o.DeploymentID, o.Retry, o.Step, o.Attempt = v.OrgID, v.DeploymentID, v.Retry, v.Step, v.Attempt
		if err := s.memStore.RecordVerdict(ctx, &o); err != nil {
			return err
		}
	}
	return s.memStore.RecordVerdict(ctx, v)
}

func TestJudgeAnswersWithTheStoredVerdict(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	r.store = &racingStore{memStore: store, other: CanaryVerdict{ActionID: "service:api", Service: "api", Verdict: releasehealth.Fail, Reason: "too slow"}}
	h.both("api", healthy(1000, 1), healthy(300, 1))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || j.Verdict != "fail" || j.Reason != "api: too slow" {
		t.Fatalf("judgement %+v, %v", j, err)
	}
}

func TestJudgeAfterARetryJudgesAfresh(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	h.both("api", healthy(1000, 1), healthy(300, 60))
	first, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || first.Verdict != "fail" {
		t.Fatalf("first %+v, %v", first, err)
	}
	if err = r.Finish(t.Context(), "org_1", id, "The canary failed"); err != nil {
		t.Fatal(err)
	}
	if ok, rerr := store.Reopen(t.Context(), "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v %v", ok, rerr)
	}
	ctx := orchestration.CanaryContext(t.Context())
	// Start the candidate again, as run does on the retry.
	if err = r.Run(ctx, "org_1", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(ctx, "org_1", id, 0, 5); err != nil {
		t.Fatal(err)
	}
	h.both("api", healthy(1000, 1), healthy(300, 1))
	second, err := r.CanaryJudge(ctx, "org_1", id, 0, 0, heldWindow)
	if err != nil || second.Verdict != "pass" {
		t.Fatalf("second %+v, %v", second, err)
	}
	vs, _ := store.Verdicts(t.Context(), "org_1", id)
	if len(vs) != 2 || vs[0].Retry != 0 || vs[1].Retry != 1 || vs[1].Verdict != releasehealth.Pass {
		t.Fatalf("verdicts %+v", vs)
	}
}

func TestJudgeFallbackOnlyFromAnEarlierStepsOwnBaseline(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	rec := func(step, weight int, baseline releasehealth.Health, fallback bool) {
		t.Helper()
		if err := store.RecordVerdict(t.Context(), &CanaryVerdict{OrgID: "org_1", DeploymentID: id, ActionID: "service:api", Service: "api",
			Step: step, Weight: weight, Verdict: releasehealth.Pass, Baseline: baseline, BaselineFallback: fallback}); err != nil {
			t.Fatal(err)
		}
	}
	rec(1, 25, healthy(800, 1), false)
	rec(2, 50, healthy(900, 1), true) // substituted: it isn't a reading of step 2
	h.both("api", healthy(0, 0), healthy(500, 2))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 3, 1, heldWindow)
	if err != nil || j.Verdict != "pass" || !strings.Contains(j.Reason, "baseline from 25%") {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	vs, _ := store.Verdicts(t.Context(), "org_1", id)
	if last := vs[len(vs)-1]; !last.BaselineFallback || last.Baseline.Requests != 800 || last.Attempt != 1 {
		t.Fatalf("verdict %+v", last)
	}
}

func TestJudgeWithoutABaselineRevisionIsInconclusive(t *testing.T) {
	r, store, h, id := judgeSetup(t, false)
	c := getDeployment(t, store, id).Canary
	c.service("service:api").Baseline = ""
	if err := store.SetCanary(t.Context(), "org_1", id, c); err != nil {
		t.Fatal(err)
	}
	h.both("api", healthy(1000, 1), healthy(300, 1))
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 0, heldWindow)
	if err != nil || j.Verdict != "inconclusive" {
		t.Fatalf("judgement %+v, %v", j, err)
	}
	for _, read := range h.reads {
		if read.revision == "" {
			t.Fatalf("read the whole service: %+v", read)
		}
	}
}

func TestJudgeWithEveryServicePromotedPasses(t *testing.T) {
	r, store, _, id := judgeSetup(t, false)
	api := getDeployment(t, store, id).Step("service:api")
	api.Status = StepSucceeded
	if err := store.UpdateStep(t.Context(), "org_1", id, *api); err != nil {
		t.Fatal(err)
	}
	j, err := r.CanaryJudge(t.Context(), "org_1", id, 0, 1, heldWindow)
	if err != nil || j.Verdict != "pass" || j.Reason != "every service is already promoted" {
		t.Fatalf("judgement %+v, %v", j, err)
	}
}
