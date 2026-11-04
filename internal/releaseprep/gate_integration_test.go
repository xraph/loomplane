//go:build integration

package releaseprep

import (
	"context"
	"testing"
	"time"
)

// The gate against the real store: only the preparation's own run records
// an evaluation, it records the evidence it read now, and a report a person
// cancelled is never allowed.
func TestGateRecordsOnAStoredReport(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	w := greenWorld()

	r := newReport("org_a", "proj_a", w.production.ID)
	r.FromEnvironmentID, r.RequestedBy = w.staging.ID, "usr_dev"
	mustCreate(t, s, r)
	if ok, err := s.SetPlan(ctx, "org_a", r.ID, w.plan.ID, false); err != nil || !ok {
		t.Fatalf("set plan: %v %v", ok, err)
	}
	preview := w.collect(nil)
	if err := s.SetEvidence(ctx, "org_a", r.ID, w.plan.Revision, preview, Checks(CheckInput{
		Project: w.project, RequesterCanApprove: true, Report: mustGet(t, s, r.ID), Preview: true,
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
