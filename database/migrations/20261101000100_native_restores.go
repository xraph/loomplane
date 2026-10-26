package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_native_sources_restore_scope").Unique().IfNotExists().
				On("loomplane_native_sources").
				Columns("org_id", "project_id", "environment_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_points_restore_scope").Unique().IfNotExists().
				On("loomplane_native_points").
				Columns("org_id", "source_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_native_restores").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("source_id", migrate.Text, migrate.NotNull()).
				Column("point_id", migrate.Text, migrate.NotNull()).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("source", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(source)='object'")).
				Column("point", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(point)='object'")).
				Column("target", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(target)='object'")).
				Column("checkpoint", migrate.JSON, migrate.NotNull(), migrate.Default("{}"),
					migrate.Check("jsonb_typeof(checkpoint)='object'")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','succeeded','failed','deleting','cleanup_failed','deleted')")).
				Column("token", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("lease_until", migrate.Timestamp).
				Column("external_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("physical_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull(), migrate.Check("expires_at>created_at")).
				Column("verified_at", migrate.Timestamp).
				Column("duration_ms", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("duration_ms>=0")).
				Unique("org_id", "project_id", "request_id").
				ForeignKey("org_id", "project_id", "environment_id", "source_id").
				References("loomplane_native_sources", "org_id", "project_id", "environment_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "source_id", "point_id").
				References("loomplane_native_points", "org_id", "source_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_restores_scope").IfNotExists().
				On("loomplane_native_restores").
				Columns("org_id", "project_id", "environment_id").
				ColumnDesc("created_at").
				ColumnDesc("id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_restores_pending").IfNotExists().
				On("loomplane_native_restores").
				Columns("status", "lease_until", "expires_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_native_restore_connection_uses() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='native_restore' AND owner_id=OLD.id;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='native_restore' AND owner_id=NEW.id;
		IF NEW.status<>'deleted' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
			SELECT NEW.org_id,id,'native_restore',NEW.id
			FROM (SELECT NEW.source->>'connection_id' AS id UNION SELECT NEW.target->>'connection_id') refs;
		END IF;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_native_restore_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_native_restore_connection_uses
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_native_restores
	FOR EACH ROW EXECUTE FUNCTION loomplane_native_restore_connection_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_native_restore_connection_uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_restores_down$
BEGIN
	IF to_regclass('loomplane_native_restores') IS NOT NULL THEN
		LOCK TABLE loomplane_native_restores IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_restores) THEN
			RAISE EXCEPTION 'native restore history is still retained' USING ERRCODE='55000';
		END IF;

		DROP TRIGGER IF EXISTS loomplane_native_restore_connection_uses ON loomplane_native_restores;
		DROP TABLE loomplane_native_restores;
	END IF;

	DROP FUNCTION IF EXISTS loomplane_native_restore_connection_uses();
	DROP INDEX IF EXISTS loomplane_native_points_restore_scope;
	DROP INDEX IF EXISTS loomplane_native_sources_restore_scope;
END $native_restores_down$`)
			if err != nil {
				return fmt.Errorf("drop native restores: %w", err)
			}
			return nil
		},
	)
}
