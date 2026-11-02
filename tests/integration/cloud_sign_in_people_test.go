//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/xraph/grove"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/notifications"
)

func TestLeftSignInsLapseOnlyPersonalClouds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	store, pg := signInStore(t)
	for _, q := range []string{
		// Only membership matters here, so drop the foreign keys that would
		// need a whole app, environment and organization behind each row.
		`DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT conrelid::regclass AS t, conname FROM pg_constraint
				WHERE contype = 'f' AND conrelid IN ('authsome_users'::regclass, 'authsome_members'::regclass)
			LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.t, r.conname); END LOOP;
		END $$`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,banned,deleted_at) VALUES
			('owner','app','env','o@example.com',false,NULL),('admin','app','env','a@example.com',false,NULL),
			('ada','app','env','ada@example.com',false,NULL)`,
		`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES ('m1','org_1','owner','owner'),('m2','org_1','admin','admin'),('m3','org_1','ada','member')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	add := func(id, cloud string) {
		t.Helper()
		// Checked just now, so the daily probe leaves them alone and the
		// test sees only what leaving does.
		c := &connections.Connection{ID: id, OrgID: "org_1", Cloud: cloud, Name: id, Status: connections.StatusReady, Method: connections.MethodOAuth,
			SignedInBy: "ada", CreatedBy: "ada", Account: "acct", CheckedAt: &now, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	add("conn_gcp", connections.GCP)
	add("conn_do", connections.DigitalOcean)

	secrets := &infraSecrets{values: map[string][]byte{}}
	raw, _ := json.Marshal(oauth.Grant{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: now.Add(30 * 24 * time.Hour)})
	for _, id := range []string{"conn_gcp", "conn_do"} {
		if err := secrets.Put(ctx, "org_1", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	db, err := grove.Open(pg)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := notifications.New(config.Config{SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}, db)
	if err != nil {
		t.Fatal(err)
	}
	svc := connections.NewService(store, secrets, nil, nil, nil, func(msg string, err error) { t.Logf("warn: %s: %v", msg, err) })
	// As the extension wires it: the lapse only queues outbox rows.
	svc.Lapsed = func(ctx context.Context, c *connections.Connection) {
		if lapseErr := mail.ConnectionLapsed(ctx, c.OrgID, c.ID, c.Name, c.Cloud, c.CreatedBy, time.Now()); lapseErr != nil {
			t.Errorf("queue the lapse notice: %v", lapseErr)
		}
	}

	// While ada is a member, nothing happens.
	report, err := svc.RefreshDue(ctx)
	if err != nil || report != (connections.RefreshReport{}) {
		t.Fatalf("with ada here: %+v %v", report, err)
	}
	if _, err = pg.Exec(ctx, `DELETE FROM authsome_members WHERE user_id = 'ada'`); err != nil {
		t.Fatal(err)
	}
	report, err = svc.RefreshDue(ctx)
	if err != nil || report.Left != 1 || report.Failed != 0 || report.Probed != 0 {
		t.Fatalf("after ada left: %+v %v", report, err)
	}
	gcp, err := store.GetConnection(ctx, "org_1", "conn_gcp")
	if err != nil || gcp.Status != connections.StatusSignInNeeded || gcp.Generation != 1 ||
		gcp.Message != "The person who signed in to conn_gcp has left the organization. Sign in again." {
		t.Fatalf("google: %+v %v", gcp, err)
	}
	do, err := store.GetConnection(ctx, "org_1", "conn_do")
	if err != nil || do.Status != connections.StatusReady || do.Message != "" {
		t.Fatalf("digitalocean: %+v %v", do, err)
	}
	outbox := func() []string {
		t.Helper()
		rows, qerr := pg.Query(ctx, `SELECT user_id FROM loomplane_notification_outbox WHERE org_id='org_1' AND payload->>'connection_id' = 'conn_gcp' ORDER BY user_id`)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var users []string
		for rows.Next() {
			var u string
			if scanErr := rows.Scan(&u); scanErr != nil {
				t.Fatal(scanErr)
			}
			users = append(users, u)
		}
		return users
	}
	if got, want := outbox(), []string{"ada", "admin", "owner"}; !slices.Equal(got, want) {
		t.Fatalf("notice recipients %v, want %v", got, want)
	}
	var others int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE payload->>'connection_id' <> 'conn_gcp'`).Scan(&others); err != nil || others != 0 {
		t.Fatalf("notices for other connections: %d %v", others, err)
	}

	// A second pass adds nothing.
	if report, err = svc.RefreshDue(ctx); err != nil || report != (connections.RefreshReport{}) {
		t.Fatalf("again: %+v %v", report, err)
	}
	if got := outbox(); len(got) != 3 {
		t.Fatalf("notices after a second pass: %v", got)
	}
}
