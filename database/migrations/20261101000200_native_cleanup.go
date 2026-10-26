package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_native_restores").
				AddColumn("cleanup_by", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("cleanup_requested_at", migrate.Timestamp).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $native_cleanup_up$
BEGIN
	ALTER TABLE loomplane_native_restores DROP CONSTRAINT IF EXISTS loomplane_native_restores_status_check;
	ALTER TABLE loomplane_native_restores ADD CONSTRAINT loomplane_native_restores_status_check
		CHECK(status IN ('queued','running','succeeded','failed','cleanup_queued','deleting','cleanup_failed','deleted'));

	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_native_restores'::regclass AND conname='loomplane_native_restores_cleanup_intent_check') THEN
		ALTER TABLE loomplane_native_restores ADD CONSTRAINT loomplane_native_restores_cleanup_intent_check
			CHECK(((cleanup_by='' AND cleanup_requested_at IS NULL) OR (cleanup_by<>'' AND cleanup_requested_at IS NOT NULL)) AND (status<>'cleanup_queued' OR cleanup_by<>''));
	END IF;
END $native_cleanup_up$`)
			if err != nil {
				return fmt.Errorf("add native restore cleanup checks: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_cleanup_down$
BEGIN
	IF to_regclass('loomplane_native_restores') IS NOT NULL THEN
		LOCK TABLE loomplane_native_restores IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_restores WHERE status='cleanup_queued') THEN
			RAISE EXCEPTION 'native cleanup history is still retained' USING ERRCODE='55000';
		END IF;
		IF EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='loomplane_native_restores'::regclass AND attname='cleanup_by' AND NOT attisdropped) THEN
			IF EXISTS(SELECT 1 FROM loomplane_native_restores WHERE cleanup_by<>'' OR cleanup_requested_at IS NOT NULL) THEN
				RAISE EXCEPTION 'native cleanup history is still retained' USING ERRCODE='55000';
			END IF;
		END IF;

		ALTER TABLE loomplane_native_restores DROP CONSTRAINT IF EXISTS loomplane_native_restores_cleanup_intent_check;
		ALTER TABLE loomplane_native_restores DROP COLUMN IF EXISTS cleanup_by, DROP COLUMN IF EXISTS cleanup_requested_at;
		ALTER TABLE loomplane_native_restores DROP CONSTRAINT IF EXISTS loomplane_native_restores_status_check;
		ALTER TABLE loomplane_native_restores ADD CONSTRAINT loomplane_native_restores_status_check
			CHECK(status IN ('queued','running','succeeded','failed','deleting','cleanup_failed','deleted'));
	END IF;
END $native_cleanup_down$`)
			if err != nil {
				return fmt.Errorf("drop native restore cleanup: %w", err)
			}
			return nil
		},
	)
}
