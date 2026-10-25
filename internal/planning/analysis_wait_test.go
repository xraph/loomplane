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

// stagingService is a staging service's binding, built from repo at commitSHA.
func stagingService(t *testing.T, name, repo, commitSHA string) *Binding {
	t.Helper()
	config, err := json.Marshal(ServiceConfig{Repo: repo, CommitSHA: commitSHA})
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "bnd_" + name, EnvironmentID: envStaging, Subject: ServiceSubject, Name: name, Config: config, CommitSHA: commitSHA}
}

// unrecordedWorld is promotionWorld where staging's release of api wasn't
// recorded, but its api service runs bbb. Analyses are queued through a
// fake, and a fake clock does the waiting.
func unrecordedWorld(t *testing.T) (*promoWorld, *fakeCommits, *fakeClock) {
	t.Helper()
	w := promotionWorld(t)
	w.store.deployed[envStaging] = w.store.deployed[envStaging][1:] // web only
	w.store.bindings = map[string][]*Binding{envStaging: {stagingService(t, "api", "api", bbb)}}
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
}

func TestCreateFrom_refusesWhenSourceBindingsDisagree(t *testing.T) {
	t.Parallel()
	w, commits, _ := unrecordedWorld(t)
	w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "worker", "api", ccc))
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
	w.store.bindings[envStaging] = append(w.store.bindings[envStaging], stagingService(t, "web", "web", ccc))
	plan := w.promotePlanning(t)
	if len(commits.calls) != 2 || len(plan.Sources) != 2 || plan.Sources[1].CommitSHA != ccc || plan.Sources[1].FromPlanID != "" {
		t.Fatalf("calls %q pins %+v", commits.calls, pins(plan.Sources))
	}

	// Bindings without a commit, or of a repository the project doesn't
	// have, run nothing.
	w.store.bindings[envStaging] = []*Binding{stagingService(t, "api", "api", ""), stagingService(t, "old", "old", bbb)}
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
	w.store.bindings[envStaging] = []*Binding{stagingService(t, "api", "api", ccc)}
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
