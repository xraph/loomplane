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

func signInStore(t *testing.T) (*connections.PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return connections.NewPgStore(pg), pg
}

func TestPendingSignInKeepsSettingsAndVerifier(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := &connections.PendingSignIn{State: "state_1", OrgID: "org_1", UserID: "usr_1", Cloud: "gcp", Name: "Main",
		Settings: map[string]string{"project": "my-proj", "region": "us-east1"}, Verifier: "verifier-1",
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
	if err := store.PutSignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := store.TakeSignIn(ctx, "state_1")
	if err != nil || got.Verifier != "verifier-1" || len(got.Settings) != 2 || got.Settings["project"] != "my-proj" || got.Settings["region"] != "us-east1" {
		t.Fatalf("take: %+v %v", got, err)
	}

	// No settings reads back as an empty map, not nil.
	if err = store.PutSignIn(ctx, &connections.PendingSignIn{State: "state_2", OrgID: "org_1", UserID: "usr_1", Cloud: "vercel", Name: "Main", ExpiresAt: now.Add(time.Minute), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, err = store.TakeSignIn(ctx, "state_2")
	if err != nil || got.Settings == nil || len(got.Settings) != 0 || got.Verifier != "" {
		t.Fatalf("take without settings: %+v %v", got, err)
	}
}

func TestAppProblemsRoundTrip(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if got, err := store.AppProblems(ctx, "org_1"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, p := range [][3]string{{"org_1", "vercel", "first"}, {"org_1", "gcp", "gcp broke"}, {"org_2", "vercel", "other org"}} {
		if err := store.SetAppProblem(ctx, p[0], p[1], p[2], now); err != nil {
			t.Fatal(err)
		}
	}
	// Setting again replaces the message.
	if err := store.SetAppProblem(ctx, "org_1", "vercel", "second", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := store.AppProblems(ctx, "org_1")
	if err != nil || len(got) != 2 || got["vercel"] != "second" || got["gcp"] != "gcp broke" {
		t.Fatalf("problems: %v %v", got, err)
	}
	if err = store.ClearAppProblem(ctx, "org_1", "vercel"); err != nil {
		t.Fatal(err)
	}
	if err = store.ClearAppProblem(ctx, "org_1", "azure"); err != nil {
		t.Fatalf("clearing nothing: %v", err)
	}
	got, err = store.AppProblems(ctx, "org_1")
	if err != nil || len(got) != 1 || got["gcp"] != "gcp broke" {
		t.Fatalf("after clear: %v %v", got, err)
	}
	if other, _ := store.AppProblems(ctx, "org_2"); other["vercel"] != "other org" {
		t.Fatalf("other org lost its problem: %v", other)
	}
}

func TestSweepSignInsDropsOnlyExpired(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for state, at := range map[string]time.Time{"old_1": now.Add(-time.Hour), "old_2": now.Add(-time.Minute), "live": now.Add(time.Hour)} {
		if err := store.PutSignIn(ctx, &connections.PendingSignIn{State: state, OrgID: "org_1", UserID: "usr_1", Cloud: "gcp", Name: "x", ExpiresAt: at, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.SweepSignIns(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	for _, state := range []string{"old_1", "old_2"} {
		if _, err = store.TakeSignIn(ctx, state); !errors.Is(err, connections.ErrNotFound) {
			t.Fatalf("%s should be gone: %v", state, err)
		}
	}
	if _, err = store.TakeSignIn(ctx, "live"); err != nil {
		t.Fatalf("live sign-in was swept: %v", err)
	}
}

func TestLeftSignInsFindsDepartedPeople(t *testing.T) {
	store, pg := signInStore(t)
	ctx := t.Context()
	for _, q := range []string{
		// Only membership matters here, so drop the foreign keys that would
		// need a whole app, environment and organization behind each row.
		`DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT conrelid::regclass AS t, conname FROM pg_constraint
				WHERE contype = 'f' AND conrelid IN ('authsome_users'::regclass, 'authsome_members'::regclass)
			LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.t, r.conname); END LOOP;
		END $$`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,banned,deleted_at) VALUES
			('here','app','env','a@example.com',false,NULL),('gone','app','env','b@example.com',false,NULL),
			('banned','app','env','c@example.com',true,NULL),('deleted','app','env','d@example.com',false,now())`,
		// 'gone' has no membership row; the others are all members.
		`INSERT INTO authsome_members(id,org_id,user_id) VALUES ('m1','org_1','here'),('m2','org_1','banned'),('m3','org_1','deleted')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	add := func(id, cloud, by, status string) {
		t.Helper()
		c := &connections.Connection{ID: id, OrgID: "org_1", Cloud: cloud, Name: id, Status: status, Method: connections.MethodOAuth, SignedInBy: by, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	add("left", "gcp", "gone", "connected")
	add("banned", "azure", "banned", "connected")
	add("deleted", "gcp", "deleted", "connected")
	add("present", "gcp", "here", "connected")
	add("nobody", "gcp", "", "connected")
	add("waiting", "gcp", "gone", connections.StatusSignInNeeded)
	add("do", "digitalocean", "gone", "connected")

	got, err := store.LeftSignIns(ctx, []string{"gcp", "azure"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, c := range got {
		ids[c.ID] = c.SignedInBy
	}
	if len(ids) != 3 || ids["left"] != "gone" || ids["banned"] != "banned" || ids["deleted"] != "deleted" {
		t.Fatalf("left sign-ins: %v", ids)
	}
}
