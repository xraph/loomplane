//go:build integration

package agentsettings_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
)

// The migrations the AI settings row comes from, by version and name.
var settingsMigrations = map[string]string{
	"20261125000100": "ai_settings",
	"20261126000100": "ai_huggingface_token",
	"20261209000100": "ai_settings_agent",
}

// scratchStores are both stores over a database of their own, dropped when
// the test ends.
func scratchStores(t *testing.T) (*agentsettings.PgStore, *aisettings.PgStore) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the agent settings store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("agentsettings_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, m := range migrations.Group().Migrations() {
		if want, ok := settingsMigrations[m.Version]; ok {
			if m.Name != want {
				t.Fatalf("migration %s is %q, want %q", m.Version, m.Name, want)
			}
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
			applied++
		}
	}
	if applied != len(settingsMigrations) {
		t.Fatalf("applied %d of %d migrations", applied, len(settingsMigrations))
	}
	return agentsettings.NewPgStore(pg), aisettings.NewPgStore(pg)
}

func TestPgStoreRoundTrip(t *testing.T) {
	st, ai := scratchStores(t)
	ctx := t.Context()

	// A workspace never written: the agent off, at revision 0, and reading
	// stores nothing.
	got, err := st.Get(ctx, "org_a")
	if err != nil || got.OrgID != "org_a" || got.Enabled || got.ProviderKey != "" || got.Revision != 0 {
		t.Fatalf("defaults %+v %v", got, err)
	}
	if _, err = st.ByNexusTenant(ctx, "tenant_a"); !errors.Is(err, agentsettings.ErrNotFound) {
		t.Fatalf("unknown tenant: %v", err)
	}

	// The first write inserts the row.
	s := &agentsettings.Settings{OrgID: "org_a", ProviderKey: "claude", Model: "claude-sonnet-5-5", Knowledge: true, NexusTenantID: "tenant_a", UpdatedBy: "usr_1"}
	if err = st.Save(ctx, s, 0); err != nil {
		t.Fatal(err)
	}
	if s.Revision != 1 || s.UpdatedAt.IsZero() || s.Enabled {
		t.Fatalf("saved %+v", s)
	}
	again := *s
	again.Revision = 0
	if err = st.Save(ctx, &again, 0); !errors.Is(err, agentsettings.ErrStale) {
		t.Fatalf("second insert: %v", err)
	}

	// The budget and embeddings are the workspace's, untouched; its budget
	// write bumps the shared revision, so the agent's next write must have
	// read it.
	if _, err = ai.SetBudget(ctx, "org_a", "100.00", "usr_2"); err != nil {
		t.Fatal(err)
	}
	s.Enabled = true
	if err = st.Save(ctx, s, 1); !errors.Is(err, agentsettings.ErrStale) {
		t.Fatalf("stale save: %v", err)
	}
	if err = st.Save(ctx, s, 2); err != nil {
		t.Fatal(err)
	}
	ws, err := ai.Settings(ctx, "org_a")
	if err != nil || ws.MonthlyBudgetUSD != "100.00" || ws.Revision != 3 || ws.Embeddings.Mode != aisettings.EmbeddingsLocal {
		t.Fatalf("workspace settings %+v %v", ws, err)
	}

	for _, read := range []func() (*agentsettings.Settings, error){
		func() (*agentsettings.Settings, error) { return st.Get(ctx, "org_a") },
		func() (*agentsettings.Settings, error) { return st.ByNexusTenant(ctx, "tenant_a") },
	} {
		got, err = read()
		if err != nil {
			t.Fatal(err)
		}
		want := agentsettings.Settings{
			OrgID: "org_a", Enabled: true, ProviderKey: "claude", Model: "claude-sonnet-5-5", Knowledge: true,
			NexusTenantID: "tenant_a", Revision: 3, UpdatedBy: "usr_1", UpdatedAt: s.UpdatedAt,
		}
		if *got != want {
			t.Fatalf("read back\n%+v\nwant\n%+v", *got, want)
		}
	}

	// An empty tenant is no tenant, and two workspaces can't share one.
	if _, err = st.ByNexusTenant(ctx, ""); !errors.Is(err, agentsettings.ErrNotFound) {
		t.Fatalf("empty tenant: %v", err)
	}
	other := &agentsettings.Settings{OrgID: "org_b", NexusTenantID: "tenant_a"}
	if err = st.Save(ctx, other, 0); err == nil || errors.Is(err, agentsettings.ErrStale) {
		t.Fatalf("a shared tenant: %v", err)
	}
	other.NexusTenantID = ""
	if err = st.Save(ctx, other, 0); err != nil {
		t.Fatal(err)
	}
}
