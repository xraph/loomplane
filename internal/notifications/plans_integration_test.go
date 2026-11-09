//go:build integration

package notifications

import (
	"context"
	"slices"
	"sync"
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

func TestDecidedReachesAPhoneThatGotTheWaitingPushWhileAnotherFailed(t *testing.T) {
	svc, gw, _ := planRig(t)
	gw.registerFor(t, svc, "bo", "h1.a")
	gw.registerFor(t, svc, "bo", "h1.b")
	gw.answer("h1.b", 500)
	newPlan(t, svc, "env-p", "ada")
	setPlan(t, svc, "ready", "")
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := svc.pg.QueryRow(t.Context(), `SELECT push_state FROM loomplane_notification_outbox WHERE kind=$1 AND user_id='bo'`, KindPlanWaiting).Scan(&state); err != nil || state != PushPending {
		t.Fatalf("waiting row: %s %v", state, err)
	}
	// The waiting row ran out of tries with one phone still failing.
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_notification_outbox SET push_state='failed',completed_at=clock_timestamp(),attempts=10 WHERE kind=$1 AND user_id='bo'`, KindPlanWaiting); err != nil {
		t.Fatal(err)
	}
	before := len(gw.taken())
	setPlan(t, svc, "approved", "ada")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()[before:]
	if len(sends) != 1 || sends[0].Handle != "h1.a" || sends[0].Env.Category != "update" || sends[0].Env.Body != "Approved by Ada Lovelace." {
		t.Fatalf("decided sends: %+v", sends)
	}
}

// teamApprovers allows deployment:approve to Ada and Bo (owner and admin)
// anywhere and to Dee on project-a only, allows everything else, and
// records every check as user|permission|resource.
type teamApprovers struct {
	mu     sync.Mutex
	checks []string
}

func (a *teamApprovers) Require(_ context.Context, p security.Principal, permission security.Permission, resource string) error {
	a.mu.Lock()
	a.checks = append(a.checks, p.UserID+"|"+permission.Name()+"|"+resource)
	a.mu.Unlock()
	if permission != security.DeploymentApprove {
		return nil
	}
	if p.UserID == "ada" || p.UserID == "bo" || (p.UserID == "dee" && resource == "project-a") {
		return nil
	}
	return security.ErrForbidden
}

func (a *teamApprovers) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.checks)
}

// teamPlanRig is planRig plus the teams migration, with members recording
// their authsome member ID. Dee (viewer) is in team A, an admin on
// project-a; Eve (viewer) is in team B, an admin on project-c, another
// org-a project; Fay (viewer) is in team F, project-a's responsible team.
// The agent (agent:org-a) is in team A too. Member rows are dated 2000, so
// a join on timestamps would accept any of them.
func teamPlanRig(t *testing.T) (*Service, *teamApprovers) {
	t.Helper()
	svc, _, _ := planRig(t)
	ctx := t.Context()
	authz := &teamApprovers{}
	svc.SetAuthorizer(authz)
	run := func(qs ...string) {
		t.Helper()
		for _, q := range qs {
			if _, err := svc.pg.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	run(
		`ALTER TABLE authsome_members ADD COLUMN id TEXT, ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT '2000-01-01'`,
		`UPDATE authsome_members SET id='amem_'||user_id`,
		`INSERT INTO authsome_users(id,app_id,email)VALUES('dee','app','dee@example.com'),('eve','app','eve@example.com'),('fay','app','fay@example.com'),('agent:org-a','app','agent@example.com')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES('amem_dee','org-a','dee','viewer'),('amem_eve','org-a','eve','viewer'),('amem_fay','org-a','fay','viewer'),('amem_agent','org-a','agent:org-a','viewer')`,
		`INSERT INTO loomplane_projects(id,org_id)VALUES('project-c','org-a')`,
	)
	executor, err := migrate.NewExecutorFor(svc.pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "teams" {
			if err = m.Up(ctx, executor); err != nil {
				t.Fatal(err)
			}
		}
	}
	run(
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by)VALUES('team-a','org-a','A','a','ada'),('team-b','org-a','B','b','ada'),('team-f','org-a','F','f','ada')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by)VALUES('team-a','org-a','dee','amem_dee','ada'),('team-a','org-a','agent:org-a','amem_agent','ada'),('team-b','org-a','eve','amem_eve','ada'),('team-f','org-a','fay','amem_fay','ada')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by)VALUES('grant-a','team-a','org-a','admin','project-a','ada'),('grant-b','team-b','org-a','admin','project-c','ada')`,
		`UPDATE loomplane_projects SET responsible_team_id='team-f' WHERE id='project-a'`,
	)
	return svc, authz
}

func inboxTotal(t *testing.T, svc *Service, user string) int {
	t.Helper()
	items, err := svc.mailStore.ListNotifications(t.Context(), "app", user, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func waitingUsers(t *testing.T, svc *Service) []string {
	t.Helper()
	rows := planRows(t, svc, KindPlanWaiting)
	users := make([]string, 0, len(rows))
	for user := range rows {
		users = append(users, user)
	}
	slices.Sort(users)
	return users
}

func TestPlanWaitingReachesTeamApprovers(t *testing.T) {
	svc, authz := teamPlanRig(t)
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "ready", "")
	// Bo as an admin, Dee through team A's grant on project-a, Fay through
	// project-a's responsible team. Never Ada (she made it), Cy (developer),
	// Eve (her team's grant is on another project) or the agent.
	if got := waitingUsers(t, svc); !slices.Equal(got, []string{"bo", "dee", "fay"}) {
		t.Fatalf("plan.waiting rows for %v", got)
	}
	release(t, svc)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]int{"bo": 1, "dee": 1, "fay": 0} {
		if got := inboxTotal(t, svc, user); got != want {
			t.Errorf("%s inbox: %d, want %d", user, got, want)
		}
	}
	if fay := planRows(t, svc, KindPlanWaiting)["fay"]; !fay.complete {
		t.Fatalf("fay's row: %+v", fay)
	}
	checks := authz.recorded()
	if !slices.Contains(checks, "dee|deployment:approve|project-a") || slices.Contains(checks, "dee|deployment:approve|") {
		t.Fatalf("checks: %v", checks)
	}
}

func TestPlanWaitingSkipsLeftoverTeamMembers(t *testing.T) {
	for name, qs := range map[string][]string{
		"left": {`DELETE FROM authsome_members WHERE user_id='dee'`},
		// Invited back: a new member ID, dated before the team row, so only
		// the member ID tells the leftover row apart.
		"invited back": {
			`DELETE FROM authsome_members WHERE user_id='dee'`,
			`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES('amem_dee2','org-a','dee','viewer')`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := teamPlanRig(t)
			for _, q := range qs {
				if _, err := svc.pg.Exec(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			newPlan(t, svc, "env-a", "ada")
			setPlan(t, svc, "ready", "")
			if got := waitingUsers(t, svc); !slices.Equal(got, []string{"bo", "fay"}) {
				t.Fatalf("plan.waiting rows for %v", got)
			}
		})
	}
}

func TestPlanWaitingStaysInItsOrg(t *testing.T) {
	svc, _ := teamPlanRig(t)
	// An org-b team with an admin grant on project-a, and another org-b
	// team named as project-a's responsible team. The schema can't stop
	// either; the trigger must not follow them out of org-a.
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email)VALUES('yan','app','yan@example.com'),('zed','app','zed@example.com')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES('amem_yan','org-b','yan','viewer'),('amem_zed','org-b','zed','viewer')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by)VALUES('team-y','org-b','Y','y','user-b'),('team-z','org-b','Z','z','user-b')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by)VALUES('team-y','org-b','yan','amem_yan','user-b'),('team-z','org-b','zed','amem_zed','user-b')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by)VALUES('grant-y','team-y','org-b','admin','project-a','user-b')`,
		`UPDATE loomplane_projects SET responsible_team_id='team-z' WHERE id='project-a'`,
	} {
		if _, err := svc.pg.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "ready", "")
	if got := waitingUsers(t, svc); !slices.Equal(got, []string{"bo", "dee"}) {
		t.Fatalf("plan.waiting rows for %v", got)
	}
}

// Only an admin grant makes a team's members approvers. Gus's team has a
// developer grant on project-a and another on the whole workspace, so he
// gets no plan.waiting row.
func TestPlanWaitingSkipsADeveloperGrant(t *testing.T) {
	svc, _ := teamPlanRig(t)
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email)VALUES('gus','app','gus@example.com')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES('amem_gus','org-a','gus','viewer')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by)VALUES('team-d','org-a','D','d','ada')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by)VALUES('team-d','org-a','gus','amem_gus','ada')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by)VALUES('grant-d1','team-d','org-a','developer','project-a','ada'),('grant-d2','team-d','org-a','developer',NULL,'ada')`,
	} {
		if _, err := svc.pg.Exec(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "ready", "")
	if got := waitingUsers(t, svc); !slices.Equal(got, []string{"bo", "dee", "fay"}) {
		t.Fatalf("plan.waiting rows for %v", got)
	}
}

// An admin grant on the whole workspace (project_id NULL) reaches every
// project's plans: Hal gets a row for a plan on project-a.
func TestPlanWaitingReachesAWorkspaceAdminGrant(t *testing.T) {
	svc, _ := teamPlanRig(t)
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email)VALUES('hal','app','hal@example.com')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES('amem_hal','org-a','hal','viewer')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by)VALUES('team-w','org-a','W','w','ada')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by)VALUES('team-w','org-a','hal','amem_hal','ada')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by)VALUES('grant-w','team-w','org-a','admin',NULL,'ada')`,
	} {
		if _, err := svc.pg.Exec(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	newPlan(t, svc, "env-a", "ada")
	setPlan(t, svc, "ready", "")
	if got := waitingUsers(t, svc); !slices.Equal(got, []string{"bo", "dee", "fay", "hal"}) {
		t.Fatalf("plan.waiting rows for %v", got)
	}
}
