//go:build integration

package agentrequests

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/teams"
)

type pgRig struct {
	t     *testing.T
	pg    *pgdriver.PgDB
	store *PgStore
	tasks *agentruns.PgStore
}

func newPgRig(t *testing.T) *pgRig {
	t.Helper()
	dsn := agenttest.FreshDatabase(t)
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	r := &pgRig{t: t, pg: pg, store: NewPgStore(pg), tasks: agentruns.NewPgStore(pg)}
	r.exec(`INSERT INTO authsome_apps(id,name,slug) VALUES('app','App','app')`)
	r.exec(`INSERT INTO authsome_environments(id,app_id,name,slug,type,is_default) VALUES('env','app','Production','production','production',true)`)
	for _, u := range []string{ada, bo, cy, dee, eve, bea} {
		r.exec(`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES($1,'app','env',$2,$3,'')`, u, u+"@example.com", names[u])
	}
	r.exec(`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES('org_a','app','env','A','a',$1),('org_b','app','env','B','b',$1)`, ada)
	return r
}

func (r *pgRig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.pg.Exec(context.Background(), q, args...); err != nil {
		r.t.Fatal(err)
	}
}

// task writes a task row, which a request refers to.
func (r *pgRig) task(org, taskID, runID, requestedBy string) *agentruns.Task {
	r.t.Helper()
	now := time.Now().UTC()
	task := &agentruns.Task{ID: taskID, OrgID: org, ProjectID: projA, Kind: agentruns.KindChat, RunID: runID, Status: agentruns.StatusWaiting,
		RequestedBy: requestedBy, MaxTokens: 1000, CreatedAt: now, UpdatedAt: now}
	if err := r.tasks.Create(context.Background(), task, agentruns.Record{Principal: []byte(`{}`), Input: "x"}); err != nil {
		r.t.Fatal(err)
	}
	return task
}

func pgRequest(reqID, org, taskID, runID, call string, at time.Time) *Request {
	return &Request{
		ID: reqID, OrgID: org, ProjectID: projA, TaskID: taskID, RunID: runID, Kind: KindAnswer, Status: StatusPending,
		Question: "Which region?", Context: []Link{{Label: "The project", Path: "/projects/" + projA}}, Options: []string{"eu", "us"},
		Target: Target{Key: "K"}, AskedFor: ada, ExpiresAt: at.Add(24 * time.Hour), CreatedAt: at, UpdatedAt: at,
		SuspensionID: "susp_1", ToolCallID: call, TeamID: "team_pay", TeamName: "Payments", RouteSource: "named", TeamReason: "they own it",
	}
}

func (r *pgRig) insert(req *Request) bool {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.store.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	created, err := r.store.Insert(ctx, tx, req)
	if err != nil {
		r.t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
	return created
}

// The Postgres store keeps each workspace's requests to itself, makes one
// request per tool call of a run, settles a request once, and finds what
// the sweep settles.
func TestPgStoreKeepsRequests(t *testing.T) {
	r := newPgRig(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.task(orgA, "atask_a", "arun_a", ada)
	r.task(orgB, "atask_b", "arun_b", bea)
	a := pgRequest("areq_a", orgA, "atask_a", "arun_a", "call_1", now)
	if !r.insert(a) {
		t.Fatal("not created")
	}
	dup := pgRequest("areq_dup", orgA, "atask_a", "arun_a", "call_1", now)
	if r.insert(dup) {
		t.Fatal("a second request for one tool call")
	}
	b := pgRequest("areq_b", orgB, "atask_b", "arun_b", "call_1", now.Add(time.Second))
	b.TeamID, b.RouteSource, b.AskedFor = "", "admins", ""
	if !r.insert(b) {
		t.Fatal("org B's request not created")
	}
	got, err := r.store.Get(ctx, orgA, a.ID)
	if err != nil || got.Question != a.Question || len(got.Options) != 2 || got.Context[0] != a.Context[0] || got.Target.Key != "K" ||
		got.SuspensionID != "susp_1" || got.ToolCallID != "call_1" || got.TeamReason != "they own it" || !got.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatalf("get %+v, %v", got, err)
	}
	if _, err = r.store.Get(ctx, orgB, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org B read org A's request: %v", err)
	}
	if list, lerr := r.store.List(ctx, orgB, StoreFilter{}); lerr != nil || len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("org B's list %+v, %v", list, lerr)
	}

	// Mine: asked of me, or routed to a team I'm a current member of.
	r.exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES ('amem_dee','org_a','usr_dee','viewer')`)
	r.exec(`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES ('team_pay','org_a','Payments','payments','x')`)
	r.exec(`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES
		('team_pay','org_a','usr_dee','amem_dee','x'),('team_pay','org_a','usr_eve','amem_gone','x')`)
	for user, want := range map[string]int{ada: 1, dee: 1, eve: 0, bo: 0} {
		list, lerr := r.store.List(ctx, orgA, StoreFilter{Mine: user})
		if lerr != nil || len(list) != want {
			t.Fatalf("mine for %s: %d, %v", user, len(list), lerr)
		}
	}
	if list, _ := r.store.List(ctx, orgB, StoreFilter{Mine: bea, Admin: true}); len(list) != 1 {
		t.Fatalf("admins' request for an admin: %d", len(list))
	}

	st := Settlement{Status: StatusAnswered, Outcome: OutcomeAnswered, AnsweredBy: ada, Answer: "eu", At: time.Now()}
	if moved, serr := r.store.Settle(ctx, orgB, a.ID, st); serr != nil || moved {
		t.Fatalf("org B settled org A's request: %v %v", moved, serr)
	}
	if moved, serr := r.store.Settle(ctx, orgA, a.ID, st); serr != nil || !moved {
		t.Fatalf("settle: %v %v", moved, serr)
	}
	if moved, _ := r.store.Settle(ctx, orgA, a.ID, st); moved {
		t.Fatal("settled twice")
	}
	if moved, rerr := r.store.Reopen(ctx, orgA, a.ID, st); rerr != nil || !moved {
		t.Fatalf("reopen: %v %v", moved, rerr)
	}
	if got, _ = r.store.Get(ctx, orgA, a.ID); got.Status != StatusPending || got.AnsweredBy != "" || got.AnsweredAt != nil {
		t.Fatalf("reopened %+v", got)
	}

	// Due: pending questions only once past their expiry.
	due, err := r.store.Due(ctx, now, Cursor{}, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due now %+v, %v", due, err)
	}
	due, _ = r.store.Due(ctx, now.Add(25*time.Hour), Cursor{}, 10)
	if len(due) != 2 || due[0].OrgID != orgA || due[0].ID != a.ID {
		t.Fatalf("due later %+v", due)
	}
	if pause, perr := r.store.Pause(ctx, orgA, "arun_a", "susp_1"); perr != nil || len(pause) != 1 {
		t.Fatalf("pause %+v, %v", pause, perr)
	}

	cancelled, err := r.store.CancelTask(ctx, orgA, "atask_a", time.Now())
	if err != nil || len(cancelled) != 1 || cancelled[0].Status != StatusCancelled {
		t.Fatalf("cancel %+v, %v", cancelled, err)
	}
	if got, _ = r.store.Get(ctx, orgB, b.ID); got.Status != StatusPending {
		t.Fatal("cancelling org A's task cancelled org B's request")
	}
}

// A request and its notices commit together: a notice that can't be
// queued takes the request with it, so nobody is asked something nobody
// was told about, and a hook repeated later makes both.
func TestRequestStoreAndNoticesShareATransaction(t *testing.T) {
	pr := newPgRig(t)
	ctx := context.Background()
	r := newRig(t)
	task := pr.task(orgA, r.background.ID, r.background.RunID, "")
	r.svc.d.Store = pr.store
	r.svc.d.DB = pr.pg
	r.svc.d.Teams = teams.NewService(teams.Deps{DB: pr.pg})
	pr.exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES ('amem_cy','org_a','usr_cy','admin')`)

	failing := errors.New("the outbox is gone")
	r.svc.enqueue = func(ctx context.Context, q notifications.Execer, n notifications.Notice) error {
		if err := notifications.Enqueue(ctx, q, n); err != nil {
			return err
		}
		return failing
	}
	n := external(ask(t, "call_bg", map[string]any{"question": "Roll back?"}))
	if err := r.svc.OnSuspended(ctx, task, n); !errors.Is(err, failing) {
		t.Fatalf("OnSuspended: %v", err)
	}
	count := func(q string) int {
		t.Helper()
		var c int
		if err := pr.pg.QueryRow(ctx, q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := count(`SELECT count(*) FROM loomplane_agent_requests`); c != 0 {
		t.Fatalf("%d requests after the notice failed", c)
	}
	if c := count(`SELECT count(*) FROM loomplane_notification_outbox WHERE kind = 'agent.request'`); c != 0 {
		t.Fatalf("%d notices after the notice failed", c)
	}

	r.svc.enqueue = notifications.Enqueue
	for range 2 {
		if err := r.svc.OnSuspended(ctx, task, n); err != nil {
			t.Fatal(err)
		}
	}
	if c := count(`SELECT count(*) FROM loomplane_agent_requests`); c != 1 {
		t.Fatalf("%d requests", c)
	}
	if c := count(`SELECT count(*) FROM loomplane_notification_outbox WHERE kind = 'agent.request' AND user_id = 'usr_cy'`); c != 1 {
		t.Fatalf("%d notices to the admin", c)
	}
	req, err := pr.store.Pause(ctx, orgA, task.RunID, n.SuspensionID.String())
	if err != nil || len(req) != 1 || req[0].RouteSource != "admins" || req[0].TeamName != teams.AdminsName {
		t.Fatalf("request %+v, %v", req, err)
	}
}

// I1: Due reads only what can settle now, and pages through it, so 150
// old requests in other workspaces that are still waiting, or that are due
// but never settle, don't keep a due one from the sweep.
func TestDueReadsOnlyWhatCanSettle(t *testing.T) {
	r := newPgRig(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.task(orgB, "atask_b", "arun_b", "")
	r.task(orgA, "atask_a", "arun_a", ada)
	r.exec(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj_b','org_b','B','b','ready','x',now(),now())`)
	r.exec(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_b','org_b','proj_b','production','production',now(),now())`)
	r.exec(`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('man_b','org_b','proj_b',1,'{}','d','x',now())`)
	r.exec(`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,kind,manifest_id,manifest_revision,created_by,created_at)
		VALUES('plan_ready','org_b','proj_b','env_b','production','ready','release','man_b',1,'x',now())`)
	old := now.Add(-48 * time.Hour)
	waiting := func(i int, kind Kind, target Target) {
		at := old.Add(time.Duration(i) * time.Second)
		req := pgRequest(fmt.Sprintf("areq_w%03d", i), orgB, "atask_b", "arun_b", fmt.Sprintf("call_w%03d", i), at)
		req.Kind, req.Target, req.ExpiresAt = kind, target, now.Add(time.Hour)
		r.insert(req)
	}
	for i := range 150 {
		switch i % 3 {
		case 0:
			waiting(i, KindApprovePlan, Target{PlanID: "plan_ready"})
		case 1:
			waiting(i, KindConnectGitHub, Target{Repository: "acme/api"})
		default:
			waiting(i, KindCreateConnection, Target{Cloud: "aws"})
		}
	}
	due := pgRequest("areq_due", orgA, "atask_a", "arun_a", "call_due", now.Add(-time.Hour))
	due.Kind, due.Target, due.ExpiresAt = KindApprovePlan, Target{PlanID: "plan_gone"}, now.Add(time.Hour)
	r.insert(due)
	got, err := r.store.Due(ctx, now, Cursor{}, 100)
	if err != nil || len(got) != 1 || got[0].ID != "areq_due" {
		t.Fatalf("due %+v, %v", got, err)
	}

	// Once their connection is made the waiting requests are due, and the
	// sweep pages through them oldest first to the one behind them.
	r.exec(`INSERT INTO loomplane_github_installations(org_id,installation_id,account,created_by,created_at) VALUES('org_b',1,'acme','x',now())`)
	r.exec(`UPDATE loomplane_agent_requests SET expires_at = $1 WHERE kind = 'create_connection'`, now.Add(-time.Minute))
	var seen []string
	cursor := Cursor{}
	for range 5 {
		page, perr := r.store.Due(ctx, now, cursor, 40)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, ref := range page {
			seen = append(seen, ref.ID)
			cursor = Cursor{At: ref.CreatedAt, ID: ref.ID}
		}
		if len(page) < 40 {
			break
		}
	}
	if len(seen) != 101 || seen[len(seen)-1] != "areq_due" {
		t.Fatalf("paged %d, last %q", len(seen), seen[len(seen)-1])
	}
}

// M10: an answer and a cancel race on one row and exactly one wins.
func TestAnAnswerAndACancelSettleOnce(t *testing.T) {
	r := newPgRig(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r.task(orgA, "atask_a", "arun_a", ada)
	r.insert(pgRequest("areq_1", orgA, "atask_a", "arun_a", "call_1", now))
	r.insert(pgRequest("areq_2", orgA, "atask_a", "arun_a", "call_2", now))
	st := Settlement{Status: StatusAnswered, Outcome: OutcomeAnswered, AnsweredBy: ada, At: now}
	if moved, err := r.store.Settle(ctx, orgA, "areq_1", st); err != nil || !moved {
		t.Fatalf("settle: %v %v", moved, err)
	}
	cancelled, err := r.store.CancelTask(ctx, orgA, "atask_a", now)
	if err != nil || len(cancelled) != 1 || cancelled[0].ID != "areq_2" {
		t.Fatalf("cancel %+v, %v", cancelled, err)
	}
	if moved, _ := r.store.Settle(ctx, orgA, "areq_2", st); moved {
		t.Fatal("answered a cancelled request")
	}
}

// M6: a person with no name is "a teammate" to the model, never their
// email.
func TestPeopleAreNeverNamedByEmail(t *testing.T) {
	r := newPgRig(t)
	r.exec(`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES('usr_anon','app','env','anon@acme.com','','')`)
	people := PgPeople{DB: r.pg, AppID: "app"}
	if got := people.Name(context.Background(), "usr_anon"); got != "a teammate" {
		t.Fatalf("name %q", got)
	}
	if got := people.Name(context.Background(), ada); got != "Ada" {
		t.Fatalf("name %q", got)
	}
}
