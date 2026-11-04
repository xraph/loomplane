//go:build integration

package releaseprep

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
)

// pgRig is a Service over Postgres: the report store, 3a's task store and
// service, and the notifications outbox are real; plans, places, teams
// and the sources are fakes.
type pgRig struct {
	*rig
	pg    *pgdriver.PgDB
	tasks *agentruns.Service
}

func newPgRig(t *testing.T) *pgRig {
	t.Helper()
	store, pg := openPg(t)
	r := newRig(t)
	tasks := agentruns.NewService(agentruns.Deps{Store: agentruns.NewPgStore(pg), Now: func() time.Time { return rigNow }})
	r.svc = NewService(Deps{
		Store: store, Sources: r.w.sources(), Runs: realRuns{tasks}, Requests: r.requests,
		Safety: &agentsafety.Scanner{Engine: r.shield}, Binder: &agentscope.Binder{Tenants: r.tenants},
		Teams: r.teams, PlanStore: r.planStore, Places: r.places, Audit: r.audit,
		Now: func() time.Time { return rigNow },
	})
	return &pgRig{rig: r, pg: pg, tasks: tasks}
}

// realRuns is 3a's service until Task 9 adds CancelTask to it.
type realRuns struct{ *agentruns.Service }

func (realRuns) CancelTask(context.Context, string, string) error {
	return errors.New("not in this test")
}

// reviewingPg stores a report under review with its task running in 3a's
// store, the plan ready.
func (r *pgRig) reviewingPg(t *testing.T, decision string) *ReleaseReport {
	t.Helper()
	ctx := context.Background()
	err := agentruns.NewPgStore(r.pg).Create(ctx, &agentruns.Task{
		ID: "atask_1", OrgID: "org_a", ProjectID: "proj_a", EnvironmentID: "env_prd", Kind: agentruns.KindReleasePreparation,
		RunID: "arun_1", Status: agentruns.StatusRunning, RequestedBy: "usr_dev", MaxTokens: 1000, Metadata: map[string]string{},
		ReleasePlanID: "plan_a", ReleaseRevision: "rev_1", CreatedAt: rigNow, UpdatedAt: rigNow,
	}, agentruns.Record{Principal: []byte(`{}`), Input: "review"})
	if err != nil {
		t.Fatal(err)
	}
	rep := newReport("org_a", "proj_a", "env_prd")
	rep.FromEnvironmentID, rep.PlanID, rep.RequestedBy = "env_stg", "plan_a", "usr_dev"
	if err = r.svc.d.Store.Create(ctx, rep); err != nil {
		t.Fatal(err)
	}
	if ok, serr := r.svc.d.Store.SetTask(ctx, "org_a", rep.ID, "atask_1", "arun_1"); !ok || serr != nil {
		t.Fatalf("set task: %v %v", ok, serr)
	}
	if decision != DecisionNone {
		if err = r.svc.d.Store.RecordEvaluation(ctx, "org_a", rep.ID, []GateCheck{}, decision, nil, rigNow); err != nil {
			t.Fatal(err)
		}
	}
	plan := *r.w.plan
	r.planStore.plans["plan_a"] = &plan
	return rep
}

type outboxRow struct {
	user, subject string
	payload       map[string]any
}

func (r *pgRig) outbox(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := r.pg.Query(context.Background(), `SELECT user_id, subject_id, payload FROM loomplane_notification_outbox
		WHERE kind = $1 ORDER BY user_id`, notifications.KindAgentRelease)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var row outboxRow
		var raw []byte
		if err := rows.Scan(&row.user, &row.subject, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &row.payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Review Focus 5 on Postgres: the workspace turns its agent off while the
// agent reviews. 3a's CancelAll cancels the task with no reason of its
// own; Settle reads the plan and the task with no principal, settles the
// report stopped with the agent-off reason, sends one plain notice to the
// requester and the project's team, and leaves the plan ready.
func TestPgSettleAfterTheAgentIsTurnedOff(t *testing.T) {
	ctx := context.Background()
	r := newPgRig(t)
	rep := r.reviewingPg(t, DecisionPromote)
	_ = r.svc.d.Store.AddConcern(ctx, "org_a", rep.ID, Concern{Kind: ConcernAsk, Text: "The migration drops a column.", RunID: "arun_1", At: rigNow})

	if err := r.tasks.CancelAll(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	task, err := r.tasks.ByRun(ctx, "org_a", "arun_1")
	if err != nil || task.Status != agentruns.StatusCancelled || task.Error != "" {
		t.Fatalf("CancelAll left %+v %v", task, err)
	}
	r.w.budgets.off = true
	r.tenants.off["org_a"] = true

	if err = r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := r.svc.d.Store.Get(ctx, "org_a", rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusDone || s.State != StateStopped || !strings.Contains(s.StateReason, agentsettings.ErrDisabled.Error()) || s.FinishedAt == nil {
		t.Fatalf("settled %s/%s %q", s.Status, s.State, s.StateReason)
	}
	if p := r.planStore.plans["plan_a"]; p.Status != planning.StatusReady || p.DecidedBy != "" {
		t.Fatalf("the plan is %s by %q", p.Status, p.DecidedBy)
	}
	if r.w.plans.reads != 0 {
		t.Fatalf("read plans as a principal %d times", r.w.plans.reads)
	}

	rows := r.outbox(t)
	users := make([]string, 0, len(rows))
	for _, row := range rows {
		users = append(users, row.user)
		if row.subject != rep.ID+":"+StateStopped || row.payload["path"] != "/release-reports/"+rep.ID ||
			row.payload["need"] != "deployment:read" || row.payload["project_id"] != "proj_a" {
			t.Fatalf("outbox row %+v", row)
		}
		// Nothing could be scanned, so the concern isn't quoted.
		if body := row.payload["body"].(string); body != sentence(s.StateReason) || strings.Contains(body, "migration") {
			t.Fatalf("body %q", body)
		}
	}
	if !slices.Equal(users, []string{"usr_ada", "usr_dev", "usr_lee"}) {
		t.Fatalf("notified %v", users)
	}

	// Settled once, told once.
	if err = r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(r.outbox(t)); n != 3 {
		t.Fatalf("%d outbox rows after a second sweep", n)
	}
}

// Task 2's M5 on Postgres: nothing ties a report to its project, so a
// report that outlives it is closed, with Loomplane's own reason in the
// stored row and its event.
func TestPgSettleClosesAReportWhoseProjectIsGone(t *testing.T) {
	ctx := context.Background()
	r := newPgRig(t)
	rep := r.reviewingPg(t, DecisionNone)
	r.places.goneProj = map[string]bool{"proj_a": true}
	if err := r.svc.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := r.svc.d.Store.Get(ctx, "org_a", rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusFailed || s.State != StateFailed || s.StateReason != reasonNoProject {
		t.Fatalf("settled %s/%s %q", s.Status, s.State, s.StateReason)
	}
	var reason string
	if err = r.pg.QueryRow(ctx, `SELECT data->>'state_reason' FROM loomplane_events
		WHERE type = 'release_report.updated' AND entity_id = $1 ORDER BY sequence DESC LIMIT 1`, rep.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != reasonNoProject {
		t.Fatalf("event reason %q", reason)
	}
	if rows := r.outbox(t); len(rows) != 3 || rows[0].subject != rep.ID+":"+StateFailed {
		t.Fatalf("outbox %+v", rows)
	}
}
