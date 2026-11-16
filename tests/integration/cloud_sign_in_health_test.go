//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

// The instance console's health of signed-in connections reads across every
// organization, compares each grant's client with the cloud's current app,
// and names organizations and people from authsome's tables.
func TestSignInHealthAcrossOrganizations(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	for _, sql := range []string{
		// Users and organizations here are only what the console reads, so the
		// rows don't need a whole app and environment behind them.
		`DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT conrelid::regclass AS t, conname FROM pg_constraint
				WHERE contype = 'f' AND conrelid IN ('authsome_users'::regclass, 'authsome_organizations'::regclass)
			LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.t, r.conname); END LOOP;
		END $$`,
		`INSERT INTO authsome_users(id,app_id,env_id,email) VALUES ('usr_1','app','env','ana@acme.example')`,
		`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by)
			VALUES ('org_a','app','env','Acme','acme','usr_1'), ('org_b','app','env','Bolt','bolt','usr_1')`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, org, cloud, method, status, client, by string) {
		t.Helper()
		_, ierr := conn.Exec(ctx, `INSERT INTO loomplane_cloud_connections
			(id, org_id, cloud, name, account, status, created_by, created_at, updated_at, method, oauth_client_id, signed_in_by, message)
			VALUES ($1, $2, $3, $1, 'team', $4, 'u', now(), now(), $5, $6, $7, '')`, id, org, cloud, status, method, client, by)
		if ierr != nil {
			t.Fatal(ierr)
		}
	}
	add("do_ok", "org_a", "digitalocean", "oauth", "ready", "do-now", "usr_1")
	add("do_old", "org_a", "digitalocean", "oauth", "ready", "do-before", "usr_1")
	add("do_lapsed", "org_b", "digitalocean", "oauth", "sign_in_needed", "do-now", "")
	add("gcp_failed", "org_b", "gcp", "oauth", "failed", "gcp-now", "")
	add("gcp_partial", "org_b", "gcp", "oauth", "incomplete", "gcp-now", "")
	// Vercel's grants outlive their app, so it's left out of current.
	add("vercel_any", "org_a", "vercel", "oauth", "ready", "whatever", "")
	// A pasted token isn't a sign-in.
	add("do_token", "org_a", "digitalocean", "token", "failed", "", "")

	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	store := connections.NewPgStore(pg)
	current := map[string]string{"digitalocean": "do-now", "gcp": "gcp-now"}

	counts, err := store.SignInHealthCounts(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	if do := counts["digitalocean"]; do.Total != 3 || do.Healthy != 1 || do.AppChanged != 1 || do.SignInNeeded != 1 {
		t.Fatalf("digitalocean %+v", do)
	}
	if g := counts["gcp"]; g.Failed != 1 || g.Incomplete != 1 {
		t.Fatalf("gcp %+v", g)
	}
	if v := counts["vercel"]; v.Total != 1 || v.Healthy != 1 {
		t.Fatalf("vercel %+v", v)
	}

	// Worst first, with names, and counts that ignore the health filter.
	page, err := store.SignedInConnections(ctx, current, connections.SignedInFilter{Health: "problems", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(page.Items))
	for _, c := range page.Items {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"do_lapsed", "do_old", "gcp_failed"}) || page.Total != 3 || page.Counts.Total != 6 {
		t.Fatalf("problems %v, total %d, counts %+v", ids, page.Total, page.Counts)
	}
	if old := page.Items[1]; old.Health != connections.SignInAppChanged || old.OrgName != "Acme" || old.SignedInByEmail != "ana@acme.example" {
		t.Fatalf("app changed %+v", old)
	}

	// Search reaches organization names and emails; a cloud filter narrows.
	page, err = store.SignedInConnections(ctx, current, connections.SignedInFilter{Search: "bolt", Cloud: "gcp", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "gcp_failed" {
		t.Fatalf("search %+v", page)
	}
	page, err = store.SignedInConnections(ctx, current, connections.SignedInFilter{Search: "ana@", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 {
		t.Fatalf("paged search %+v", page)
	}
	// LIKE wildcards in a search are literal.
	page, err = store.SignedInConnections(ctx, current, connections.SignedInFilter{Search: "%", Limit: 10})
	if err != nil || page.Total != 0 {
		t.Fatalf("wildcard search %+v %v", page, err)
	}

	emails, err := store.UserEmails(ctx, []string{"usr_1", "usr_gone"})
	if err != nil || len(emails) != 1 || emails["usr_1"] != "ana@acme.example" {
		t.Fatalf("emails %v %v", emails, err)
	}
}
