//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/authsome/rbac"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platformadmin"
)

func TestGrantPlatformAdmin(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	// Setup makes its account platform owner, so Ada signs up after it.
	firstAccount(t, &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}, "owner@example.com", "Correct-horse-battery-9")
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, nil)

	ctx := t.Context()
	users, roles, closeDB, err := platformadmin.Open(ctx, cfg.Database.URL, cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()

	// The address is typed with different capitals than it was stored with.
	for i, want := range []bool{true, false} {
		_, granted, grantErr := platformadmin.Grant(ctx, users, roles, cfg.Identity.AppID, "Ada@Example.com")
		if grantErr != nil || granted != want {
			t.Fatalf("grant %d: granted=%v err=%v, want %v", i+1, granted, grantErr, want)
		}
	}
	if _, _, err = platformadmin.Grant(ctx, users, roles, cfg.Identity.AppID, "nobody@example.com"); err == nil {
		t.Fatal("granting an unknown email succeeded")
	}

	id, err := users.UserIDByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	held, err := roles.ListUserRolesForApp(ctx, cfg.Identity.AppID, id)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, role := range held {
		if role.Slug == rbac.PlatformOwnerSlug {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("platform-owner held %d times, want once: %+v", owners, held)
	}

	// The CLI records the grant the way grant-platform-admin does, from
	// outside the running app, after setup's own event in the platform
	// stream.
	trail, closeAudit, err := platformadmin.OpenAudit(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAudit()
	if err = platformadmin.RecordGrant(ctx, trail, cfg.Identity.AppID, platformadmin.CLIActor, id, map[string]string{"host": "ops"}); err != nil {
		t.Fatal(err)
	}
	db, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	var got string
	if err = db.QueryRow(ctx, `SELECT tenant_id||'|'||user_id||'|'||resource_id||'|'||coalesce(metadata->>'role','')||'|'||coalesce(metadata->>'host','')
		FROM chronicle_events WHERE action='platform_admin.granted'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := platformadmin.AuditTenant + "|" + platformadmin.CLIActor + "|" + id + "|" + rbac.PlatformOwnerSlug + "|ops"; got != want {
		t.Fatalf("grant recorded as %q, want %q", got, want)
	}
}
