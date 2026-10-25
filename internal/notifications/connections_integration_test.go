//go:build integration

package notifications

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func TestConnectionLapseNotifiesItsMakerAndAdmins(t *testing.T) {
	db := notificationDatabase(t)
	ctx := t.Context()
	cfg := config.Config{PublicURL: "https://loomplane.example.com", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}}
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
	svc.SetAuthorizer(&allowedProjects{})
	if svc.relay, err = relay.New(relay.WithStore(svc.relayStore)); err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.registerEventTypes(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email)VALUES('owner-a','app','a@example.com'),('admin-b','app','b@example.com'),('dev-c','app','c@example.com'),('dev-d','app','d@example.com')`,
		`INSERT INTO authsome_members(org_id,user_id,role)VALUES('org-c','owner-a','owner'),('org-c','admin-b','admin'),('org-c','dev-c','member'),('org-c','dev-d','member')`,
	} {
		if _, err = svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	for range 2 {
		if err = svc.ConnectionLapsed(ctx, "org-c", "conn-1", "Prod DigitalOcean", "digitalocean", "dev-c", at); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := svc.pg.Query(ctx, `SELECT user_id,kind,project_id,attempt,payload::text FROM loomplane_notification_outbox WHERE org_id='org-c' ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for rows.Next() {
		var user, kind, project, payload string
		var attempt int
		if err = rows.Scan(&user, &kind, &project, &attempt, &payload); err != nil {
			t.Fatal(err)
		}
		if kind != "connection" || project != "" || attempt != 0 {
			t.Fatalf("row for %s: kind=%q project=%q attempt=%d", user, kind, project, attempt)
		}
		if !strings.Contains(payload, `"connection_id"`) || !strings.Contains(payload, "sign_in_needed") {
			t.Fatalf("payload: %s", payload)
		}
		users = append(users, user)
	}
	rows.Close()
	if want := []string{"admin-b", "dev-c", "owner-a"}; !slices.Equal(users, want) {
		t.Fatalf("recipients %v, want %v", users, want)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE completed_at IS NULL`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("undelivered rows: %d %v", pending, err)
	}
	for _, user := range users {
		list, listErr := svc.List(ctx, security.Principal{OrgID: "org-c", UserID: user})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(list.Items) != 1 {
			t.Fatalf("%s inbox: %+v", user, list)
		}
		n := list.Items[0]
		if n.Title != "Prod DigitalOcean needs you to sign in again" || n.ActionURL != "https://loomplane.example.com/clouds/accounts/conn-1" {
			t.Fatalf("%s notice: %+v", user, n)
		}
	}
	// Three recipients, but one lapse: subscribers get one event.
	var receipts int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM relay_acceptances`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("webhook receipts for one lapse: %d %v", receipts, err)
	}
	var inboxTypes int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox WHERE type='connection.sign_in_needed'`).Scan(&inboxTypes); err != nil || inboxTypes != 3 {
		t.Fatalf("inbox notifications of the lapse type: %d %v", inboxTypes, err)
	}
	// A member who is not an owner, admin or the maker hears nothing.
	list, err := svc.List(ctx, security.Principal{OrgID: "org-c", UserID: "dev-d"})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("bystander inbox: %+v %v", list, err)
	}
}
