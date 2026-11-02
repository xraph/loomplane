//go:build integration

package notifications

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// needDenied refuses one named permission for the listed users and allows
// everything else.
type needDenied struct {
	name  string
	users map[string]bool
}

func (n needDenied) Require(_ context.Context, p security.Principal, permission security.Permission, _ string) error {
	if permission.Name() == n.name && n.users[p.UserID] {
		return security.ErrForbidden
	}
	return nil
}

// noticeService is domainNoticeService with user-b in org-a as well, and an
// authorizer that allows everything.
func noticeService(t *testing.T) (*Service, <-chan string) {
	t.Helper()
	svc, messages := domainNoticeService(t)
	svc.SetAuthorizer(&allowedProjects{})
	if _, err := svc.pg.Exec(t.Context(), `INSERT INTO authsome_members(org_id,user_id)VALUES('org-a','user-b')`); err != nil {
		t.Fatal(err)
	}
	return svc, messages
}

func requestNotice() Notice {
	return Notice{Kind: KindAgentRequest, OrgID: "org-a", ProjectID: "project-a", SubjectID: "areq_1", Recipients: []string{"user-a", "user-b"},
		Title: "Ada asked", Body: "Ada asked the agent to scale the API.", Path: "/agent/requests?id=areq_1", Metadata: map[string]string{"request_id": "areq_1"}}
}

func outboxCount(t *testing.T, svc *Service, where string) int {
	t.Helper()
	var n int
	if err := svc.pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_notification_outbox WHERE `+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeploymentNotificationsAreUnchanged(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,plan_id,started_by,status)VALUES('deployment-a','org-a','project-a','env-a','plan-a','user-a','running')`,
		`UPDATE loomplane_deployments SET status='succeeded' WHERE id='deployment-a'`,
	} {
		if _, err := svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, svc, `kind='deployment' AND deployment_id='deployment-a' AND subject_id=''`); n != 1 {
		t.Fatalf("deployment rows: %d", n)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if item := list.Items[0]; item.Title != "Deployment succeeded" || item.ActionURL != "https://loomplane.example.com/deployments/plan-a" || item.ProjectID != "project-a" || item.Status != "succeeded" {
		t.Fatalf("inbox item: %+v", item)
	}
	var eventType string
	if err = svc.pg.QueryRow(ctx, `SELECT type FROM relay_events`).Scan(&eventType); err != nil || eventType != "deployment.succeeded" {
		t.Fatalf("webhook: %s %v", eventType, err)
	}
}

func TestEnqueueWritesOneRowPerRecipient(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	n := requestNotice()
	n.Recipients = []string{"user-a", "user-b", "user-a", ""}
	for range 2 {
		if err := Enqueue(ctx, svc.pg, n); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	rows, err := svc.pg.Query(ctx, `SELECT id FROM loomplane_notification_outbox WHERE kind='agent.request' AND id=deployment_id AND attempt=0 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if !slices.Equal(ids, []string{"agent.request:areq_1:user-a", "agent.request:areq_1:user-b"}) {
		t.Fatalf("outbox rows: %v", ids)
	}
	if n := outboxCount(t, svc, `subject_id='areq_1'`); n != 2 {
		t.Fatalf("subject rows: %d", n)
	}
}

func TestEnqueueRejectsInvalidNotices(t *testing.T) {
	svc, _ := noticeService(t)
	for name, change := range map[string]func(*Notice){
		"no kind":       func(n *Notice) { n.Kind = "" },
		"deployment":    func(n *Notice) { n.Kind = KindDeployment },
		"no org":        func(n *Notice) { n.OrgID = "" },
		"no subject":    func(n *Notice) { n.SubjectID = "" },
		"relative path": func(n *Notice) { n.Path = "agent" },
		"long title":    func(n *Notice) { n.Title = strings.Repeat("é", 201) },
		"long body":     func(n *Notice) { n.Body = strings.Repeat("é", 2001) },
	} {
		n := requestNotice()
		change(&n)
		if err := Enqueue(t.Context(), svc.pg, n); err == nil || !strings.Contains(err.Error(), ErrInvalid.Error()) {
			t.Errorf("%s: %v", name, err)
		}
	}
	long := requestNotice()
	long.Title, long.Body = strings.Repeat("é", 200), strings.Repeat("é", 2000)
	if err := Enqueue(t.Context(), svc.pg, long); err != nil {
		t.Fatal("limits are inclusive:", err)
	}
	if n := outboxCount(t, svc, `kind<>'deployment'`); n != 2 {
		t.Fatalf("rows after rejections: %d", n)
	}
}

func TestNoticeDeliversInboxWebhookAndEmail(t *testing.T) {
	svc, messages := noticeService(t)
	ctx := t.Context()
	n := requestNotice()
	n.Recipients = []string{"user-a"}
	if err := Enqueue(ctx, svc.pg, n); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if item := list.Items[0]; item.Title != "Ada asked" || item.Body != "Ada asked the agent to scale the API." || item.ActionURL != "https://loomplane.example.com/agent/requests?id=areq_1" || item.ProjectID != "project-a" || item.EmailStatus != "sent" {
		t.Fatalf("inbox item: %+v", item)
	}
	var eventType string
	if err = svc.pg.QueryRow(ctx, `SELECT type FROM relay_events`).Scan(&eventType); err != nil || eventType != "agent.request.created" {
		t.Fatalf("webhook: %s %v", eventType, err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "Subject: Loomplane: Ada asked") || !strings.Contains(body, "Open it: https://loomplane.example.com/agent/requests?id=areq_1") {
			t.Fatalf("email: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("email was not delivered")
	}
}

func TestNoticeSkipsPeopleWhoLeft(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	if err := Enqueue(ctx, svc.pg, requestNotice()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pg.Exec(ctx, `DELETE FROM authsome_members WHERE org_id='org-a' AND user_id='user-b'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var inboxDone bool
	var emailState string
	if err := svc.pg.QueryRow(ctx, `SELECT inbox_done,email_state FROM loomplane_notification_outbox WHERE user_id='user-b'`).Scan(&inboxDone, &emailState); err != nil {
		t.Fatal(err)
	}
	if !inboxDone || emailState != "skipped" {
		t.Fatalf("user who left: inbox_done=%t email=%s", inboxDone, emailState)
	}
	var items int
	if err := svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox WHERE user_id='user-b'`).Scan(&items); err != nil || items != 0 {
		t.Fatalf("inbox items for user who left: %d %v", items, err)
	}
	if err := svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox WHERE user_id='user-a'`).Scan(&items); err != nil || items != 1 {
		t.Fatalf("inbox items for user-a: %d %v", items, err)
	}
}

func TestNoticeHonoursAgentEmailPreference(t *testing.T) {
	svc, messages := noticeService(t)
	ctx := t.Context()
	p := security.Principal{OrgID: "org-a", UserID: "user-a"}
	off := false
	prefs, err := svc.SetPreferences(ctx, p, PreferenceInput{AgentEmail: &off})
	if err != nil {
		t.Fatal(err)
	}
	if prefs.AgentEmail || !prefs.DeploymentEmail {
		t.Fatalf("a partial update changed the other field: %+v", prefs)
	}
	n := requestNotice()
	n.Recipients = []string{"user-a"}
	if err = Enqueue(ctx, svc.pg, n); err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var inboxDone bool
	var emailState string
	if err = svc.pg.QueryRow(ctx, `SELECT inbox_done,email_state FROM loomplane_notification_outbox WHERE user_id='user-a'`).Scan(&inboxDone, &emailState); err != nil {
		t.Fatal(err)
	}
	if !inboxDone || emailState != "skipped" {
		t.Fatalf("agent_email off: inbox_done=%t email=%s", inboxDone, emailState)
	}
	select {
	case body := <-messages:
		t.Fatalf("email sent with agent_email off: %s", body)
	default:
	}
	got, err := svc.Preferences(ctx, p)
	if err != nil || got.AgentEmail || !got.DeploymentEmail {
		t.Fatalf("preferences: %+v %v", got, err)
	}
	// The other field alone leaves agent_email as it was.
	if got, err = svc.SetPreferences(ctx, p, PreferenceInput{DeploymentEmail: &off}); err != nil || got.AgentEmail || got.DeploymentEmail {
		t.Fatalf("preferences after deployment_email off: %+v %v", got, err)
	}
	fresh, err := svc.Preferences(ctx, security.Principal{OrgID: "org-a", UserID: "user-c"})
	if err != nil || !fresh.AgentEmail || !fresh.DeploymentEmail {
		t.Fatalf("defaults: %+v %v", fresh, err)
	}
}

func TestWorkspaceNoticeShowsInTheInbox(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	n := requestNotice()
	n.ProjectID = ""
	if err := Enqueue(ctx, svc.pg, n); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	p := security.Principal{OrgID: "org-a", UserID: "user-a"}
	list, err := svc.List(ctx, p)
	if err != nil || len(list.Items) != 1 || list.Items[0].ProjectID != "" || list.Items[0].Title != "Ada asked" {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if err = svc.MarkRead(ctx, p, list.Items[0].ID); err != nil {
		t.Fatal("mark read:", err)
	}
}

func TestNoticeNeedIsCheckedAtDelivery(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	svc.SetAuthorizer(needDenied{name: "agent:use", users: map[string]bool{"user-b": true}})
	n := requestNotice()
	n.Need = "agent:use"
	if err := Enqueue(ctx, svc.pg, n); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"user-a": "sent", "user-b": "skipped"} {
		var state string
		if err := svc.pg.QueryRow(ctx, `SELECT email_state FROM loomplane_notification_outbox WHERE user_id=$1`, user).Scan(&state); err != nil || state != want {
			t.Fatalf("%s email_state=%q %v, want %q", user, state, err, want)
		}
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-b"})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("user-b sees: %+v %v", list, err)
	}
}

func TestConnectionNoticesStillQueueAfterTheMigration(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	for range 2 {
		if err := svc.ConnectionLapsed(ctx, "org-a", "conn-1", "Prod", "aws", "user-a", at); err != nil {
			t.Fatal(err)
		}
	}
	// user-a (maker and admin), user-b and user-c are admins of org-a.
	if n := outboxCount(t, svc, `kind='connection'`); n != 3 {
		t.Fatalf("connection rows: %d", n)
	}
}

func TestAdminsListsOwnersAndAdmins(t *testing.T) {
	svc, _ := noticeService(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO authsome_members(org_id,user_id,role)VALUES('org-z','u-viewer','viewer'),('org-z','u-member','member'),('org-z','u-owner','owner'),('org-z','u-admin','admin'),('org-y','u-other','owner')`,
	} {
		if _, err := svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Admins(ctx, svc.pg, "org-z")
	if err != nil || !slices.Equal(got, []string{"u-admin", "u-owner"}) {
		t.Fatalf("admins: %v %v", got, err)
	}
	if got, err = Admins(ctx, svc.pg, "org-none"); err != nil || len(got) != 0 {
		t.Fatalf("no members: %v %v", got, err)
	}
}
