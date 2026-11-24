//go:build integration

package catalogconnect

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPgStoreKeepsTheUsersLoomplaneMade(t *testing.T) {
	store, _ := scratchStore(t)
	ctx := t.Context()
	e := &Endpoint{ID: "endpoint_1", OrgID: "org", Name: "orders", Engine: "postgres", Host: "db", Port: 5432, TLS: "require", CreatedBy: "u", CreatedAt: time.Now()}
	if err := store.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	made := time.Now().UTC().Truncate(time.Second)
	for _, env := range []string{"production", "staging", "production"} {
		if err := store.SaveOwnUser(ctx, "org", &OwnUser{EndpointID: e.ID, User: "lp_api", Database: "api", CreatedDatabase: env == "production", ProjectID: "p", Service: "api", Environments: []string{env}, CreatedAt: made}); err != nil {
			t.Fatal(err)
		}
	}
	users, err := store.OwnUsers(ctx, "org", e.ID)
	if err != nil || len(users) != 1 || !reflect.DeepEqual(users[0].Environments, []string{"production", "staging"}) || !users[0].CreatedDatabase {
		t.Fatalf("saved: %+v %v", users, err)
	}
	if left, rerr := store.ReleaseOwnUser(ctx, "org", e.ID, "lp_api", "staging"); rerr != nil || left != 1 {
		t.Fatalf("staging let go: %d %v", left, rerr)
	}
	// Still used by production, it isn't revoked.
	if err = store.RevokeOwnUser(ctx, "org", e.ID, "lp_api", made.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if users, _ = store.OwnUsers(ctx, "org", e.ID); users[0].RevokedAt != nil {
		t.Fatalf("revoked in use: %+v", users)
	}
	if left, rerr := store.ReleaseOwnUser(ctx, "org", e.ID, "lp_api", "production"); rerr != nil || left != 0 {
		t.Fatalf("production let go: %d %v", left, rerr)
	}
	if err = store.RevokeOwnUser(ctx, "org", e.ID, "lp_api", made.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if users, _ = store.OwnUsers(ctx, "org", e.ID); users[0].RevokedAt == nil {
		t.Fatalf("not revoked: %+v", users)
	}
	if left, rerr := store.ReleaseOwnUser(ctx, "org", e.ID, "nobody", "production"); rerr != nil || left != 0 {
		t.Fatalf("an unknown user: %d %v", left, rerr)
	}
	// Saved again, it's in use again, and keeps when it was made.
	if err = store.SaveOwnUser(ctx, "org", &OwnUser{EndpointID: e.ID, User: "lp_api", Database: "api", ProjectID: "p", Service: "api", Environments: []string{"staging"}, CreatedAt: made.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if users, _ = store.OwnUsers(ctx, "org", e.ID); users[0].RevokedAt != nil || !users[0].CreatedDatabase || !users[0].CreatedAt.Equal(made) {
		t.Fatalf("saved again: %+v", users)
	}
	if err = store.DeleteOwnUser(ctx, "org", e.ID, "lp_api"); err != nil {
		t.Fatal(err)
	}
	if users, _ = store.OwnUsers(ctx, "org", e.ID); len(users) != 0 {
		t.Fatalf("deleted: %+v", users)
	}
}

// TestPostgresAdminMakesAndRemovesAUser runs the statements against a real
// server: a user made with a database of its own logs in to it, loses its
// login when it's locked, and is gone with its database when dropped.
func TestPostgresAdminMakesAndRemovesAUser(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the Postgres admin test")
	}
	ctx := context.Background()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
	}
	var d net.Dialer
	admin, err := openPostgresAdmin(ctx, u.String(), d.DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	suffix := fmt.Sprint(time.Now().UnixNano())
	user, database := "lp_it_"+suffix[len(suffix)-8:], "own_it_"+suffix[len(suffix)-8:]
	t.Cleanup(func() {
		_ = admin.DropDatabase(ctx, database)
		_ = admin.DropUser(ctx, user, database)
	})
	if err = admin.EnsureUser(ctx, user, "first-password"); err != nil {
		t.Fatal(err)
	}
	if err = admin.EnsureUser(ctx, user, "second-password"); err != nil {
		t.Fatal(err)
	}
	if err = admin.CreateDatabase(ctx, database, user); err != nil {
		t.Fatal(err)
	}
	if err = admin.GrantDatabase(ctx, database, user); err != nil {
		t.Fatal(err)
	}
	as := *u
	as.User, as.Path = url.UserPassword(user, "second-password"), "/"+database
	conn, err := pgx.Connect(ctx, as.String())
	if err != nil {
		t.Fatalf("the user can't log in: %v", err)
	}
	if _, err = conn.Exec(ctx, "CREATE TABLE things (id int)"); err != nil {
		t.Fatalf("the user can't use its database: %v", err)
	}
	_ = conn.Close(ctx)
	if err = admin.LockUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if conn, err = pgx.Connect(ctx, as.String()); err == nil {
		_ = conn.Close(ctx)
		t.Fatal("a locked user logged in")
	}
	if err = admin.DropDatabase(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err = admin.DropUser(ctx, user, database); err != nil {
		t.Fatal(err)
	}
	if ok, _ := admin.DatabaseExists(ctx, database); ok {
		t.Fatal("the database is still there")
	}
}
