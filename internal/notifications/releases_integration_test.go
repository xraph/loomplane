//go:build integration

package notifications

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
	"github.com/xraph/herald/inbox"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/security"
)

// releaseRig is planRig (Ada owner, Bo admin, Cy member in org-a) with Di,
// another member, and the release notice migration. Plan plan-a was decided
// by decider.
func releaseRig(t *testing.T, decider string) (*Service, *fakeGateway) {
	t.Helper()
	svc, gw, _ := planRig(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email,first_name)VALUES('di','app','di@example.com','Di')`,
		`INSERT INTO authsome_members(org_id,user_id,role)VALUES('org-a','di','member')`,
	} {
		if _, err := svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.pg.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,created_by,decided_by)VALUES('plan-a','org-a','project-a','env-p','production','approved',$1,'cy',$2)`, planRevision, decider); err != nil {
		t.Fatal(err)
	}
	executor, err := migrate.NewExecutorFor(svc.pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "release_notices" {
			if err = m.Up(ctx, executor); err != nil {
				t.Fatal(err)
			}
		}
	}
	return svc, gw
}

// quietPhone registers a phone for the user with no quiet hours.
func quietPhone(t *testing.T, svc *Service, gw *fakeGateway, user string) {
	t.Helper()
	d := gw.registerFor(t, svc, user, "h1."+user)
	if _, err := svc.UpdateDevice(t.Context(), security.Principal{UserID: user, OrgID: "org-a"}, d.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true}}); err != nil {
		t.Fatal(err)
	}
}

func startDeployment(t *testing.T, svc *Service, env, startedBy string) {
	t.Helper()
	if _, err := svc.pg.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,plan_id,started_by,status)VALUES('deployment-a','org-a','project-a',$1,'plan-a',$2,'running')`, env, startedBy); err != nil {
		t.Fatal(err)
	}
}

// canary writes the deployment's canary the way the runner does.
func canary(t *testing.T, svc *Service, status string, step, weight int) {
	t.Helper()
	doc := fmt.Sprintf(`{"status":%q,"steps":[10,25,50],"step":%d,"weight":%d,"reason":"error rate 9%%"}`, status, step, weight)
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET canary=$1::jsonb WHERE id='deployment-a'`, doc); err != nil {
		t.Fatal(err)
	}
}

// ofKind keeps the sends of one notice kind.
func ofKind(sends []gatewaySend, kind string) []gatewaySend {
	return slices.DeleteFunc(slices.Clone(sends), func(s gatewaySend) bool { return s.Env.Data["kind"] != kind })
}

func releaseUsers(t *testing.T, svc *Service, kind string) []string {
	t.Helper()
	rows, err := svc.pg.Query(t.Context(), `SELECT user_id FROM loomplane_notification_outbox WHERE kind=$1 ORDER BY user_id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var users []string
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	return users
}

func TestCanaryRollbackNotifiesStarterApproverAndAdmins(t *testing.T) {
	svc, gw := releaseRig(t, "di")
	for _, u := range []string{"ada", "bo", "cy", "di"} {
		quietPhone(t, svc, gw, u)
	}
	startDeployment(t, svc, "env-p", "cy")
	canary(t, svc, "ramping", 1, 25)
	canary(t, svc, "reverting", 1, 25)
	canary(t, svc, "rolled_back", 1, 0)
	if got := releaseUsers(t, svc, KindReleaseRolledBack); !slices.Equal(got, []string{"ada", "bo", "cy", "di"}) {
		t.Fatalf("recipients: %v", got)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := ofKind(gw.taken(), KindReleaseRolledBack)
	if len(sends) != 4 {
		t.Fatalf("sends: %d", len(sends))
	}
	s := sends[0]
	if s.Body.Urgency != "time_sensitive" || s.Env.Category != "alert" || s.Body.CollapseID != collapseID("release:deployment-a") || s.Env.Data["actions"] != "open" {
		t.Fatalf("send: %+v %+v", s.Body, s.Env)
	}
	if s.Env.Title != "Atlas: canary rolled back in production" || !strings.Contains(s.Env.Body, "failed at canary step 2 (25% of traffic)") {
		t.Fatalf("copy: %q %q", s.Env.Title, s.Env.Body)
	}
	for key, want := range map[string]string{"kind": "release.rolled_back", "subject": "deployment-a", "path": "/deployments/plan-a", "class": "alert"} {
		if s.Env.Data[key] != want {
			t.Errorf("data[%s] = %q, want %q", key, s.Env.Data[key], want)
		}
	}
	var webhooks int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_notification_outbox WHERE kind=$1 AND NOT webhook_done`, KindReleaseRolledBack).Scan(&webhooks); err != nil || webhooks != 0 {
		t.Fatalf("rows owing a webhook: %d %v", webhooks, err)
	}
}

func TestRollbackToAnAdminWhoApprovedIsOneRow(t *testing.T) {
	svc, _ := releaseRig(t, "bo")
	startDeployment(t, svc, "env-a", "cy")
	canary(t, svc, "ramping", 0, 10)
	canary(t, svc, "rolled_back", 0, 0)
	if got := releaseUsers(t, svc, KindReleaseRolledBack); !slices.Equal(got, []string{"ada", "bo", "cy"}) {
		t.Fatalf("recipients: %v", got)
	}
}

func TestStagingRollbackIsActive(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	quietPhone(t, svc, gw, "cy")
	startDeployment(t, svc, "env-a", "cy")
	canary(t, svc, "ramping", 0, 10)
	canary(t, svc, "rolled_back", 0, 0)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 || sends[0].Body.Urgency != "active" {
		t.Fatalf("sends: %+v", sends)
	}
}

func TestRevertFailedIsCriticalForAdminsOnly(t *testing.T) {
	svc, gw := releaseRig(t, "di")
	for _, u := range []string{"ada", "bo", "cy", "di"} {
		quietPhone(t, svc, gw, u)
	}
	startDeployment(t, svc, "env-a", "cy")
	canary(t, svc, "ramping", 1, 25)
	canary(t, svc, "reverting", 1, 25)
	canary(t, svc, "revert_failed", 1, 25)
	if got := releaseUsers(t, svc, KindReleaseRevertFailed); !slices.Equal(got, []string{"ada", "bo"}) {
		t.Fatalf("recipients: %v", got)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 2 {
		t.Fatalf("sends: %d", len(sends))
	}
	for _, s := range sends {
		if s.Body.Urgency != "critical" || s.Env.Title != "Atlas: rollback failed in staging" || s.Body.CollapseID != collapseID("release:deployment-a") {
			t.Fatalf("send: %+v %+v", s.Body, s.Env)
		}
	}
}

func TestCanaryProgressWritesOneInboxRow(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	quietPhone(t, svc, gw, "cy")
	quietPhone(t, svc, gw, "bo")
	startDeployment(t, svc, "env-p", "cy")
	canary(t, svc, "pending", -1, 0)
	canary(t, svc, "ramping", 0, 10)
	canary(t, svc, "ramping", 0, 10) // the same step again adds nothing
	canary(t, svc, "ramping", 1, 25)
	canary(t, svc, "ramping", 2, 50)
	canary(t, svc, "promoting", 2, 50)
	canary(t, svc, "promoted", 2, 100)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 8 {
		t.Fatalf("sends: %d", len(sends))
	}
	var cy []gatewaySend
	for _, s := range sends {
		if s.Body.CollapseID != collapseID("release:deployment-a") || s.Body.Urgency != "passive" || s.Env.Category != "update" {
			t.Fatalf("send: %+v %+v", s.Body, s.Env)
		}
		if s.Handle == "h1.cy" {
			cy = append(cy, s)
		}
	}
	want := [][2]string{
		{"Atlas: canary at 10% in production", "Loomplane sent 10% of traffic to the new release and is watching its health."},
		{"Atlas: canary at 25% in production", "It held up at 10%, so Loomplane moved it to 25%."},
		{"Atlas: canary at 50% in production", "It held up at 25%, so Loomplane moved it to 50%."},
		{"Atlas: release promoted in production", "Every canary step passed and the new release takes all traffic."},
	}
	if len(cy) != len(want) {
		t.Fatalf("cy sends: %d", len(cy))
	}
	for i, w := range want {
		if cy[i].Env.Title != w[0] || cy[i].Env.Body != w[1] {
			t.Errorf("send %d: %q %q", i, cy[i].Env.Title, cy[i].Env.Body)
		}
	}
	for _, u := range []string{"cy", "bo"} {
		items, err := svc.mailStore.ListNotifications(t.Context(), "app", u, 50, 0)
		if err != nil || len(items) != 1 {
			t.Fatalf("%s inbox: %d %v", u, len(items), err)
		}
	}
	var mailed int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_notification_outbox WHERE kind=$1 AND email_state<>'skipped'`, KindReleaseProgress).Scan(&mailed); err != nil || mailed != 0 {
		t.Fatalf("progress rows that mail: %d %v", mailed, err)
	}
}

func TestRetryCanaryNotifiesAgain(t *testing.T) {
	svc, _ := releaseRig(t, "bo")
	startDeployment(t, svc, "env-a", "cy")
	canary(t, svc, "ramping", 0, 10)
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET retries=1`); err != nil {
		t.Fatal(err)
	}
	canary(t, svc, "pending", -1, 0)
	canary(t, svc, "ramping", 0, 10)
	var n int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_notification_outbox WHERE kind=$1`, KindReleaseProgress).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows (starter and approver, twice): %d %v", n, err)
	}
}

func TestAutomationStarterIsNotARecipient(t *testing.T) {
	svc, _ := releaseRig(t, "loomplane:org-a")
	startDeployment(t, svc, "env-a", "loomplane:org-a")
	canary(t, svc, "ramping", 0, 10)
	canary(t, svc, "rolled_back", 0, 0)
	for _, kind := range []string{KindReleaseRolledBack, KindReleaseProgress} {
		for _, u := range releaseUsers(t, svc, kind) {
			if strings.HasPrefix(u, "loomplane:") {
				t.Fatalf("%s row for %s", kind, u)
			}
		}
	}
	if got := releaseUsers(t, svc, KindReleaseRolledBack); !slices.Equal(got, []string{"ada", "bo"}) {
		t.Fatalf("recipients: %v", got)
	}
	if got := releaseUsers(t, svc, KindReleaseProgress); len(got) != 0 {
		t.Fatalf("progress recipients: %v", got)
	}
}

func TestProductionFailureReachesOwnersAndAdmins(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	for _, u := range []string{"ada", "bo", "cy"} {
		quietPhone(t, svc, gw, u)
	}
	startDeployment(t, svc, "env-p", "cy")
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed'`); err != nil {
		t.Fatal(err)
	}
	if got := releaseUsers(t, svc, KindDeployment); !slices.Equal(got, []string{"ada", "bo", "cy"}) {
		t.Fatalf("recipients: %v", got)
	}
	var pending int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_notification_outbox WHERE kind=$1 AND NOT webhook_done`, KindDeployment).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("rows owing a webhook: %d %v", pending, err)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(gw.taken()) != 3 {
		t.Fatalf("sends: %d", len(gw.taken()))
	}
	var events int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM relay_events WHERE type='deployment.failed'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("webhooks: %d %v", events, err)
	}
}

func TestStagingFailureStaysWithTheStarter(t *testing.T) {
	svc, _ := releaseRig(t, "bo")
	startDeployment(t, svc, "env-a", "cy")
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed'`); err != nil {
		t.Fatal(err)
	}
	if got := releaseUsers(t, svc, KindDeployment); !slices.Equal(got, []string{"cy"}) {
		t.Fatalf("recipients: %v", got)
	}
}

func TestCoveredRollbackSkipsPush(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	quietPhone(t, svc, gw, "cy")
	cover := &coverAll{}
	svc.SetPushCover(cover)
	startDeployment(t, svc, "env-p", "cy")
	canary(t, svc, "ramping", 0, 10)
	canary(t, svc, "rolled_back", 0, 0)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sends := ofKind(gw.taken(), KindReleaseRolledBack); len(sends) != 0 {
		t.Fatalf("sends: %d", len(sends))
	}
	if len(cover.asked) == 0 || cover.asked[0].Kind != KindReleaseRolledBack || cover.asked[0].SubjectID != "deployment-a" || !cover.asked[0].Production {
		t.Fatalf("asked: %+v", cover.asked)
	}
	items, err := svc.mailStore.ListNotifications(t.Context(), "app", "cy", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack := slices.DeleteFunc(items, func(n *inbox.Notification) bool { return n.Type != KindReleaseRolledBack }); len(rolledBack) != 1 {
		t.Fatalf("rolled back inbox items: %d", len(rolledBack))
	}
}

func TestReleaseCopyHasNoEmDashes(t *testing.T) {
	for _, kind := range []string{KindReleaseRolledBack, KindReleaseRevertFailed, KindReleaseProgress} {
		for _, p := range []releasePayload{{Weight: 25}, {Weight: 0}, {Step: 1, Weight: 25, PreviousWeight: 10}, {Status: "promoted"}} {
			title, body := releaseCopy(kind, p, "Atlas", "production")
			if strings.ContainsAny(title+body, "—–") || title == "" || body == "" {
				t.Errorf("%s %+v: %q %q", kind, p, title, body)
			}
		}
	}
}

// A notice must never fail the deployment write that caused it, whatever
// shape the canary JSON takes.
func TestOddCanaryJSONNeverFailsTheWrite(t *testing.T) {
	svc, _ := releaseRig(t, "bo")
	startDeployment(t, svc, "env-a", "cy")
	for _, doc := range []string{
		`{"status":"ramping","steps":null,"step":0,"weight":10}`,
		`{"status":"promoted","steps":null}`,
		`{"status":"ramping","step":1}`,
		`{"status":"rolled_back","steps":"x","step":2}`,
		`{"status":"ramping","steps":{"a":1},"step":0,"weight":null}`,
		`{"status":"revert_failed"}`,
		`{"status":"ramping","step":"first","steps":[10]}`,
		`{"status":null}`,
		`{}`,
		`null`,
		`[]`,
	} {
		if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET canary=$1::jsonb WHERE id='deployment-a'`, doc); err != nil {
			t.Fatalf("canary %s failed the write: %v", doc, err)
		}
	}
}

// failedCanary fails the deployment the way the runner does: the canary
// settles as rolled back, then the deployment is marked failed.
func failedCanary(t *testing.T, svc *Service, step, weight int) {
	t.Helper()
	startDeployment(t, svc, "env-p", "cy")
	if step >= 0 {
		canary(t, svc, "ramping", step, weight)
	}
	canary(t, svc, "rolled_back", step, 0)
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed'`); err != nil {
		t.Fatal(err)
	}
}

// emailsSent counts the emails the user's rows sent, by kind.
func emailsSent(t *testing.T, svc *Service, user string) map[string]int {
	t.Helper()
	rows, err := svc.pg.Query(t.Context(), `SELECT kind FROM loomplane_notification_outbox WHERE user_id=$1 AND email_state='sent'`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var k string
		if err = rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out[k]++
	}
	return out
}

func TestCanaryFailedBeforeTrafficMovedIsOneNotice(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	quietPhone(t, svc, gw, "cy")
	failedCanary(t, svc, -1, 0)
	if got := releaseUsers(t, svc, KindReleaseRolledBack); len(got) != 0 {
		t.Fatalf("a rollback with no traffic moved queued %v", got)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 || sends[0].Env.Data["kind"] != "deployment.failed" || sends[0].Body.CollapseID != collapseID("deployment:deployment-a") {
		t.Fatalf("sends: %+v", sends)
	}
	if got := emailsSent(t, svc, "cy"); got[KindDeployment] != 1 || len(got) != 1 {
		t.Fatalf("emails: %v", got)
	}
}

func TestCanaryRolledBackAfterTrafficMovedIsOneNoticeAndTellsTheTruth(t *testing.T) {
	svc, gw := releaseRig(t, "bo")
	quietPhone(t, svc, gw, "cy")
	failedCanary(t, svc, 1, 25)
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The ramp's own progress push came first; the failure is one more.
	if failed := ofKind(gw.taken(), "deployment.failed"); len(failed) != 0 {
		t.Fatalf("the deployment's own failure pushed too: %d", len(failed))
	}
	sends := ofKind(gw.taken(), KindReleaseRolledBack)
	if len(sends) != 1 {
		t.Fatalf("sends: %+v", sends)
	}
	body := sends[0].Env.Body
	if !strings.Contains(body, "failed at canary step 2 (25% of traffic)") || !strings.Contains(body, "Reason: error rate 9%") || strings.Contains(body, "health check") {
		t.Fatalf("copy: %q", body)
	}
	if got := emailsSent(t, svc, "cy"); got[KindReleaseRolledBack] != 1 || len(got) != 1 {
		t.Fatalf("emails: %v", got)
	}
	var state, email string
	if err := svc.pg.QueryRow(t.Context(), `SELECT push_state,email_state FROM loomplane_notification_outbox WHERE user_id='cy' AND kind=$1`, KindDeployment).Scan(&state, &email); err != nil || state != PushSkipped || email != "skipped" {
		t.Fatalf("deployment row: %s %s %v", state, email, err)
	}
	items, err := svc.mailStore.ListNotifications(t.Context(), "app", "cy", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if failed := slices.DeleteFunc(items, func(n *inbox.Notification) bool { return n.Type != "deployment.failed" }); len(failed) != 1 {
		t.Fatalf("the failure keeps its inbox item: %d", len(failed))
	}
}

func TestRollbackCopyNamesTheStepNotAHealthCheck(t *testing.T) {
	_, body := releaseCopy(KindReleaseRolledBack, releasePayload{Step: 0, Weight: 10}, "Atlas", "production")
	if !strings.Contains(body, "step 1 (10% of traffic)") || strings.Contains(body, "health check") {
		t.Fatalf("body: %q", body)
	}
	_, body = releaseCopy(KindReleaseRevertFailed, releasePayload{Step: -1, Reason: "boom"}, "Atlas", "production")
	if strings.Contains(body, "health check") || !strings.HasSuffix(body, "Reason: boom") {
		t.Fatalf("body: %q", body)
	}
}

func TestAdminCopyOfAProductionFailureDoesNotClaimTheyStartedIt(t *testing.T) {
	svc, _ := releaseRig(t, "bo")
	startDeployment(t, svc, "env-p", "cy")
	if _, err := svc.pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed'`); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"cy": "Your deployment failed.", "ada": "A deployment in your organization failed."} {
		var raw []byte
		var id string
		if err := svc.pg.QueryRow(t.Context(), `SELECT id,payload FROM loomplane_notification_outbox WHERE user_id=$1 AND kind=$2`, user, KindDeployment).Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		d, err := deploymentMessage(&outboxItem{id: id, wire: raw})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(d.emailBody, want) {
			t.Errorf("%s: %q", user, d.emailBody)
		}
	}
}
