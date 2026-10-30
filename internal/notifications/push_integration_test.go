//go:build integration

package notifications

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/herald/credential"
	"github.com/xraph/herald/driver"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func pushProviders(t *testing.T, svc *Service) []string {
	t.Helper()
	key, err := security.DeriveKey(svc.cfg.SecretKey, "loomplane/herald credentials v1")
	if err != nil {
		t.Fatal(err)
	}
	box, err := credential.NewCipher(credential.Key{ID: "lp1", Bytes: key})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.mailStore.ListAllProviders(t.Context(), svc.cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range list {
		if p.Name == "loomplane-push" {
			if p.Driver != "sealed" || p.Channel != "push" {
				t.Fatalf("provider: %#v", p)
			}
			seed, err := box.Decrypt(p.ID.String(), "signing_key", p.Credentials["signing_key"])
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, seed)
		}
	}
	return out
}

func pushService(t *testing.T, db *grove.DB, secret []byte, migrate bool) *Service {
	t.Helper()
	cfg := config.Config{SecretKey: base64.StdEncoding.EncodeToString(secret), Identity: config.IdentityConfig{AppID: "app"}, Push: config.PushConfig{Enabled: true, GatewayURL: "https://push.example.com"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if migrate {
		if err = svc.mailStore.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPreparePushUpsertsOneProvider(t *testing.T) {
	db := notificationDatabase(t)
	svc := pushService(t, db, make([]byte, 32), true)
	if err := svc.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	list, err := svc.mailStore.ListAllProviders(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range list {
		if p.Name == "loomplane-push" {
			n++
			if p.Driver != "sealed" || p.Settings["gateway_url"] != "https://push.example.com" || !p.Enabled {
				t.Fatalf("provider: %#v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d push providers", n)
	}
}

func TestRotatedSecretMovesKeyIDAndProvider(t *testing.T) {
	db := notificationDatabase(t)
	before, err := pushService(t, db, make([]byte, 32), true).PushConfig(t.Context(), security.Principal{UserID: "user-a"})
	if err != nil || before.KeyID != "8029410f9a15b4ff" {
		t.Fatalf("before: %#v %v", before, err)
	}
	other := make([]byte, 32)
	other[0] = 7
	rotated := pushService(t, db, other, false)
	after, err := rotated.PushConfig(t.Context(), security.Principal{UserID: "user-a"})
	if err != nil || after.KeyID == before.KeyID {
		t.Fatalf("after: %#v %v", after, err)
	}
	seeds := pushProviders(t, rotated)
	if len(seeds) != 1 {
		t.Fatalf("%d push providers", len(seeds))
	}
	seed, err := serverKey(rotated.cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0] != base64.StdEncoding.EncodeToString(seed.Seed()) {
		t.Fatal("provider kept the old signing key")
	}
}

// pushRig is a service with push on, one member (user-a) in org-a, a project
// and a staging and a production environment, and a fake gateway.
func pushRig(t *testing.T) (*Service, *fakeGateway, *allowedProjects, <-chan string) {
	t.Helper()
	db := notificationDatabase(t)
	ctx := t.Context()
	gw := newFakeGateway(t)
	host, port, messages := smtpSink(t)
	cfg := config.Config{PublicURL: "https://loomplane.example.com/", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"},
		Notifications: config.NotificationsConfig{SMTPHost: host, SMTPPort: port, From: "loomplane@example.com"}, Push: config.PushConfig{Enabled: true, GatewayURL: gw.URL}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.mailStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.registerEventTypes(ctx); err != nil {
		t.Fatal(err)
	}
	authz := &allowedProjects{}
	svc.SetAuthorizer(authz)
	for _, q := range []string{
		`UPDATE loomplane_projects SET name='Atlas' WHERE id='project-a'`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind)VALUES('env-a','org-a','project-a','staging','staging'),('env-p','org-a','project-a','production','production')`,
		`INSERT INTO authsome_organizations(id,name)VALUES('org-a','Acme')`,
	} {
		if _, err = svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return svc, gw, authz, messages
}

func failDeployment(t *testing.T, svc *Service, env string) {
	t.Helper()
	if _, err := svc.pg.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,plan_id,started_by,status)VALUES('deployment-a','org-a','project-a',$1,'plan-a','user-a','failed')`, env); err != nil {
		t.Fatal(err)
	}
}

type pushRow struct {
	state, collapse, emailState, inboxID, lastError string
	devices                                         map[string]string
	inboxDone, complete                             bool
}

func userRow(t *testing.T, svc *Service) pushRow {
	t.Helper()
	var r pushRow
	var raw []byte
	err := svc.pg.QueryRow(t.Context(), `SELECT push_state,push_collapse,email_state,inbox_id,last_error,push_devices,inbox_done,completed_at IS NOT NULL FROM loomplane_notification_outbox WHERE user_id='user-a'`).
		Scan(&r.state, &r.collapse, &r.emailState, &r.inboxID, &r.lastError, &raw, &r.inboxDone, &r.complete)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &r.devices); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDeploymentFailurePushesASealedEnvelope(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 {
		t.Fatalf("gateway sends: %d", len(sends))
	}
	send := sends[0]
	if send.Body.Urgency != "active" || send.Body.CollapseID != collapseID("deployment:deployment-a") || send.Body.Kind != "show" || send.Body.TTLSeconds != 43200 {
		t.Fatalf("send: %+v", send.Body)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	env := send.Env
	if env.Title != "Atlas: deployment to staging failed" || env.Category != "alert" || env.Thread != "project-a" {
		t.Fatalf("envelope: %+v", env)
	}
	for key, want := range map[string]string{"kind": "deployment.failed", "class": "alert", "actions": "open,retry", "inbox_id": list.Items[0].ID, "subject": "deployment-a", "org": "org-a", "org_name": "Acme",
		"server": "https://loomplane.example.com", "path": "/deployments/plan-a", "project": "project-a", "environment": "staging", "urgency": "active"} {
		if env.Data[key] != want {
			t.Errorf("data[%s] = %q, want %q", key, env.Data[key], want)
		}
	}
	if row := userRow(t, svc); row.state != PushSent || row.collapse != send.Body.CollapseID || row.inboxID != list.Items[0].ID || !row.complete {
		t.Fatalf("row: %+v", row)
	}
}

func TestLevelAndToggleFiltering(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	a := gw.register(t, svc, "h1.a")
	b := gw.register(t, svc, "h1.b")
	if _, err := svc.UpdateDevice(ctx, security.Principal{UserID: "user-a", OrgID: "org-a"}, a.ID, UpdateDeviceInput{Settings: map[string]string{"level.staging": "off"}}); err != nil {
		t.Fatal(err)
	}
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 1 || got[0] != "h1.b" {
		t.Fatalf("gateway sends: %v", got)
	}
	row := userRow(t, svc)
	if len(row.devices) != 1 || row.devices[b.ID] != "sent" || row.state != PushSent {
		t.Fatalf("row: %+v", row)
	}
}

func TestQuietHoursHoldBackNonCritical(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	a := gw.register(t, svc, "h1.a")
	now := time.Now().UTC()
	quiet := QuietHours{Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04")}
	if _, err := svc.UpdateDevice(ctx, security.Principal{UserID: "user-a", OrgID: "org-a"}, a.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true, Value: &quiet}}); err != nil {
		t.Fatal(err)
	}
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 0 {
		t.Fatalf("gateway sends: %v", got)
	}
	if row := userRow(t, svc); row.devices[a.ID] != "suppressed" || row.state != PushSkipped {
		t.Fatalf("row: %+v", row)
	}
}

func TestPushRetryReachesOnlyFailedDevices(t *testing.T) {
	svc, gw, _, messages := pushRig(t)
	ctx := t.Context()
	a := gw.register(t, svc, "h1.a")
	b := gw.register(t, svc, "h1.b")
	gw.answer("h1.b", 500)
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 2 {
		t.Fatalf("first drain sends: %v", got)
	}
	row := userRow(t, svc)
	if row.complete || row.state != PushPending || row.lastError != "push delivery failed" || row.devices[a.ID] != "sent" || row.devices[b.ID] != "failed" || row.emailState != "sent" {
		t.Fatalf("after the first drain: %+v", row)
	}
	if _, err := svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 3 || got[2] != "h1.b" {
		t.Fatalf("after the retry: %v", got)
	}
	row = userRow(t, svc)
	if !row.complete || row.state != PushSent || row.emailState != "sent" || row.devices[b.ID] != "sent" {
		t.Fatalf("after the retry: %+v", row)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if len(messages) != 1 {
		t.Fatalf("emails: %d", len(messages))
	}
}

func TestGoneDeviceIsDeleted(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	gw.answer("h1.a", 410)
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := svc.herald.ListDevices(ctx, "app", "user-a")
	if err != nil || len(left) != 0 {
		t.Fatalf("devices: %v %v", left, err)
	}
	if row := userRow(t, svc); !row.complete || row.state != PushSkipped {
		t.Fatalf("row: %+v", row)
	}
}

func TestLostPermissionStopsPushes(t *testing.T) {
	svc, gw, authz, _ := pushRig(t)
	gw.register(t, svc, "h1.a")
	authz.denied.Store(true)
	failDeployment(t, svc, "env-a")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 0 {
		t.Fatalf("gateway sends: %v", got)
	}
	if row := userRow(t, svc); row.state != PushSkipped || !row.inboxDone {
		t.Fatalf("row: %+v", row)
	}
}

func TestPushOffSkipsThePushStep(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	gw.register(t, svc, "h1.a")
	svc.cfg.Push.Enabled = false
	failDeployment(t, svc, "env-a")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 0 {
		t.Fatalf("gateway sends: %v", got)
	}
	if row := userRow(t, svc); row.state != PushSkipped || !row.complete {
		t.Fatalf("row: %+v", row)
	}
}

type coverAll struct{ asked []CoverQuery }

func (c *coverAll) Covers(_ context.Context, q CoverQuery) (bool, error) {
	c.asked = append(c.asked, q)
	return true, nil
}

func TestCoveredPushKeepsInboxAndEmail(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	gw.register(t, svc, "h1.a")
	cover := &coverAll{}
	svc.SetPushCover(cover)
	failDeployment(t, svc, "env-p")
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := gw.handles(); len(got) != 0 {
		t.Fatalf("gateway sends: %v", got)
	}
	want := CoverQuery{OrgID: "org-a", Kind: "deployment.failed", SubjectID: "deployment-a", ProjectID: "project-a", EnvironmentID: "env-p", Production: true}
	if len(cover.asked) != 1 || cover.asked[0] != want {
		t.Fatalf("cover asked: %+v", cover.asked)
	}
	row := userRow(t, svc)
	if row.state != PushSkipped || row.emailState != "sent" || row.inboxID == "" {
		t.Fatalf("row: %+v", row)
	}
}

func TestWorkspaceNoticePushes(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	svc.SetAuthorizer(cloudManagers{"user-a": true})
	gw.register(t, svc, "h1.a")
	if err := svc.EnqueueRenewalNotice(t.Context(), RenewalNotice{OrgID: "org-a", RegistrationID: "reg_1", Domain: "acme.dev", RenewsOn: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 {
		t.Fatalf("gateway sends: %d", len(sends))
	}
	if d := sends[0].Env.Data; d["class"] != "workspace" || d["urgency"] != "active" || d["kind"] != "domain.renewal" || sends[0].Env.Category != "update" || sends[0].Body.Urgency != "active" {
		t.Fatalf("envelope: %+v", sends[0].Env)
	}
}

func TestGenericNoticePushOnlyWritesNoInbox(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	gw.register(t, svc, "h1.a")
	err := Enqueue(t.Context(), svc.pg, Notice{Kind: KindAgentReport, OrgID: "org-a", ProjectID: "project-a", SubjectID: "report_1:taken", Recipients: []string{"user-a"},
		Title: "Ada took the report", Body: "Ada took over.", Path: "/agent/reports/report_1", PushOnly: true,
		Push: &Push{Class: ClassUpdate, Urgency: driver.UrgencyPassive, Category: CategoryUpdate, Actions: []string{ActionOpen}, Collapse: "report:report_1", Production: true, Data: map[string]string{"taken_by": "Ada"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	sends := gw.taken()
	if len(sends) != 1 {
		t.Fatalf("gateway sends: %d", len(sends))
	}
	data := sends[0].Env.Data
	if id, ok := data["inbox_id"]; !ok || id != "" || data["taken_by"] != "Ada" || data["kind"] != KindAgentReport || sends[0].Body.CollapseID != collapseID("report:report_1") {
		t.Fatalf("envelope: %+v", sends[0])
	}
	list, err := svc.List(t.Context(), security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if row := userRow(t, svc); row.emailState != "skipped" || row.state != PushSent || row.inboxID != "" || !row.complete {
		t.Fatalf("row: %+v", row)
	}
}

func TestBrokenPushProviderStillSendsEmailOnce(t *testing.T) {
	svc, gw, _, messages := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	list, err := svc.mailStore.ListAllProviders(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Name == "loomplane-push" {
			if err = svc.herald.DeleteProvider(ctx, "app", p.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	failDeployment(t, svc, "env-a")
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	row := userRow(t, svc)
	if row.complete || row.state != PushPending || row.lastError != "push delivery failed" || row.emailState != "sent" || len(messages) != 1 || len(gw.handles()) != 0 {
		t.Fatalf("after the broken send: %+v, %d emails", row, len(messages))
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	row = userRow(t, svc)
	if !row.complete || row.state != PushSent || len(messages) != 1 || len(gw.handles()) != 1 {
		t.Fatalf("after the retry: %+v, %d emails, %d sends", row, len(messages), len(gw.handles()))
	}
}

func asUserA() security.Principal { return security.Principal{UserID: "user-a", OrgID: "org-a"} }

func TestTestPushReachesOnlyThatDevice(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	gw.register(t, svc, "h1.a")
	b := gw.register(t, svc, "h1.b")
	got, err := svc.TestPush(t.Context(), asUserA(), b.ID)
	if err != nil || got.Status != "sent" {
		t.Fatalf("result: %+v %v", got, err)
	}
	sends := gw.taken()
	if len(sends) != 1 || sends[0].Handle != "h1.b" {
		t.Fatalf("gateway sends: %v", gw.handles())
	}
	env := sends[0].Env
	if env.Title != "Test notification" || env.Body != "Push notifications work on this phone." || env.Category != "update" || sends[0].Body.CollapseID != collapseID("notice:test:"+b.ID) || env.Data["kind"] != "test" || env.Data["path"] != "/notifications" {
		t.Fatalf("send: %+v", sends[0])
	}
}

func TestTestPushInQuietHours(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	a := gw.register(t, svc, "h1.a")
	now := time.Now().UTC()
	quiet := QuietHours{Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04")}
	if _, err := svc.UpdateDevice(t.Context(), asUserA(), a.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true, Value: &quiet}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.TestPush(t.Context(), asUserA(), a.ID)
	if err != nil || got.Status != "quiet_hours" || len(gw.taken()) != 0 {
		t.Fatalf("result: %+v %v, sends %d", got, err, len(gw.taken()))
	}
}

func TestTestPushTwiceIsTooSoon(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	a := gw.register(t, svc, "h1.a")
	if _, err := svc.TestPush(t.Context(), asUserA(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TestPush(t.Context(), asUserA(), a.ID); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("second: %v", err)
	}
}

func TestTestPushOnSomeoneElsesDevice(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	a := gw.register(t, svc, "h1.a")
	if _, err := svc.TestPush(t.Context(), security.Principal{UserID: "user-b", OrgID: "org-a"}, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err: %v", err)
	}
}

func TestMarkReadWithdrawsFromPhonesThatGotIt(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	gw.register(t, svc, "h1.b")
	c := gw.register(t, svc, "h1.c")
	gw.registerFor(t, svc, "user-b", "h1.other")
	if _, err := svc.UpdateDevice(ctx, asUserA(), c.ID, UpdateDeviceInput{Settings: map[string]string{"level.staging": "off"}}); err != nil {
		t.Fatal(err)
	}
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	row := userRow(t, svc)
	if row.state != PushSent || len(row.devices) != 2 || row.devices[c.ID] != "" {
		t.Fatalf("row: %+v", row)
	}
	all, err := svc.herald.ListDevices(ctx, "app", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, d := range all {
		if row.devices[d.ID] == "sent" {
			want = append(want, d.Address)
		}
	}
	slices.Sort(want)
	before := len(gw.taken())
	list, err := svc.List(ctx, asUserA())
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if err = svc.MarkRead(ctx, asUserA(), list.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(gw.taken()) < before+len(want) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err = svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	sent := gw.taken()[before:]
	got := make([]string, 0, len(sent))
	for _, s := range sent {
		if s.Body.Kind != "withdraw" || s.Body.CollapseID != collapseID("deployment:deployment-a") || s.Body.Sealed != "" {
			t.Fatalf("withdraw: %+v", s.Body)
		}
		got = append(got, s.Handle)
	}
	slices.Sort(got)
	if len(want) != 2 || !slices.Equal(got, want) {
		t.Fatalf("withdrew from %v, want %v", got, want)
	}
}

func TestMarkReadOfUnpushedNoticeSendsNothing(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	if _, err := svc.UpdateDevice(ctx, asUserA(), svc.firstDevice(t), UpdateDeviceInput{Settings: map[string]string{"level.staging": "off"}}); err != nil {
		t.Fatal(err)
	}
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, asUserA())
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if err = svc.MarkRead(ctx, asUserA(), list.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if row := userRow(t, svc); row.state != PushSkipped || len(gw.taken()) != 0 {
		t.Fatalf("row %+v, sends %d", row, len(gw.taken()))
	}
}

func (s *Service) firstDevice(t *testing.T) string {
	t.Helper()
	list, err := s.herald.ListDevices(t.Context(), "app", "user-a")
	if err != nil || len(list) == 0 {
		t.Fatalf("devices: %v %v", list, err)
	}
	return list[0].ID
}

func TestTestPushWithPushOff(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	a := gw.register(t, svc, "h1.a")
	svc.cfg.Push.Enabled = false
	if _, err := svc.TestPush(t.Context(), asUserA(), a.ID); !errors.Is(err, ErrPushDisabled) || len(gw.taken()) != 0 {
		t.Fatalf("err: %v, sends %d", err, len(gw.taken()))
	}
}

func TestTestPushGatewayRefusal(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	a := gw.register(t, svc, "h1.a")
	gw.answer("h1.a", 500)
	if _, err := svc.TestPush(t.Context(), asUserA(), a.ID); !errors.Is(err, ErrPushFailed) {
		t.Fatalf("err: %v", err)
	}
}

func TestInboxRowAppendsNotificationCreated(t *testing.T) {
	svc, _, _, _ := pushRig(t)
	ctx := t.Context()
	failDeployment(t, svc, "env-a")
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	var n int
	var org, entity, data string
	if err = svc.pg.QueryRow(ctx, `SELECT count(*),coalesce(max(org_id),''),coalesce(max(entity_id),''),coalesce(max(data::text),'') FROM test_events WHERE type='notification.created'`).Scan(&n, &org, &entity, &data); err != nil {
		t.Fatal(err)
	}
	if n != 1 || org != "org-a" || entity != list.Items[0].ID {
		t.Fatalf("events: %d %s %s", n, org, entity)
	}
	var payload map[string]string
	if err = json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != list.Items[0].ID || payload["org_id"] != "org-a" || payload["user_id"] != "user-a" || len(payload) != 3 {
		t.Fatalf("payload: %v", payload)
	}
	// A recipient who got no inbox item adds no event.
	err = Enqueue(ctx, svc.pg, Notice{Kind: KindAgentReport, OrgID: "org-a", ProjectID: "project-a", SubjectID: "report_1:taken", Recipients: []string{"user-a"},
		Title: "Ada took the report", Body: "Ada took over.", Path: "/agent/reports/report_1", PushOnly: true,
		Push: &Push{Class: ClassUpdate, Urgency: driver.UrgencyPassive, Category: CategoryUpdate, Actions: []string{ActionOpen}, Collapse: "report:report_1", Production: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM test_events WHERE type='notification.created'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("push-only notice appended an event: %d %v", n, err)
	}
}

func TestHungGatewayNeverDelaysTheEmail(t *testing.T) {
	svc, gw, _, messages := pushRig(t)
	ctx := t.Context()
	old := pushStepTimeout
	pushStepTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pushStepTimeout = old })
	release := gw.hang()
	t.Cleanup(release)
	gw.register(t, svc, "h1.a")
	gw.register(t, svc, "h1.b")
	failDeployment(t, svc, "env-a")
	start := time.Now()
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the drain took %s", took)
	}
	row := userRow(t, svc)
	if row.complete || row.state != PushPending || row.emailState != "sent" || row.lastError != "push delivery failed" || len(messages) != 1 {
		t.Fatalf("after the hang: %+v, %d emails", row, len(messages))
	}
	release()
	if _, err := svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	row = userRow(t, svc)
	if !row.complete || row.state != PushSent || len(messages) != 1 {
		t.Fatalf("after the retry: %+v, %d emails", row, len(messages))
	}
}

func TestRetryExhaustedSkipsPushesOlderThanTheirTTL(t *testing.T) {
	svc, gw, _, _ := pushRig(t)
	ctx := t.Context()
	gw.register(t, svc, "h1.a")
	failDeployment(t, svc, "env-a")
	if _, err := svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET push_state='failed',attempts=10,created_at=clock_timestamp()-interval '13 hours'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pg.Exec(ctx, `INSERT INTO authsome_members(org_id,user_id,role)VALUES('org-a','user-a','owner') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RetryExhausted(ctx, asUserA()); err != nil {
		t.Fatal(err)
	}
	if row := userRow(t, svc); row.state != PushSkipped {
		t.Fatalf("an old failed push: %+v", row)
	}
	if _, err := svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET push_state='failed',attempts=10,created_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RetryExhausted(ctx, asUserA()); err != nil {
		t.Fatal(err)
	}
	if row := userRow(t, svc); row.state != PushPending {
		t.Fatalf("a fresh failed push: %+v", row)
	}
}
