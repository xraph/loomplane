//go:build integration

package catalogconnect

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/recipes"
)

// TestPostgresProberAgainstAServer runs the preload and role queries on a
// real server: as the test database's own user, and as a role that can
// neither create roles nor read shared_preload_libraries.
func TestPostgresProberAgainstAServer(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the Postgres prober test")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !q.Has("sslmode") {
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
	}
	ctx := WithConsumer(t.Context(), Consumer{Title: "Fabriq", Role: "fabriq_app"})
	req := recipes.Requirement{Engine: "postgres", MinVersion: "14", Preload: []string{"loomplane_missing_library"}, CreateRole: true}
	dial := (&net.Dialer{}).DialContext

	admin, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last first: the role goes before the connection.
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var super bool
	if err = admin.QueryRow(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&super); err != nil {
		t.Fatal(err)
	}
	if !super {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL's user isn't a superuser")
	}
	got, err := PostgresProber{}.Probe(ctx, dial, u.String(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version == "" || !slices.Equal(got.Problems, []string{"loomplane_missing_library isn't in shared_preload_libraries"}) {
		t.Fatalf("superuser: %+v", *got)
	}

	role := fmt.Sprintf("lp_probe_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD 'probe-password' NOSUPERUSER NOCREATEROLE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	})
	u.User = url.UserPassword(role, "probe-password")
	got, err = PostgresProber{}.Probe(ctx, dial, u.String(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"the connection's role can't read shared_preload_libraries, so Loomplane can't tell whether loomplane_missing_library is there; grant it pg_read_all_settings",
		"the connection's role can't create roles, and Fabriq creates fabriq_app",
	}
	if !slices.Equal(got.Problems, want) {
		t.Fatalf("unprivileged role: %+v", *got)
	}
	// pg_read_all_settings is enough to read the setting.
	if _, err = admin.Exec(ctx, "GRANT pg_read_all_settings TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	got, err = PostgresProber{}.Probe(ctx, dial, u.String(), req)
	if err != nil || !slices.Equal(got.Problems, []string{"loomplane_missing_library isn't in shared_preload_libraries", want[1]}) {
		t.Fatalf("with pg_read_all_settings: %+v %v", got, err)
	}
}
