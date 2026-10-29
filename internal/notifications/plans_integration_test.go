//go:build integration

package notifications

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/security"
)

// approvers denies deployment:approve to the users in it.
type approvers struct{ demoted atomic.Value }

func (a *approvers) Require(_ context.Context, p security.Principal, permission security.Permission, _ string) error {
	if permission == security.DeploymentApprove {
		if user, _ := a.demoted.Load().(string); user == p.UserID {
			return security.ErrForbidden
		}
	}
	return nil
}

// planRig is pushRig with Ada (owner, creates plans), Bo (admin) and Cy
// (developer) in org-a, the plans table, and the plan notice trigger.
func planRig(t *testing.T) (*Service, *fakeGateway, *approvers) {
	t.Helper()
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	authz := &approvers{}
	svc.SetAuthorizer(authz)
	for _, q := range []string{
		`DELETE FROM authsome_members WHERE user_id='user-a'`,
		`INSERT INTO authsome_users(id,app_id,email,first_name,last_name)VALUES('ada','app','ada@example.com','Ada','Lovelace'),('bo','app','bo@example.com','Bo',''),('cy','app','cy@example.com','Cy','')`,
		`INSERT INTO authsome_members(org_id,user_id,role)VALUES('org-a','ada','owner'),('org-a','bo','admin'),('org-a','cy','member')`,
		`CREATE TABLE loomplane_plans(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,environment TEXT NOT NULL,status TEXT NOT NULL,revision TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,decided_by TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	executor, err := migrate.NewExecutorFor(svc.pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "plan_notices" {
			if err = m.Up(ctx, executor); err != nil {
				t.Fatal(err)
			}
		}
	}
	return svc, gw, authz
}

// planRevision is a real plan revision: a 64-character SHA-256 digest.
const planRevision = "9f2b7c1d4e8a3b6c5d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e"

func newPlan(t *testing.T, svc *Service, env, createdBy string) {
	t.Helper()
	name := map[string]string{"env-a": "staging", "env-p": "production"}[env]
	if _, err := svc.pg.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,created_by)VALUES('plan-1','org-a','project-a',$1,$2,'planning',$4,$3)`, env, name, createdBy, planRevision); err != nil {
		t.Fatal(err)
	}
}

func setPlan(t *testing.T, svc *Service, status, by string) {
	t.Helper()
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_plans SET status=$1,decided_by=$2 WHERE id='plan-1'`, status, by); err != nil {
		t.Fatal(err)
	}
}

type planRow struct {
	id, user, kind, pushState, emailState string
	inboxDone, complete                   bool
	attempts                              int
	due                                   time.Duration
}

func planRows(t *testing.T, svc *Service, kind string) map[string]planRow {
	t.Helper()
	rows, err := svc.pg.Query(t.Context(), `SELECT id,user_id,kind,push_state,email_state,inbox_done,completed_at IS NOT NULL,attempts,next_attempt_at-clock_timestamp() FROM loomplane_notification_outbox WHERE kind=$1`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]planRow{}
	for rows.Next() {
		var r planRow
		var due time.Duration
		if err = rows.Scan(&r.id, &r.user, &r.kind, &r.pushState, &r.emailState, &r.inboxDone, &r.complete, &r.attempts, &due); err != nil {
			t.Fatal(err)
		}
		r.due = due
		out[r.user] = r
	}
	return out
}

// release makes every plan row due now.
func release(t *testing.T, svc *Service) {
	t.Helper()
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_notification_outbox SET next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
}

func boInbox(t *testing.T, svc *Service) (total, unread int) {
	t.Helper()
	items, err := svc.mailStore.ListNotifications(t.Context(), "app", "bo", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range items {
		total++
		if !n.Read {
			unread++
		}
	}
	return total, unread
}

func TestPlanReadyQueuesApproversButNotTheCreator(t *testing.T) {
	svc, _, _ := planRig(t)
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "ready", "")
	rows := planRows(t, svc, KindPlanWaiting)
	bo, ok := rows["bo"]
	if len(rows) != 1 || !ok || bo.id != "plan.waiting:plan-1:bo" {
		t.Fatalf("rows: %+v", rows)
	}
	if bo.due < time.Minute+30*time.Second || bo.due > 2*time.Minute+5*time.Second {
		t.Fatalf("due in %s", bo.due)
	}
}

func TestAutomationPlanSkipsTheWait(t *testing.T) {
	svc, _, _ := planRig(t)
	newPlan(t, svc, "env-a", "loomplane:org-a")
	setPlan(t, svc, "ready", "")
	rows := planRows(t, svc, KindPlanWaiting)
	if len(rows) != 2 || rows["ada"].due > 0 || rows["bo"].due > 0 {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestWaitingPushReachesApproversDevices(t *testing.T) {
	svc, gw, _ := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	if err := svc.drain(t.Context()); err != nil || len(gw.taken()) != 0 {
		t.Fatalf("before the delay: %v %d", err, len(gw.taken()))
	}
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 {
		t.Fatalf("sends: %d", len(sends))
	}
	s := sends[0]
	if s.Body.Urgency != "time_sensitive" || s.Body.CollapseID != collapseID("plan:plan-1") || s.Env.Category != "approval" {
		t.Fatalf("send: %+v %+v", s.Body, s.Env)
	}
	for key, want := range map[string]string{"actions": "approve,review", "revision": planRevision, "kind": "plan.waiting", "subject": "plan-1", "path": "/deployments/plan-1", "environment": "production", "class": "needs_you"} {
		if s.Env.Data[key] != want {
			t.Errorf("data[%s] = %q, want %q", key, s.Env.Data[key], want)
		}
	}
	if s.Env.Title != "Atlas to production needs your approval" {
		t.Fatalf("title: %q", s.Env.Title)
	}
	if total, unread := boInbox(t, svc); total != 1 || unread != 1 {
		t.Fatalf("inbox: %d %d", total, unread)
	}
}

func TestDecidedBeforeTheDelaySkipsPush(t *testing.T) {
	svc, gw, _ := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	setPlan(t, svc, "approved", "ada")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(gw.taken()) != 0 {
		t.Fatalf("sends: %d", len(gw.taken()))
	}
	waiting := planRows(t, svc, KindPlanWaiting)["bo"]
	if waiting.pushState != PushSkipped || waiting.emailState != "skipped" || !waiting.complete {
		t.Fatalf("waiting: %+v", waiting)
	}
	if decided := planRows(t, svc, KindPlanDecided)["bo"]; decided.pushState != PushSkipped || !decided.complete {
		t.Fatalf("decided: %+v", decided)
	}
	if total, unread := boInbox(t, svc); total != 1 || unread != 0 {
		t.Fatalf("inbox: %d %d", total, unread)
	}
}

func TestDecidedReplacesTheWaitingNotification(t *testing.T) {
	svc, gw, _ := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if total, unread := boInbox(t, svc); total != 1 || unread != 1 {
		t.Fatalf("inbox before: %d %d", total, unread)
	}
	setPlan(t, svc, "approved", "ada")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 2 {
		t.Fatalf("sends: %d", len(sends))
	}
	s := sends[1]
	if s.Body.CollapseID != sends[0].Body.CollapseID || s.Body.Urgency != "passive" || s.Env.Body != "Approved by Ada Lovelace." || s.Env.Title != "Atlas to production" || s.Env.Data["actions"] != "open" {
		t.Fatalf("decided: %+v %+v", s.Body, s.Env)
	}
	if total, unread := boInbox(t, svc); total != 1 || unread != 0 {
		t.Fatalf("inbox after: %d %d", total, unread)
	}
}

func TestDecidedWaitsForAnInFlightWaitingPush(t *testing.T) {
	svc, gw, _ := planRig(t)
	bo := gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	setPlan(t, svc, "approved", "ada")
	// The waiting row is mid-push: leased, attempted once, nothing recorded.
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_notification_outbox SET attempts=1,lease_owner='other',lease_until=clock_timestamp()+interval '1 hour',next_attempt_at=clock_timestamp() WHERE kind='plan.waiting'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var lastErr string
	if err := svc.pg.QueryRow(t.Context(), `SELECT last_error FROM loomplane_notification_outbox WHERE kind='plan.decided'`).Scan(&lastErr); err != nil || lastErr != "waiting for an earlier push" {
		t.Fatalf("decided last_error %q %v", lastErr, err)
	}
	if len(gw.taken()) != 0 {
		t.Fatalf("sends: %d", len(gw.taken()))
	}
	// The waiting push lands on the phone, then the decided row is retried.
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_notification_outbox SET push_state='sent',completed_at=clock_timestamp(),push_devices=(SELECT jsonb_build_object(d.id,'sent') FROM (SELECT $1::text AS id) d),lease_owner='',lease_until=NULL WHERE kind='plan.waiting'`, bo.ID); err != nil {
		t.Fatal(err)
	}
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 || sends[0].Env.Body != "Approved by Ada Lovelace." {
		t.Fatalf("sends: %+v", sends)
	}
}

func TestDemotedApproverGetsNoWaitingPush(t *testing.T) {
	svc, gw, authz := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	authz.demoted.Store("bo")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(gw.taken()) != 0 {
		t.Fatalf("sends: %d", len(gw.taken()))
	}
	if total, _ := boInbox(t, svc); total != 0 {
		t.Fatalf("inbox: %d", total)
	}
	if row := planRows(t, svc, KindPlanWaiting)["bo"]; row.pushState != PushSkipped || !row.complete {
		t.Fatalf("row: %+v", row)
	}
}

func TestBlockedPlanQueuesNothing(t *testing.T) {
	svc, _, _ := planRig(t)
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "blocked", "")
	if n := len(planRows(t, svc, KindPlanWaiting)) + len(planRows(t, svc, KindPlanDecided)); n != 0 {
		t.Fatalf("rows: %d", n)
	}
}

func TestPlanDecidedCopy(t *testing.T) {
	for status, want := range map[string]string{
		"approved": "Approved by Ada.", "rejected": "Rejected by Ada.", "expired": "It expired before anyone decided.",
		"stale": "It's out of date. Plan again.", "superseded": "A newer plan replaced it.",
	} {
		if got := planDecidedCopy(status, "Ada"); got != want {
			t.Errorf("%s: %q", status, got)
		}
	}
}

func TestDecidedReachesAPhoneSetToCriticalOnly(t *testing.T) {
	svc, gw, _ := planRig(t)
	bo := gw.registerFor(t, svc, "bo", "h1.bo")
	if _, err := svc.UpdateDevice(t.Context(), security.Principal{UserID: "bo", OrgID: "org-a"}, bo.ID, UpdateDeviceInput{Settings: map[string]string{"level.production": "critical"}}); err != nil {
		t.Fatal(err)
	}
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	setPlan(t, svc, "rejected", "ada")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 2 || sends[1].Env.Body != "Rejected by Ada Lovelace." {
		t.Fatalf("sends: %+v", sends)
	}
}

func TestPlanDecidedDuringRetryStopsTheWaitingPush(t *testing.T) {
	svc, gw, _ := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.bo")
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	// The inbox item was written on an earlier attempt that then failed.
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_notification_outbox SET inbox_done=TRUE,attempts=1,next_attempt_at=clock_timestamp() WHERE kind='plan.waiting'`); err != nil {
		t.Fatal(err)
	}
	setPlan(t, svc, "approved", "ada")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(gw.taken()) != 0 {
		t.Fatalf("sends: %d", len(gw.taken()))
	}
	if row := planRows(t, svc, KindPlanWaiting)["bo"]; row.pushState != PushSkipped || row.emailState != "skipped" || !row.complete {
		t.Fatalf("row: %+v", row)
	}
}

func TestDeciderNames(t *testing.T) {
	svc, _, _ := planRig(t)
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email,username)VALUES('u-name','app','n@example.com','nick')`,
		`INSERT INTO authsome_users(id,app_id,email,display_username,username)VALUES('u-display','app','d@example.com','Dee D','dee')`,
		`INSERT INTO authsome_users(id,app_id,email)VALUES('u-mail','app','only@example.com')`,
		`INSERT INTO authsome_users(id,app_id,email,first_name,deleted_at)VALUES('u-gone','app','gone@example.com','Gone',now())`,
	} {
		if _, err := svc.pg.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	for by, want := range map[string]string{"ada": "Ada Lovelace", "bo": "Bo", "u-name": "nick", "u-display": "Dee D", "u-mail": "only@example.com", "u-gone": "someone", "nobody": "someone", "": "someone", "loomplane:org-a": "Loomplane automation"} {
		if got, err := svc.decider(t.Context(), by); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", by, got, err, want)
		}
	}
}
