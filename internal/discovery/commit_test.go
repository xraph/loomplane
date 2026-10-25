package discovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

// commitWorld is a source with a commit analyzer over an in-memory store.
type commitWorld struct {
	store    *memStore
	queue    *queue
	audits   *audits
	src      *Source
	analyzer *CommitAnalyzer
	now      time.Time
}

func newCommitWorld() *commitWorld {
	w := &commitWorld{
		store: newMemStore(), queue: &queue{}, audits: &audits{},
		src: &Source{ID: "src_api", OrgID: org, ProjectID: project, Alias: "api", Repository: "acme/api", Ref: "main"},
		now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}
	w.analyzer = &CommitAnalyzer{Store: w.store, Enqueue: w.queue, Audit: w.audits, Now: func() time.Time { return w.now }}
	return w
}

func (w *commitWorld) add(a *Analysis) {
	a.OrgID, a.ProjectID, a.SourceID, a.Source = org, project, w.src.ID, w.src.Alias
	if a.CreatedAt.IsZero() {
		a.CreatedAt = w.now.Add(-time.Minute)
	}
	_ = w.store.CreateAnalysis(context.Background(), a)
}

func TestCommitAnalyzer_reusesASucceededAnalysisOfTheCommit(t *testing.T) {
	w := newCommitWorld()
	w.add(&Analysis{ID: "anl_1", Status: StatusSucceeded, Ref: "main", CommitSHA: commit})
	a, err := w.analyzer.Analyze(t.Context(), org, w.src, commit, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "anl_1" || len(w.queue.ids) != 0 || len(w.audits.events) != 0 {
		t.Fatalf("analysis %s, queued %v, audits %d", a.ID, w.queue.ids, len(w.audits.events))
	}
}

func TestCommitAnalyzer_reusesOneInFlightForTheCommit(t *testing.T) {
	w := newCommitWorld()
	w.add(&Analysis{ID: "anl_2", Status: StatusQueued, Ref: commit})
	a, err := w.analyzer.Analyze(t.Context(), org, w.src, commit, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "anl_2" || len(w.queue.ids) != 0 {
		t.Fatalf("analysis %s, queued %v", a.ID, w.queue.ids)
	}
}

func TestCommitAnalyzer_queuesOnePinnedToTheCommitEvenWithABranchAnalysisRunning(t *testing.T) {
	w := newCommitWorld()
	w.add(&Analysis{ID: "anl_branch", Status: StatusAnalyzing, Ref: "main", CommitSHA: "fedcba9876543210fedcba9876543210fedcba98"})
	// An earlier analysis of the commit that failed is retried, not reused.
	w.add(&Analysis{ID: "anl_failed", Status: StatusFailed, Ref: commit, CommitSHA: commit, Error: "rate limited"})
	// One stuck for longer than an analysis may block another is stuck.
	w.add(&Analysis{ID: "anl_stuck", Status: StatusQueued, Ref: commit, CreatedAt: w.now.Add(-2 * time.Hour)})
	if _, err := w.store.ActiveAnalysis(t.Context(), org, w.src.ID, w.now.Add(-activeWindow)); err != nil {
		t.Fatalf("the branch analysis should block Start: %v", err)
	}
	a, err := w.analyzer.Analyze(t.Context(), org, w.src, commit, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Ref != commit || a.Status != StatusQueued || a.CreatedBy != "usr_1" || a.SourceID != "src_api" || a.Source != "api" ||
		a.ProjectID != project || a.Repository != "acme/api" || !a.CreatedAt.Equal(w.now) {
		t.Fatalf("analysis %+v", a)
	}
	if len(w.queue.ids) != 1 || w.queue.ids[0] != a.ID {
		t.Fatalf("queued %v, want [%s]", w.queue.ids, a.ID)
	}
	if stored, gerr := w.store.GetAnalysis(t.Context(), org, a.ID); gerr != nil || stored.Ref != commit {
		t.Fatalf("stored %+v %v", stored, gerr)
	}
	if len(w.audits.events) != 1 {
		t.Fatalf("audits %+v", w.audits.events)
	}
	e := w.audits.events[0]
	if e.Action != "analysis.start" || e.ProjectID != project || e.Principal.UserID != "usr_1" || e.Principal.OrgID != org ||
		e.Attributes["for"] != "promotion" || e.Attributes["analysis_id"] != a.ID || e.Attributes["ref"] != commit {
		t.Fatalf("audit %+v", e)
	}

	// Asking again finds the one just queued.
	again, err := w.analyzer.Analyze(t.Context(), org, w.src, commit, "usr_2")
	if err != nil || again.ID != a.ID || len(w.queue.ids) != 1 {
		t.Fatalf("again %+v %v, queued %v", again, err, w.queue.ids)
	}
}

func TestCommitAnalyzer_takesBackAnAnalysisItCouldntEnqueue(t *testing.T) {
	w := newCommitWorld()
	w.queue.fail = errors.New("queue down")
	if _, err := w.analyzer.Analyze(t.Context(), org, w.src, commit, "usr_1"); err == nil {
		t.Fatal("no error")
	}
	if list, _ := w.store.ListSourceAnalyses(t.Context(), org, w.src.ID, 50); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
}

func TestCommitAnalyzer_refusesAnAbbreviatedCommit(t *testing.T) {
	w := newCommitWorld()
	for _, sha := range []string{"aaa1234", "", "main", "0123456789ABCDEF0123456789ABCDEF01234567"} {
		if _, err := w.analyzer.Analyze(t.Context(), org, w.src, sha, "usr_1"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: err %v", sha, err)
		}
	}
	if len(w.queue.ids) != 0 {
		t.Fatalf("queued %v", w.queue.ids)
	}
}

func TestCommitAnalyzer_refusesASourceOfAnotherOrganization(t *testing.T) {
	w := newCommitWorld()
	if _, err := w.analyzer.Analyze(t.Context(), "aorg_other", w.src, commit, "usr_1"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err %v", err)
	}
	w.add(&Analysis{ID: "anl_1", Status: StatusSucceeded, CommitSHA: commit})
	if _, err := w.analyzer.Analysis(t.Context(), "aorg_other", "anl_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Analysis err %v", err)
	}
	if a, err := w.analyzer.AnalyzeCommit(t.Context(), org, w.src, commit, "usr_1"); err != nil || a.ID != "anl_1" {
		t.Fatalf("AnalyzeCommit %+v %v", a, err)
	}
}
