//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestManagedCleanupIntentsMigration(t *testing.T) {
	conn, executor := scratch(t)
	orchestrator := migrate.NewOrchestrator(executor, Group())
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var targets string
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_managed_cleanup_targets('missing','missing','missing','missing','delete')::text`).Scan(&targets); err != nil || targets != "[]" {
		t.Fatalf("empty targets: %s %v", targets, err)
	}
	if err := migration(t, "managed_cleanup_grants").Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := migration(t, "managed_cleanup_intents").Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
}
