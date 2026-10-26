//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

func TestPendingSignInIsTakenOnce(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := connections.NewPgStore(pg)
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := &connections.PendingSignIn{State: "state_1", OrgID: "org_1", UserID: "usr_1", Cloud: "digitalocean", Name: "Main", ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
	if err := store.PutSignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := store.TakeSignIn(ctx, "state_1")
	if err != nil || got.OrgID != "org_1" || got.UserID != "usr_1" || got.Cloud != "digitalocean" || got.Name != "Main" || got.ConnectionID != "" || !got.ExpiresAt.Equal(in.ExpiresAt) {
		t.Fatalf("take: %+v %v", got, err)
	}
	if _, err := store.TakeSignIn(ctx, "state_1"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("second take: %v", err)
	}
}

func TestSignInAppsAndMethodPersist(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := connections.NewPgStore(pg)
	now := time.Now().UTC()
	if err := store.PutApp(ctx, &connections.StoredApp{OrgID: "org_1", Cloud: "digitalocean", ClientID: "one", UpdatedBy: "usr_1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutApp(ctx, &connections.StoredApp{OrgID: "org_1", Cloud: "digitalocean", ClientID: "two", UpdatedBy: "usr_2", CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetApp(ctx, "org_1", "digitalocean")
	if err != nil || a.ClientID != "two" || a.UpdatedBy != "usr_2" || a.CreatedAt.After(now.Add(time.Minute)) {
		t.Fatalf("app: %+v %v", a, err)
	}
	if list, err := store.ListApps(ctx, "org_2"); err != nil || len(list) != 0 {
		t.Fatalf("other org: %v %v", list, err)
	}
	if err := store.DeleteApp(ctx, "org_1", "digitalocean"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetApp(ctx, "org_1", "digitalocean"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	c := &connections.Connection{ID: "conn_1", OrgID: "org_1", Cloud: "digitalocean", Name: "a", Status: "ready", Method: connections.MethodOAuth, CreatedAt: now, UpdatedAt: now}
	d := &connections.Connection{ID: "conn_2", OrgID: "org_1", Cloud: "digitalocean", Name: "b", Status: "ready", CreatedAt: now, UpdatedAt: now}
	for _, x := range []*connections.Connection{c, d} {
		if err := store.CreateConnection(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"conn_1": "oauth", "conn_2": "token"} {
		got, err := store.GetConnection(ctx, "org_1", id)
		if err != nil || got.Method != want {
			t.Fatalf("%s: %+v %v", id, got, err)
		}
	}
	c.Method = connections.MethodToken
	if err := store.UpdateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetConnection(ctx, "org_1", "conn_1"); got.Method != "token" {
		t.Fatalf("update method: %+v", got)
	}
}
