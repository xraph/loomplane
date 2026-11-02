//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestManagedCleanupObservationMigration(t *testing.T) {
	conn, executor := scratch(t)
	orch := migrate.NewOrchestrator(executor, Group())
	if _, err := orch.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var block string
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_managed_cleanup_admission_block('org','cloud','unrelated')`).Scan(&block); err != nil || block != "" {
		t.Fatalf("unrelated capacity blocked: %q %v", block, err)
	}
	step := migration(t, "managed_cleanup_observations")
	if err := step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := step.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
}
