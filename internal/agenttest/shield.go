package agenttest

import (
	"context"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"
	shieldsqlite "github.com/xraph/shield/store/sqlite"

	"github.com/xraph/loomplane/internal/agentscope"
)

// Shield is a real shield engine over an in-memory store with the
// InfraAgent preset seeded for orgID under agentscope.App, the way the
// agent's extension seeds a workspace when the agent turns on. A scan
// also needs a context bound to orgID, as agentscope.Binder binds one.
func Shield(t testing.TB, orgID string) *shieldengine.Engine {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatalf("agenttest: open shield's store: %v", err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatalf("agenttest: open shield's store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := shieldsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatalf("agenttest: migrate shield's store: %v", err)
	}
	if _, err = presets.InfraAgent().Seed(ctx, st, orgID, agentscope.App); err != nil {
		t.Fatalf("agenttest: seed shield for %s: %v", orgID, err)
	}
	eng, err := shieldengine.New(shieldengine.WithStore(st))
	if err != nil {
		t.Fatalf("agenttest: start shield: %v", err)
	}
	return eng
}
