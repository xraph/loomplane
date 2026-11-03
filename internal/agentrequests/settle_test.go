package agentrequests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func (r *rig) settle(t *testing.T) {
	t.Helper()
	if err := r.svc.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSettleApprovesWhenAPersonApproved(t *testing.T) {
	r := newRig(t)
	n := approval(t, "plan_1", "rev1")
	r.suspend(t, r.chat, n)
	r.plans.set(planning.StatusApproved, cy)
	r.settle(t)
	d := r.resumer.decided(t)
	if len(d) != 1 {
		t.Fatalf("decisions %+v", d)
	}
	got := d[0]
	if !got.d.Approved || got.d.DecidedBy != cy || got.d.Reason != "approved by Cy" || got.checkpoint != n.CheckpointID.String() || !got.bound {
		t.Fatalf("decision %+v", got)
	}
	// Cortex records the person who decided, through the codec.
	if dec, ok := got.principal.(agentauthz.Decider); !ok || dec.Person != person(orgA, cy) {
		t.Fatalf("decider %#v", got.principal)
	}
	req := r.store.only(t)
	if req.Status != StatusAnswered || req.Outcome != OutcomeApproved || req.AnsweredBy != cy {
		t.Fatalf("request %+v", req)
	}
}

// M8: the run resumes only once the plan has left ready, so the resumed
// approve_plan call reads the person's decision, never a plan still ready.
func TestSettleWaitsWhileThePlanIsReady(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
	for _, status := range []string{planning.StatusReady, planning.StatusPlanning} {
		r.plans.set(status, "")
		r.settle(t)
		if len(r.resumer.decided(t)) != 0 {
			t.Fatalf("decided while the plan was %s", status)
		}
	}
	// Past the request's expiry with the plan still ready, cortex's own
	// expiry decides the checkpoint, never Loomplane.
	r.advance(3 * time.Hour)
	r.plans.set(planning.StatusReady, "")
	r.settle(t)
	if len(r.resumer.decided(t)) != 0 {
		t.Fatal("decided a checkpoint whose plan is still ready")
	}
	if req := r.store.only(t); req.Status != StatusExpired || req.Outcome != OutcomeExpired {
		t.Fatalf("request %+v", req)
	}
}

func TestSettleRejectsOtherwise(t *testing.T) {
	for status, want := range map[string]string{
		planning.StatusRejected:   "the plan was rejected by Cy",
		planning.StatusStale:      "the plan went stale: it changed after it was planned",
		planning.StatusExpired:    "the plan expired before anyone decided it",
		planning.StatusSuperseded: "a newer plan for the environment superseded it",
		planning.StatusFailed:     "the plan failed",
	} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t)
			r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
			by := ""
			if status == planning.StatusRejected {
				by = cy
			}
			r.plans.set(status, by)
			r.settle(t)
			d := r.resumer.decided(t)
			if len(d) != 1 || d[0].d.Approved || d[0].d.Reason != want {
				t.Fatalf("decisions %+v", d)
			}
			req := r.store.only(t)
			if req.Status == StatusPending || req.Outcome == OutcomeApproved {
				t.Fatalf("request %+v", req)
			}
		})
	}
}

// Only a person's approval resumes a run approved: a plan approved by the
// agent or Loomplane automation while the checkpoint waited is not one.
func TestSettleApprovesOnlyAPersonsApproval(t *testing.T) {
	for _, by := range []string{security.AgentSubject(orgA), "loomplane:" + orgA, ""} {
		r := newRig(t)
		r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
		r.plans.set(planning.StatusApproved, by)
		r.settle(t)
		d := r.resumer.decided(t)
		if len(d) != 1 || d[0].d.Approved {
			t.Fatalf("approved by %q: %+v", by, d)
		}
	}
}

func TestSettleIsIdempotent(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
	r.plans.set(planning.StatusApproved, cy)
	r.settle(t)
	r.settle(t)
	if d := r.resumer.decided(t); len(d) != 1 {
		t.Fatalf("decisions %+v", d)
	}
	settles := 0
	for _, a := range r.audit.actions() {
		if a == "agent.request.settle" {
			settles++
		}
	}
	if settles != 1 {
		t.Fatalf("%d settle audits", settles)
	}
	// A decision recorded by an earlier pass whose request write was lost
	// is finished on the next.
	r2 := newRig(t)
	n := approval(t, "plan_1", "rev1")
	r2.suspend(t, r2.chat, n)
	r2.plans.set(planning.StatusApproved, cy)
	r2.resumer.decisions = append(r2.resumer.decisions, decision{checkpoint: n.CheckpointID.String(), d: checkpoint.Decision{Approved: true, DecidedBy: cy}})
	r2.settle(t)
	if req := r2.store.only(t); req.Status != StatusAnswered {
		t.Fatalf("request %+v", req)
	}
}

func TestSettleExpiresUnansweredQuestions(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	r.settle(t)
	if len(r.resumer.provided(t)) != 0 {
		t.Fatal("expired before its time")
	}
	r.advance(25 * time.Hour)
	r.settle(t)
	got, err := r.store.Get(context.Background(), orgA, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusExpired || got.Outcome != OutcomeExpired {
		t.Fatalf("request %+v", got)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].in.ToolResults[0].Error != "nobody answered in time" || !res[0].bound {
		t.Fatalf("results %+v", res)
	}
	r.settle(t)
	if len(r.resumer.provided(t)) != 1 {
		t.Fatal("expired twice")
	}
}

func TestSettleNoticesAConnectionMadeElsewhere(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(
		suspension.PendingCall{ID: "call_gh", Name: "request_github_connection", Arguments: args(t, map[string]string{"repository": "acme/api", "why": "x"})},
	))
	r.settle(t)
	if len(r.resumer.provided(t)) != 0 {
		t.Fatal("settled before anything was connected")
	}
	r.github.add(orgA, "acme", r.now.Add(time.Minute))
	r.github.add(orgB, "other", r.now.Add(time.Minute))
	r.settle(t)
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].in.ToolResults[0].Content != "GitHub is connected: acme" {
		t.Fatalf("results %+v", res)
	}

	r2 := newRig(t)
	r2.suspend(t, r2.chat, external(
		suspension.PendingCall{ID: "call_cloud", Name: "request_cloud_connection", Arguments: args(t, map[string]string{"cloud": "gcp", "why": "x"})},
	))
	r2.clouds.conns = append(r2.clouds.conns,
		&connections.Connection{ID: "c_old", OrgID: orgA, Cloud: "gcp", Name: "old", CreatedBy: cy, CreatedAt: r2.now.Add(-time.Hour)},
		&connections.Connection{ID: "c_aws", OrgID: orgA, Cloud: "aws", Name: "aws", CreatedBy: cy, CreatedAt: r2.now.Add(time.Hour)},
		&connections.Connection{ID: "c_b", OrgID: orgB, Cloud: "gcp", Name: "theirs", CreatedBy: bea, CreatedAt: r2.now.Add(time.Hour)},
	)
	r2.settle(t)
	if len(r2.resumer.provided(t)) != 0 {
		t.Fatal("an older connection, another cloud's or another workspace's counted")
	}
	r2.clouds.conns = append(r2.clouds.conns, &connections.Connection{ID: "c_new", OrgID: orgA, Cloud: "gcp", Name: "prod-gcp", CreatedBy: cy, CreatedAt: r2.now.Add(time.Hour)})
	r2.settle(t)
	res = r2.resumer.provided(t)
	if len(res) != 1 || !strings.HasPrefix(res[0].in.ToolResults[0].Content, "Cy connected gcp as prod-gcp") {
		t.Fatalf("results %+v", res)
	}
	if req := r2.store.only(t); req.AnsweredBy != cy || req.Outcome != OutcomeConnected {
		t.Fatalf("request %+v", req)
	}
}

// ---- fix round 1 ----

// I1: the sweep reads only what is due, a page at a time from where it
// stopped, so rows that never settle can't hold back one that can.
func TestSettleReachesADueRowPastManyStuckOnes(t *testing.T) {
	r := newRig(t)
	stuck := r.task("atask_stuck", orgB, projB, "")
	r.runs.bindFail[stuck.ID] = true
	r.plans.plans["plan_b"] = &planning.Plan{ID: "plan_b", OrgID: orgB, ProjectID: projB, Status: planning.StatusApproved, DecidedBy: bea, Revision: "r"}
	old := r.now.Add(-time.Hour)
	r.store.mu.Lock()
	for i := range 150 {
		at := old.Add(time.Duration(i) * time.Second)
		reqID := fmt.Sprintf("areq_stuck_%03d", i)
		r.store.rows[reqID] = &Request{ID: reqID, OrgID: orgB, ProjectID: projB, TaskID: stuck.ID, RunID: stuck.RunID, Kind: KindApprovePlan,
			Status: StatusPending, Target: Target{PlanID: "plan_b", PlanRevision: "r"}, ExpiresAt: r.now.Add(time.Hour),
			CreatedAt: at, UpdatedAt: at, SuspensionID: id.NewSuspensionID().String(), CheckpointID: id.NewCheckpointID().String(), ToolCallID: reqID}
	}
	r.store.mu.Unlock()
	n := approval(t, "plan_1", "rev1")
	r.suspend(t, r.chat, n)
	r.plans.set(planning.StatusApproved, cy)
	if err := r.svc.Settle(context.Background()); err == nil {
		t.Fatal("the stuck rows' failures were hidden")
	}
	found := false
	for _, d := range r.resumer.decided(t) {
		found = found || (d.checkpoint == n.CheckpointID.String() && d.d.Approved)
	}
	if !found {
		t.Fatal("the due row behind 150 stuck ones was never settled")
	}
}

// M1: only an approval of the revision the agent asked about, by someone
// who may still approve deployments on the project, counts.
func TestSettleCountsOnlyACurrentApprovalOfTheRevisionAsked(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, approval(t, "plan_1", "rev_old"))
	r.plans.set(planning.StatusApproved, cy)
	r.settle(t)
	if d := r.resumer.decided(t); len(d) != 1 || d[0].d.Approved {
		t.Fatalf("another revision's approval: %+v", d)
	}
	r2 := newRig(t)
	r2.suspend(t, r2.chat, approval(t, "plan_1", "rev1"))
	r2.plans.set(planning.StatusApproved, ada) // a developer: no deployment:approve
	r2.settle(t)
	if d := r2.resumer.decided(t); len(d) != 1 || d[0].d.Approved {
		t.Fatalf("an approval by someone who can't approve: %+v", d)
	}
	r3 := newRig(t)
	n := approval(t, "plan_1", "rev1")
	r3.suspend(t, r3.chat, n)
	before := r3.now.Add(-time.Minute)
	r3.plans.mu.Lock()
	r3.plans.plans["plan_1"].Status, r3.plans.plans["plan_1"].DecidedBy, r3.plans.plans["plan_1"].DecidedAt = planning.StatusApproved, cy, &before
	r3.plans.mu.Unlock()
	r3.settle(t)
	if d := r3.resumer.decided(t); len(d) != 1 || !d[0].d.Approved || d[0].d.Reason != "the plan was already approved by Cy" {
		t.Fatalf("an approval before the request: %+v", d)
	}
}

// M2: a checkpoint cortex already closed (expired, or decided some other
// way) settles the request expired, not with the plan's verdict.
func TestSettleDoesntClaimACheckpointCortexClosed(t *testing.T) {
	for name, setup := range map[string]func(r *rig, n suspension.Notice){
		"expired": func(r *rig, _ suspension.Notice) { r.resumer.decideErr = cortex.ErrSuspensionExpired },
		"decided otherwise": func(r *rig, n suspension.Notice) {
			r.resumer.decisions = append(r.resumer.decisions, decision{checkpoint: n.CheckpointID.String(), d: checkpoint.Decision{Approved: false, DecidedBy: "engine"}})
		},
	} {
		r := newRig(t)
		n := approval(t, "plan_1", "rev1")
		r.suspend(t, r.chat, n)
		r.plans.set(planning.StatusApproved, cy)
		setup(r, n)
		r.settle(t)
		if req := r.store.only(t); req.Status != StatusExpired || req.Outcome != OutcomeExpired {
			t.Fatalf("%s: %+v", name, req)
		}
	}
}
