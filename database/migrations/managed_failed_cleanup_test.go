//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestManagedFailedCleanupMigration(t *testing.T) {
	conn, executor := scratch(t)
	orchestrator := migrate.NewOrchestrator(executor, Group())
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var targets string
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_managed_failed_cleanup_targets('missing','missing')::text`).Scan(&targets); err != nil || targets != "[]" {
		t.Fatalf("empty failed cleanup targets: %s %v", targets, err)
	}
	if err := migration(t, "managed_failed_cleanup").Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := migration(t, "managed_failed_cleanup").Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
}
