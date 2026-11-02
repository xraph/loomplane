package planning

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/discovery"
)

var (
	bbb = strings.Repeat("b", 40)
	ccc = strings.Repeat("c", 40)
)

// fakeCommits analyzes commits in memory: each AnalyzeCommit queues anl_q,
// whose status the test then sets.
type fakeCommits struct {
	mu       sync.Mutex
	calls    []string // "org source commit creator"
	analyses map[string]*discovery.Analysis
	reads    int
}

func (f *fakeCommits) AnalyzeCommit(_ context.Context, orgID string, src *discovery.Source, commitSHA, createdBy string) (*discovery.Analysis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join([]string{orgID, src.ID, commitSHA, createdBy}, " "))
	id := "anl_q"
	if src.ID != "src_api" {
		id += "_" + src.Alias
	}
	a := &discovery.Analysis{ID: id, OrgID: orgID, SourceID: src.ID, Source: src.Alias, Status: discovery.StatusQueued, Ref: commitSHA}
	f.analyses[a.ID] = a
	c := *a
	return &c, nil
}

func (f *fakeCommits) Analysis(_ context.Context, _, id string) (*discovery.Analysis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	a, ok := f.analyses[id]
	if !ok {
		return nil, discovery.ErrNotFound
	}
	c := *a
	return &c, nil
}

// set finishes anl_q, the analysis queued for api.
func (f *fakeCommits) set(status, commitSHA, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.analyses["anl_q"]
	a.Status, a.CommitSHA, a.Error = status, commitSHA, reason
}

// fakeClock is a clock that waiting moves.
type fakeClock struct {
	mu    sync.Mutex
	at    time.Time
	waits []time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.at = c.at.Add(d)
	return nil
}

// stagingService is a staging service's binding, built from repo at
// commitSHA and deployed by planID.
func stagingService(t *testing.T, name, repo, commitSHA, planID string) *Binding {
	t.Helper()
	config, err := json.Marshal(ServiceConfig{Repo: repo, CommitSHA: commitSHA})
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "bnd_" + name, EnvironmentID: envStaging, Subject: ServiceSubject, Name: name, Config: config, CommitSHA: commitSHA, PlanID: planID}
}

// unrecordedWorld is promotionWorld where staging's release of api wasn't
// recorded, but its api service runs bbb, which plan_s0 deployed from
// services/api on release-api. Analyses are queued through a fake, and a
// fake clock does the waiting.
func unrecordedWorld(t *testing.T) (*promoWorld, *fakeCommits, *fakeClock) {
	t.Helper()
	w := promotionWorld(t)
	w.store.deployed[envStaging] = w.store.deployed[envStaging][1:] // web only
	w.store.plans["plan_s0"] = &Plan{
		ID: "plan_s0", OrgID: "org", ProjectID: "proj", EnvironmentID: envStaging, Environment: "staging", Status: StatusApproved,
		Sources: []PlanSource{{SourceID: "src_api", Alias: "api", Repository: "acme/api", Root: "services/api", Ref: "release-api", CommitSHA: bbb, InScope: true}},
	}
	w.store.bindings = map[string][]*Binding{envStaging: {stagingService(t, "api", "api", bbb, "plan_s0")}}
	commits := &fakeCommits{analyses: map[string]*discovery.Analysis{}}
	clock := &fakeClock{at: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	w.svc.commits, w.svc.now, w.svc.sleep = commits, clock.now, clock.sleep
	return w, commits, clock
}

func (w *promoWorld) promotePlanning(t *testing.T) *Plan {
	t.Helper()
	plan, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusPlanning {
		t.Fatalf("plan %s: %s", plan.Status, plan.Error)
	}
	return plan
}

func TestCreateFrom_queuesAnAnalysisForACommitWithoutOne(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	if want := []string{"org src_api " + bbb + " usr"}; !reflect.DeepEqual(commits.calls, want) {
		t.Fatalf("AnalyzeCommit calls %q, want %q", commits.calls, want)
	}
	want := []pin{{"api", "anl_q", bbb, "", true}, {"web", "anl_1", "aaa", "plan_s1", true}}
	if got := pins(plan.Sources); !reflect.DeepEqual(got, want) {
		t.Fatalf("pins %+v, want %+v", got, want)
	}
	// The root and branch are the deploying plan's, not the source's own.
	if api := plan.Sources[0]; api.Root != "services/api" || api.Ref != "release-api" {
		t.Fatalf("api root %q ref %q", api.Root, api.Ref)
	}
}

func TestCreateFrom_refusesABindingPinWithoutItsDeployingPlan(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*promoWorld)
		want  string
	}{
		{
			name:  "no plan named",
			setup: func(w *promoWorld) { w.store.bindings[envStaging][0].PlanID = "" },
			want:  "service api runs api at bbbbbbb, but its binding names no plan",
		},
		{
			name:  "plan gone",
			setup: func(w *promoWorld) { delete(w.store.plans, "plan_s0") },
			want:  "plan plan_s0, which deployed api at bbbbbbb, is gone",
		},
		{
			name:  "plan doesn't list the source",
			setup: func(w *promoWorld) { w.store.plans["plan_s0"].Sources[0].SourceID = "src_other" },
			want:  "plan plan_s0, which deployed api at bbbbbbb, doesn't list it",
		},
		{
			name: "plans disagree",
			setup: func(w *promoWorld) {
				// plan_s1 deployed api from the repository root.
				w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "worker", "api", bbb, "plan_s1"))
			},
			want: "plans plan_s0 and plan_s1, which deployed api at bbbbbbb, disagree on its root and branch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w, commits, _ := unrecordedWorld(t)
			tt.setup(w)
			_, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"})
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
			if len(commits.calls) != 0 {
				t.Fatalf("analyzed %q", commits.calls)
			}
		})
	}
}

func TestCreateFrom_bindingPinsAgreeAcrossPlans(t *testing.T) {
	t.Parallel()
	w, _, _ := unrecordedWorld(t)
	w.store.plans["plan_s2"] = clonePlan(w.store.plans["plan_s0"])
	w.store.plans["plan_s2"].ID = "plan_s2"
	w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "worker", "api", bbb, "plan_s2"))
	plan := w.promotePlanning(t)
	if api := plan.Sources[0]; api.Root != "services/api" || api.Ref != "release-api" || api.FromPlanID != "" {
		t.Fatalf("api %+v", api)
	}
}

func TestCreateFrom_refusesWhenSourceBindingsDisagree(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "worker", "api", ccc, "plan_s0"))
	_, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "bbbbbbb") || !strings.Contains(err.Error(), "ccccccc") {
		t.Fatalf("err %v", err)
	}
	if len(commits.calls) != 0 {
		t.Fatalf("analyzed %q", commits.calls)
	}
}

func TestCreateFrom_needsCommitsToAnalyzeAnUnrecordedCommit(t *testing.T) {
	t.Parallel()
	w, _, _ := unrecordedWorld(t)
	w.svc.commits = nil
	_, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"})
	if !errors.Is(err, ErrNoAnalysis) || !strings.Contains(err.Error(), "api") {
		t.Fatalf("err %v", err)
	}
	// Scoped to web, api is held at what production runs, so nothing
	// needs analyzing.
	plan := w.promote(t, "web")
	want := []pin{{"api", "anl_5", "ccc", "", false}, {"web", "anl_1", "aaa", "plan_s1", true}}
	if got := pins(plan.Sources); !reflect.DeepEqual(got, want) {
		t.Fatalf("pins %+v, want %+v", got, want)
	}
}

func TestCreateFrom_runsWhatOnlyBindingsShow(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	delete(w.store.deployed, envStaging) // nothing recorded at all
	w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "web", "web", ccc, "plan_s1"))
	plan := w.promotePlanning(t)
	if len(commits.calls) != 2 || len(plan.Sources) != 2 || plan.Sources[1].CommitSHA != ccc || plan.Sources[1].FromPlanID != "" {
		t.Fatalf("calls %q pins %+v", commits.calls, pins(plan.Sources))
	}

	// Bindings without a commit, or of a repository the project doesn't
	// have, run nothing.
	w.store.bindings[envStaging] = []*Binding{stagingService(t, "api", "api", "", "plan_s0"), stagingService(t, "old", "old", bbb, "plan_s0")}
	if _, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err %v", err)
	}
}

func TestPlan_waitsForAPinnedAnalysisThenPlans(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	_, err := w.svc.Plan(t.Context(), "org", plan.ID)
	if !errors.Is(err, ErrAnalysisPending) || permanent(err) {
		t.Fatalf("err %v", err)
	}
	if got, _ := w.store.GetPlan(t.Context(), "org", plan.ID); got.Status != StatusPlanning {
		t.Fatalf("status %s", got.Status)
	}
	commits.set(discovery.StatusSucceeded, bbb, "")
	status, err := w.svc.Plan(t.Context(), "org", plan.ID)
	if err != nil || status != StatusReady {
		t.Fatalf("status %s err %v", status, err)
	}
}

func TestPlan_failsWhenThePinnedAnalysisFails(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	commits.set(discovery.StatusFailed, "", "repository not found")
	status, err := w.svc.Plan(t.Context(), "org", plan.ID)
	if err != nil || status != StatusFailed {
		t.Fatalf("status %s err %v", status, err)
	}
	got, _ := w.store.GetPlan(t.Context(), "org", plan.ID)
	if got.Status != StatusFailed || got.Error != "The analysis of api at bbbbbbb: repository not found" {
		t.Fatalf("plan %s: %q", got.Status, got.Error)
	}
}

func TestPlan_failsWhenAnalysisResolvedAnotherCommit(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	commits.set(discovery.StatusSucceeded, ccc, "")
	status, err := w.svc.Plan(t.Context(), "org", plan.ID)
	if err != nil || status != StatusFailed {
		t.Fatalf("status %s err %v", status, err)
	}
	got, _ := w.store.GetPlan(t.Context(), "org", plan.ID)
	if !strings.Contains(got.Error, "resolved ccccccc instead of bbbbbbb") {
		t.Fatalf("error %q", got.Error)
	}
}

func TestPlan_failsWhenThePinnedAnalysisIsGone(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	delete(commits.analyses, "anl_q")
	if status, err := w.svc.Plan(t.Context(), "org", plan.ID); err != nil || status != StatusFailed {
		t.Fatalf("status %s err %v", status, err)
	}
	if got, _ := w.store.GetPlan(t.Context(), "org", plan.ID); !strings.Contains(got.Error, "The analysis of api at bbbbbbb is gone") {
		t.Fatalf("error %q", got.Error)
	}
}

func TestPlan_waitIsBoundedPerAttempt(t *testing.T) {
	t.Parallel()
	w, commits, clock := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	clock.waits, commits.reads = nil, 0
	start := clock.now()
	if _, err := w.svc.Plan(t.Context(), "org", plan.ID); !errors.Is(err, ErrAnalysisPending) {
		t.Fatalf("err %v", err)
	}
	if waited := clock.now().Sub(start); waited > analysisWaitPerAttempt {
		t.Fatalf("waited %s, more than %s", waited, analysisWaitPerAttempt)
	}
	if want := int(analysisWaitPerAttempt / analysisPoll); len(clock.waits) != want || commits.reads != want+1 {
		t.Fatalf("%d waits and %d reads, want %d and %d", len(clock.waits), commits.reads, want, want+1)
	}
	for _, d := range clock.waits {
		if d != analysisPoll {
			t.Fatalf("waited %s at a time, want %s", d, analysisPoll)
		}
	}
	if analysisWaitPerAttempt != 30*time.Second || analysisPoll != 2*time.Second {
		t.Fatalf("wait %s, poll %s", analysisWaitPerAttempt, analysisPoll)
	}
}

func TestPinsMatchSource_readsBindingsForAnUnrecordedSource(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	plan := w.promotePlanning(t)
	commits.set(discovery.StatusSucceeded, bbb, "")
	ok, diffs, err := w.svc.PinsMatchSource(t.Context(), "org", plan)
	if err != nil || !ok || len(diffs) != 0 {
		t.Fatalf("unchanged: %v %q %v", ok, diffs, err)
	}
	w.store.bindings[envStaging] = []*Binding{stagingService(t, "api", "api", ccc, "plan_s0")}
	ok, diffs, err = w.svc.PinsMatchSource(t.Context(), "org", plan)
	if err != nil || ok || len(diffs) != 1 || diffs[0] != "api: staging runs "+ccc+", the plan pins "+bbb {
		t.Fatalf("after api moved: %v %q %v", ok, diffs, err)
	}
	w.store.bindings[envStaging] = nil
	ok, diffs, err = w.svc.PinsMatchSource(t.Context(), "org", plan)
	if err != nil || ok || len(diffs) != 1 || !strings.Contains(diffs[0], "doesn't run it any more") {
		t.Fatalf("after api went: %v %q %v", ok, diffs, err)
	}
}

// A test that stops the clock without making sleep move it still gets its
// plan back from settle, after as many polls as settleWait allows.
func TestSettle_returnsWhenTheClockStops(t *testing.T) {
	w := promotionWorld(t)
	frozen := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	polls := 0
	w.svc.now = func() time.Time { return frozen }
	w.svc.sleep = func(context.Context, time.Duration) error { polls++; return nil }
	plan := w.store.plans["plan_s1"]
	if got := w.svc.settle(t.Context(), plan); got != plan {
		t.Fatalf("settle returned %+v", got)
	}
	if want := int(settleWait / settlePoll); polls != want {
		t.Fatalf("polled %d times, want %d", polls, want)
	}
}
