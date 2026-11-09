//go:build integration

package releaseprep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/releasewatch"
)

// The gate against the real store: only the preparation's own run records
// an evaluation, it records the evidence it read now, and a report a person
// cancelled is never allowed.
func TestGateRecordsOnAStoredReport(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	w := greenWorld()
	r := storeReviewing(t, s, w)
	later := collectedAt.Add(time.Minute)
	c := w.collector()
	c.Now = func() time.Time { return later }
	g := &Gate{Store: s, Collector: c, Audit: &fakeAudit{}, Now: func() time.Time { return later }}
	req := (&gateFixture{w: w}).request()

	d, err := g.Evaluate(ctx, req)
	if err != nil || !d.Allowed {
		t.Fatalf("evaluate: %+v %v", d, err)
	}
	got := mustGet(t, s, r.ID)
	if got.Preview || got.Decision != DecisionPromote || got.EvaluatedAt == nil || !got.EvaluatedAt.Equal(later) || len(got.Gates) != 7 {
		t.Fatalf("recorded %+v", got)
	}
	if !got.Evidence.CollectedAt.Equal(later) {
		t.Fatalf("recorded the report's evidence, not what the gate read: %v", got.Evidence.CollectedAt)
	}

	// Another run of the same workspace: not allowed, nothing recorded.
	other := req
	other.TaskID = "atask_chat"
	if od, oerr := g.Evaluate(ctx, other); oerr != nil || od.Allowed {
		t.Fatalf("other run: %+v %v", od, oerr)
	}
	if again := mustGet(t, s, r.ID); again.Decision != DecisionPromote || !again.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("another run's evaluation was recorded: %+v", again)
	}

	// A person cancels it: the next evaluation is refused, not an error.
	if err = s.Settle(ctx, "org_a", r.ID, StateReviewing, StatusCancelled, StateCancelled, "cancelled by a person", "", later); err != nil {
		t.Fatal(err)
	}
	d, err = g.Evaluate(ctx, req)
	if err != nil || d.Allowed || !failedGate(d, GateFromStaging) {
		t.Fatalf("after cancel: %+v %v", d, err)
	}
	if final := mustGet(t, s, r.ID); final.Status != StatusCancelled || final.Decision != DecisionPromote {
		t.Fatalf("cancelled report changed: %+v", final)
	}
}

// storeReviewing stores the world's report as the job leaves it: plan,
// evidence and preview set, then the task, so it's reviewing.
func storeReviewing(t *testing.T, s *PgStore, w *world) *ReleaseReport {
	t.Helper()
	ctx := context.Background()
	r := newReport("org_a", "proj_a", w.production.ID)
	r.FromEnvironmentID, r.RequestedBy = w.staging.ID, "usr_dev"
	mustCreate(t, s, r)
	if ok, err := s.SetPlan(ctx, "org_a", r.ID, w.plan.ID, false); err != nil || !ok {
		t.Fatalf("set plan: %v %v", ok, err)
	}
	preview := w.collect(nil)
	if err := s.SetEvidence(ctx, "org_a", r.ID, w.plan.Revision, preview, PreviewChecks(CheckInput{
		Project: w.project, RequesterCanApprove: true, Report: mustGet(t, s, r.ID),
		Plan: w.plan, From: w.staging, To: w.production, Evidence: preview,
	})); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetTask(ctx, "org_a", r.ID, "atask_1", "run_1"); err != nil || !ok {
		t.Fatalf("set task: %v %v", ok, err)
	}
	if got := mustGet(t, s, r.ID); !got.Preview || got.Status != StatusReviewing || len(got.Gates) != 7 {
		t.Fatalf("prepared report %+v", got)
	}
	return r
}

// What a cloud, a reader or 3c wrote never reaches the realtime events, nor
// the report's gates, state reason or error.
func TestGateWritesNoOutsideTextToEvents(t *testing.T) {
	s, pg := openPg(t)
	ctx := context.Background()
	w := greenWorld()
	r := storeReviewing(t, s, w)

	bad := strings.Join(outsideText, " ")
	w.watches.soak = &releasewatch.Soak{Environment: "staging", Running: true, Duration: 2 * time.Hour, Reasons: []string{bad},
		Services: []releasewatch.SoakService{{Name: "api", Verdict: "fail", Reason: bad}}}
	for _, e := range w.watches.evidence {
		e.Unverified, e.Reason = true, "The watch couldn't decide by its bound: api: "+bad
	}
	w.watches.coverage.Unverified = true
	w.canaries.runs[0].Canary.Status, w.canaries.runs[0].Canary.Reason = deployments.CanaryRolledBack, bad
	w.reports.open = []OpenReport{{ID: "arep_1", Title: bad, Severity: bad}}
	w.plans.deployed["production"][0].CommitSHA = shaC
	w.diffs.err = errors.New(bad)
	w.budgets.err = errors.New(bad)

	g := &Gate{Store: s, Collector: w.collector(), Audit: &fakeAudit{}, Now: func() time.Time { return collectedAt }}
	d, err := g.Evaluate(ctx, (&gateFixture{w: w}).request())
	if err != nil || d.Allowed {
		t.Fatalf("evaluate: %+v %v", d, err)
	}
	got := mustGet(t, s, r.ID)
	if got.Decision != DecisionAsk {
		t.Fatalf("decision %q", got.Decision)
	}
	texts := make([]string, 0, 2+len(got.Gates))
	texts = append(texts, got.StateReason, got.Error)
	for _, gc := range got.Gates {
		texts = append(texts, gc.Detail)
	}
	assertNoOutsideText(t, "the stored report", texts)
	assertNoOutsideText(t, "a release_report event", eventData(t, pg))
}

// eventData is every release_report event's body.
func eventData(t *testing.T, pg *pgdriver.PgDB) []string {
	t.Helper()
	rows, err := pg.Query(context.Background(), `SELECT data::text FROM loomplane_events WHERE type LIKE 'release_report.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no release_report events were emitted")
	}
	return out
}

// Org B's report for a plan with the same ID never answers for org A.
func TestGateReadsOnlyItsOrganizationsReport(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	w := greenWorld()
	theirs := newReport("org_b", "proj_b", "env_prd_b")
	mustCreate(t, s, theirs)
	if ok, err := s.SetPlan(ctx, "org_b", theirs.ID, w.plan.ID, false); err != nil || !ok {
		t.Fatalf("set plan: %v %v", ok, err)
	}
	if ok, err := s.SetTask(ctx, "org_b", theirs.ID, "atask_1", "run_1"); err != nil || !ok {
		t.Fatalf("set task: %v %v", ok, err)
	}
	g := &Gate{Store: s, Collector: w.collector(), Audit: &fakeAudit{}, Now: func() time.Time { return collectedAt }}
	d, err := g.Evaluate(ctx, (&gateFixture{w: w}).request())
	if err != nil || d.Allowed || len(d.Failed) != 1 || d.Failed[0] != noReport {
		t.Fatalf("org A: %+v %v", d, err)
	}
	if got, err := s.Get(ctx, "org_b", theirs.ID); err != nil || got.EvaluatedAt != nil {
		t.Fatalf("org B's report was evaluated: %+v %v", got, err)
	}
}
