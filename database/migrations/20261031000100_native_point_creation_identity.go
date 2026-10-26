package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Native timestamps retain nanoseconds in body. TIMESTAMPTZ retains only
// microseconds, so it cannot key a selected provider recovery point.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_point_identity_up$
BEGIN
	ALTER TABLE loomplane_native_points ADD COLUMN IF NOT EXISTS creation_identity TEXT COLLATE "C"
		GENERATED ALWAYS AS (
			left(body->>'created_at',19)||'.'||rpad(split_part(split_part(body->>'created_at','.',2),'Z',1),9,'0')||'Z'
		) STORED NOT NULL;

	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_native_points'::regclass AND conname='loomplane_native_points_creation_time_utc') THEN
		ALTER TABLE loomplane_native_points ADD CONSTRAINT loomplane_native_points_creation_time_utc
			CHECK ((body->>'created_at') ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$');
	END IF;

	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_native_points'::regclass AND conname='loomplane_native_points_creation_identity_key') THEN
		ALTER TABLE loomplane_native_points ADD CONSTRAINT loomplane_native_points_creation_identity_key UNIQUE(source_id,provider_id,creation_identity);
	END IF;

	CREATE INDEX IF NOT EXISTS loomplane_native_points_identity_order ON loomplane_native_points(org_id,source_id,creation_identity DESC,id DESC);
	ALTER TABLE loomplane_native_points DROP CONSTRAINT IF EXISTS loomplane_native_points_source_id_provider_id_created_at_key;
	DROP INDEX IF EXISTS loomplane_native_points_source;
END $native_point_identity_up$`)
			if err != nil {
				return fmt.Errorf("add native point creation identity: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_point_identity_down$
BEGIN
	IF to_regclass('loomplane_native_points') IS NULL THEN
		RETURN;
	END IF;
	IF NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='loomplane_native_points'::regclass AND attname='creation_identity' AND NOT attisdropped) THEN
		RETURN;
	END IF;

	LOCK TABLE loomplane_native_points IN ACCESS EXCLUSIVE MODE;
	IF EXISTS(SELECT 1 FROM loomplane_native_points) THEN
		RAISE EXCEPTION 'native recovery creation identities are still retained' USING ERRCODE='55000';
	END IF;

	ALTER TABLE loomplane_native_points DROP CONSTRAINT IF EXISTS loomplane_native_points_creation_identity_key;
	ALTER TABLE loomplane_native_points DROP CONSTRAINT IF EXISTS loomplane_native_points_creation_time_utc;
	DROP INDEX IF EXISTS loomplane_native_points_identity_order;
	ALTER TABLE loomplane_native_points DROP COLUMN creation_identity;

	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_native_points'::regclass AND conname='loomplane_native_points_source_id_provider_id_created_at_key') THEN
		ALTER TABLE loomplane_native_points ADD CONSTRAINT loomplane_native_points_source_id_provider_id_created_at_key UNIQUE(source_id,provider_id,created_at);
	END IF;

	CREATE INDEX IF NOT EXISTS loomplane_native_points_source ON loomplane_native_points(org_id,source_id,created_at DESC,id DESC);
END $native_point_identity_down$`)
			if err != nil {
				return fmt.Errorf("drop native point creation identity: %w", err)
			}
			return nil
		},
	)
}
