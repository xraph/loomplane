//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestManagedCleanupOrphanSourceMigration(t *testing.T) {
	_, executor := scratch(t)
	orch := migrate.NewOrchestrator(executor, Group())
	if _, err := orch.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	step := migration(t, "managed_cleanup_orphan_source")
	if err := step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := step.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
}
