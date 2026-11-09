//go:build integration

package releaseprep

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	dispatchengine "github.com/xraph/dispatch/engine"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
)

// newPgJobRig is the job's rig over the real report store and the real
// notifications outbox.
func newPgJobRig(t *testing.T) *pgRig {
	t.Helper()
	store, pg := openPg(t)
	r := newJobRig(t)
	r.svc = NewService(Deps{
		Store: store, Sources: r.w.sources(), Runs: r.runs, Requests: r.requests,
		Jobs:   func() *dispatchengine.Engine { return r.jobs },
		Safety: &agentsafety.Scanner{Engine: r.shield}, Binder: &agentscope.Binder{Tenants: r.tenants},
		Teams: r.teams, PlanStore: r.planStore, Places: r.places, Audit: r.audit,
		Now: func() time.Time { return rigNow },
	})
	r.svc.planPoll, r.svc.planPollFor = time.Millisecond, time.Second
	return &pgRig{rig: r, pg: pg}
}

func (r *pgRig) stored(t *testing.T, id string) *ReleaseReport {
	t.Helper()
	got, err := r.svc.d.Store.Get(context.Background(), "org_a", id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPgJobPreparesAndStartsTheReview(t *testing.T) {
	r := newPgJobRig(t)
	r.w.plans.statuses["plan_a"] = []string{planning.StatusPlanning, planning.StatusReady}
	rep, payload := r.prepared(t, dev)
	if err := r.svc.RunJob(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	got := r.stored(t, rep.ID)
	if got.Status != StatusReviewing || got.State != StateReviewing || got.PlanID != "plan_a" || got.PlanRevision != "rev_1" ||
		got.TaskID != "atask_1" || got.RunID != "arun_1" || got.Evidence == nil || len(got.Gates) != 7 || !got.Preview {
		t.Fatalf("report %+v", got)
	}
	starts := r.runs.started()
	if len(starts) != 1 || starts[0].ReleasePlanID != "plan_a" || starts[0].ReleaseRevision != "rev_1" ||
		starts[0].Metadata["release_report_id"] != rep.ID || len(starts[0].Metadata) != 1 {
		t.Fatalf("starts %+v", starts)
	}
	// A retry finds the review started and starts nothing.
	if err := r.svc.RunJob(context.Background(), payload); err != nil || len(r.runs.started()) != 1 || r.w.plans.creates != 1 {
		t.Fatalf("retry: %v, %d starts, %d creates", err, len(r.runs.started()), r.w.plans.creates)
	}
}

func TestPgJobCancelsATaskStartedAfterTheReportWasCancelled(t *testing.T) {
	r := newPgJobRig(t)
	rep, payload := r.prepared(t, dev)
	start := r.runs.startFn
	r.runs.startFn = func(in agentruns.StartInput) (*agentruns.Task, error) {
		if _, err := r.svc.Cancel(context.Background(), dev, rep.ID); err != nil {
			t.Errorf("cancel: %v", err)
		}
		return start(in)
	}
	if err := r.svc.RunJob(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	got := r.stored(t, rep.ID)
	if got.Status != StatusCancelled || got.TaskID != "" {
		t.Fatalf("report %s task %q", got.Status, got.TaskID)
	}
	if !slices.Equal(r.runs.cancelTasks, []string{"org_a atask_1"}) {
		t.Fatalf("cancelled %v", r.runs.cancelTasks)
	}
}

// Review Focus 5 in the job: the budget runs out as the review would
// start. The report settles stopped with Loomplane's words in its row and
// its event, the requester and the team are told once, and the plan is
// left ready for a person.
func TestPgJobLeavesThePlanForAPersonWhenTheBudgetRunsOut(t *testing.T) {
	ctx := context.Background()
	r := newPgJobRig(t)
	r.runs.startFn = func(agentruns.StartInput) (*agentruns.Task, error) { return nil, agentsettings.ErrBudgetExhausted }
	rep, payload := r.prepared(t, dev)
	if err := r.svc.RunJob(ctx, payload); err != nil {
		t.Fatal(err)
	}
	got := r.stored(t, rep.ID)
	want := reviewStopped(agentsettings.ErrBudgetExhausted)
	if got.Status != StatusDone || got.State != StateStopped || got.StateReason != want || got.FinishedAt == nil {
		t.Fatalf("report %s/%s %q", got.Status, got.State, got.StateReason)
	}
	var reason string
	if err := r.pg.QueryRow(ctx, `SELECT data->>'state_reason' FROM loomplane_events
		WHERE type = 'release_report.updated' AND entity_id = $1 ORDER BY sequence DESC LIMIT 1`, rep.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != want {
		t.Fatalf("event reason %q", reason)
	}
	if p := r.w.plans.byID["plan_a"]; p.Status != planning.StatusReady || p.DecidedBy != "" {
		t.Fatalf("plan %s by %q", p.Status, p.DecidedBy)
	}
	rows := r.outbox(t)
	if len(rows) != 3 || !strings.HasPrefix(rows[0].subject, rep.ID+":"+StateStopped) {
		t.Fatalf("outbox %+v", rows)
	}
	// Settled once: a sweep tells no one again.
	if err := r.svc.Settle(ctx); err != nil || len(r.outbox(t)) != 3 {
		t.Fatalf("settle: %v, %d rows", err, len(r.outbox(t)))
	}
}
