//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestRuntimeAccessMigration(t *testing.T) {
	_, executor := scratch(t)
	orchestrator := migrate.NewOrchestrator(executor, Group())
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := migration(t, "runtime_access").Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := migration(t, "runtime_access").Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
}
