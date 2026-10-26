package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Tags let a manifest select a target by what it is. A Cloud's default
// placement is one provider and account, kept on the Cloud row itself, so a
// Cloud without targets cannot hold a second provider. Its connection is
// retained like a target's.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_tags_valid(tags TEXT[]) RETURNS BOOLEAN LANGUAGE SQL IMMUTABLE AS $$
	SELECT cardinality(tags)<=16
		AND NOT EXISTS(SELECT 1 FROM unnest(tags) t WHERE t IS NULL OR length(t)>32 OR t !~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$')
		AND cardinality(tags)=(SELECT count(DISTINCT t) FROM unnest(tags) t)
$$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_tags_valid: %w", err)
			}

			err = db.NewAlterTable("loomplane_cloud_targets").
				AddColumn("tags", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.Default("{}")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_target_tags') THEN
		ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_cloud_target_tags CHECK(loomplane_cloud_tags_valid(tags));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add constraint loomplane_cloud_target_tags: %w", err)
			}

			err = db.NewAlterTable("loomplane_cloud_profiles").
				AddColumn("default_provider", migrate.Text).IfNotExists().
				AddColumn("default_connection_id", migrate.Text).IfNotExists().
				AddColumn("default_region", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_default_placement') THEN
		ALTER TABLE loomplane_cloud_profiles ADD CONSTRAINT loomplane_cloud_default_placement CHECK(
			(default_provider IS NULL AND default_connection_id IS NULL AND default_region='')
			OR (default_provider IS NOT NULL AND default_connection_id IS NOT NULL AND default_provider<>'' AND default_connection_id<>''));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add constraint loomplane_cloud_default_placement: %w", err)
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_default_connection') THEN
		ALTER TABLE loomplane_cloud_profiles ADD CONSTRAINT loomplane_cloud_default_connection FOREIGN KEY(org_id,default_connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT;
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add constraint loomplane_cloud_default_connection: %w", err)
			}

			return db.NewCreateIndex("loomplane_cloud_profiles_default_connection").IfNotExists().
				On("loomplane_cloud_profiles").
				Columns("org_id", "default_connection_id").
				Where("default_connection_id IS NOT NULL").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropIndex("loomplane_cloud_profiles_default_connection").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_profiles").
				DropConstraint("loomplane_cloud_default_connection").IfExists().
				DropConstraint("loomplane_cloud_default_placement").IfExists().
				DropColumn("default_region").IfExists().
				DropColumn("default_connection_id").IfExists().
				DropColumn("default_provider").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_targets").
				DropConstraint("loomplane_cloud_target_tags").IfExists().
				DropColumn("tags").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_tags_valid(TEXT[])`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_tags_valid: %w", err)
			}
			return nil
		},
	)
}
