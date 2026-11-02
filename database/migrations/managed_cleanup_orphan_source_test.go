//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestManagedCleanupOrphanSourceMigration(t *testing.T) {
	for _, version := range []string{"20261206000500", "20261207000100"} {
		t.Run(version, func(t *testing.T) {
			conn, executor := scratch(t)
			group := migrate.NewGroup(GroupName)
			var step *migrate.Migration
			for _, candidate := range Group().Migrations() {
				if candidate.Version > "20261207000100" {
					continue
				}
				if candidate.Name == "managed_cleanup_orphan_source" && candidate.Version != version {
					continue
				}
				copied := *candidate
				group.MustRegister(&copied)
				if copied.Version == version {
					step = &copied
				}
			}
			if step == nil {
				t.Fatal("missing migration version", version)
			}
			if _, err := migrate.NewOrchestrator(executor, group).Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := step.Down(t.Context(), executor); err != nil {
				t.Fatal(err)
			}
			if err := step.Up(t.Context(), executor); err != nil {
				t.Fatal(err)
			}
			var restored bool
			if err := conn.QueryRow(t.Context(), `SELECT to_regprocedure('loomplane_guard_orphan_cleanup_source()') IS NOT NULL`).Scan(&restored); err != nil || !restored {
				t.Fatalf("restored cleanup guard: %v, %v", restored, err)
			}
		})
	}
}
