package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Retain the renamed version for databases that already recorded it. The
// original version owns the DDL whenever both migration records are present.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		applied, err := managedCleanupOrphanSourceApplied(ctx, db, "20261206000500")
		if err != nil || applied {
			return err
		}
		return managedCleanupOrphanSourceUp(ctx, db)
	}, func(ctx context.Context, db *migrate.DB) error {
		applied, err := managedCleanupOrphanSourceApplied(ctx, db, "20261206000500")
		if err != nil || applied {
			return err
		}
		return managedCleanupOrphanSourceDown(ctx, db)
	})
}
