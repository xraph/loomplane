//go:build integration

package integration

import (
	"errors"
	"fmt"
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
	if err := store.PutApp(ctx, &connections.StoredApp{Cloud: "digitalocean", ClientID: "one", UpdatedBy: "usr_1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutApp(ctx, &connections.StoredApp{Cloud: "digitalocean", ClientID: "two", UpdatedBy: "usr_2", CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutApp(ctx, &connections.StoredApp{Cloud: "vercel", ClientID: "oac", Slug: "loomplane", UpdatedBy: "usr_1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetApp(ctx, "digitalocean")
	if err != nil || a.ClientID != "two" || a.UpdatedBy != "usr_2" || a.CreatedAt.After(now.Add(time.Minute)) {
		t.Fatalf("app: %+v %v", a, err)
	}
	list, err := store.ListApps(ctx)
	if err != nil || len(list) != 2 || list[0].Cloud != "digitalocean" || list[1].Cloud != "vercel" || list[1].Slug != "loomplane" {
		t.Fatalf("list: %v %v", list, err)
	}
	if err = store.DeleteApp(ctx, "digitalocean"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetApp(ctx, "digitalocean"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if list, err = store.ListApps(ctx); err != nil || len(list) != 1 {
		t.Fatalf("after delete: %v %v", list, err)
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
	if got, err := store.AppProblems(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, cloud := range []string{"vercel", "gcp", "azure"} {
		if err := store.PutApp(ctx, &connections.StoredApp{Cloud: cloud, ClientID: cloud + "-id", UpdatedBy: "usr_1", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range [][2]string{{"vercel", "first"}, {"gcp", "gcp broke"}} {
		if err := store.SetAppProblem(ctx, p[0], p[1], now); err != nil {
			t.Fatal(err)
		}
	}
	// Setting again replaces the message.
	if err := store.SetAppProblem(ctx, "vercel", "second", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A cloud with no app has no row to put a problem on.
	if err := store.SetAppProblem(ctx, "digitalocean", "nowhere", now); err != nil {
		t.Fatalf("problem for a cloud with no app: %v", err)
	}
	got, err := store.AppProblems(ctx)
	if err != nil || len(got) != 2 || got["vercel"] != "second" || got["gcp"] != "gcp broke" {
		t.Fatalf("problems: %v %v", got, err)
	}
	if err = store.ClearAppProblem(ctx, "vercel"); err != nil {
		t.Fatal(err)
	}
	if err = store.ClearAppProblem(ctx, "azure"); err != nil {
		t.Fatalf("clearing nothing: %v", err)
	}
	if err = store.ClearAppProblem(ctx, "digitalocean"); err != nil {
		t.Fatalf("clearing for a cloud with no app: %v", err)
	}
	got, err = store.AppProblems(ctx)
	if err != nil || len(got) != 1 || got["gcp"] != "gcp broke" {
		t.Fatalf("after clear: %v %v", got, err)
	}
	// Saving the app again leaves its problem for the service to clear,
	// and the app itself reads back unchanged.
	if err = store.PutApp(ctx, &connections.StoredApp{Cloud: "gcp", ClientID: "new-id", UpdatedBy: "usr_2", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if a, getErr := store.GetApp(ctx, "gcp"); getErr != nil || a.ClientID != "new-id" {
		t.Fatalf("app: %+v %v", a, getErr)
	}
	if got, _ = store.AppProblems(ctx); got["gcp"] != "gcp broke" {
		t.Fatalf("save dropped the problem: %v", got)
	}
}

// A connection keeps the client ID it signed in with.
func TestConnectionKeepsItsOAuthClientID(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	c := &connections.Connection{ID: "conn_1", OrgID: "org_1", Cloud: "gcp", Name: "a", Status: "ready", Method: connections.MethodOAuth, OAuthClientID: "client-1", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetConnection(ctx, "org_1", "conn_1")
	if err != nil || got.OAuthClientID != "client-1" {
		t.Fatalf("created: %+v %v", got, err)
	}
	got.OAuthClientID = "client-2"
	if err = store.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, err = store.GetConnection(ctx, "org_1", "conn_1"); err != nil || got.OAuthClientID != "client-2" {
		t.Fatalf("updated: %+v %v", got, err)
	}
}

// Counting an app's sign-ins skips other apps, other clouds, tokens and
// sign-ins waiting to be redone, and counts each organization once.
func TestSignedInWithCountsWorkingSignIns(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	for i, c := range []connections.Connection{
		{OrgID: "org_1", Cloud: "gcp", Method: connections.MethodOAuth, OAuthClientID: "app", Status: "ready"},
		{OrgID: "org_1", Cloud: "gcp", Method: connections.MethodOAuth, OAuthClientID: "app", Status: "incomplete"},
		{OrgID: "org_2", Cloud: "gcp", Method: connections.MethodOAuth, OAuthClientID: "app", Status: "ready"},
		{OrgID: "org_3", Cloud: "gcp", Method: connections.MethodOAuth, OAuthClientID: "app", Status: connections.StatusSignInNeeded},
		{OrgID: "org_4", Cloud: "gcp", Method: connections.MethodOAuth, OAuthClientID: "other", Status: "ready"},
		{OrgID: "org_5", Cloud: "gcp", Method: connections.MethodToken, Status: "ready"},
		{OrgID: "org_6", Cloud: "azure", Method: connections.MethodOAuth, OAuthClientID: "app", Status: "ready"},
	} {
		c.ID, c.Name, c.CreatedAt, c.UpdatedAt = fmt.Sprintf("conn_%d", i), fmt.Sprintf("n%d", i), now, now
		if err := store.CreateConnection(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	n, orgs, err := store.SignedInWith(ctx, "gcp", "app")
	if err != nil || n != 3 || orgs != 2 {
		t.Fatalf("got %d connections in %d orgs: %v", n, orgs, err)
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
