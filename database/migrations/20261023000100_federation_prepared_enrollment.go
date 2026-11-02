package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, atomicFederationDDL(
				`ALTER TABLE loomplane_federation_preparations ADD COLUMN IF NOT EXISTS enrollment_id TEXT NOT NULL DEFAULT ''`,
				`CREATE UNIQUE INDEX IF NOT EXISTS federation_preparation_enrollment
	ON loomplane_federation_preparations(org_id,enrollment_id)
	WHERE enrollment_id<>''`,
			))
			if err != nil {
				return fmt.Errorf("add federation preparation enrollment: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, atomicFederationDDL(
				`LOCK TABLE loomplane_federation_preparations IN ACCESS EXCLUSIVE MODE`,
				`IF EXISTS(SELECT 1 FROM loomplane_federation_preparations WHERE enrollment_id<>'') THEN
	RAISE EXCEPTION 'preparation enrollment receipts are still retained' USING ERRCODE='55000';
END IF`,
				`DROP INDEX IF EXISTS federation_preparation_enrollment`,
				`ALTER TABLE loomplane_federation_preparations DROP COLUMN IF EXISTS enrollment_id`,
			))
			if err != nil {
				return fmt.Errorf("drop federation preparation enrollment: %w", err)
			}
			return nil
		},
	)
}
