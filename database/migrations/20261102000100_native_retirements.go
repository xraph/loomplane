package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_native_retirements").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("source_id", migrate.Text, migrate.NotNull()).
				Column("point_id", migrate.Text, migrate.NotNull()).
				Column("witness_id", migrate.Text, migrate.NotNull(), migrate.Check("point_id<>witness_id")).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("completed_at", migrate.Timestamp).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','failed','succeeded')")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("policy", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(policy)='object'")).
				Column("source", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(source)='object'")).
				Column("point", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(point)='object'")).
				Column("witness", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(witness)='object'")).
				Column("attempted", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("token", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("lease_until", migrate.Timestamp).
				Column("policy_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Unique("org_id", "source_id", "request_id").
				Unique("org_id", "source_id", "point_id").
				ForeignKey("org_id", "source_id").
				References("loomplane_native_sources", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "source_id", "point_id").
				References("loomplane_native_points", "org_id", "source_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "source_id", "witness_id").
				References("loomplane_native_points", "org_id", "source_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_retirements_active").Unique().IfNotExists().
				On("loomplane_native_retirements").
				Columns("org_id", "source_id").
				Where("status<>'succeeded'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_retirements_work").IfNotExists().
				On("loomplane_native_retirements").
				Columns("status", "created_at", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_native_retirement_events").IfNotExists().
				Column("id", migrate.BigSerial, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("retirement_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_native_retirements", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("actor", migrate.Text, migrate.NotNull()).
				Column("error", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_retirement_events_scope").IfNotExists().
				On("loomplane_native_retirement_events").
				Columns("org_id", "retirement_id").
				ColumnDesc("id").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_native_retirement_connection_uses() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='native_retirement' AND owner_id=OLD.id;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='native_retirement' AND owner_id=NEW.id;
		IF NEW.status<>'succeeded' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
			VALUES(NEW.org_id,NEW.source->>'connection_id','native_retirement',NEW.id);
		END IF;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_native_retirement_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_native_retirement_connection_uses
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_native_retirements
	FOR EACH ROW EXECUTE FUNCTION loomplane_native_retirement_connection_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_native_retirement_connection_uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_retirements_down$
BEGIN
	IF to_regclass('loomplane_native_retirements') IS NOT NULL THEN
		LOCK TABLE loomplane_native_retirements IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_retirements) THEN
			RAISE EXCEPTION 'native deletion history is retained' USING ERRCODE='55000';
		END IF;
	END IF;

	DROP TABLE IF EXISTS loomplane_native_retirement_events;
	DROP TABLE IF EXISTS loomplane_native_retirements;
	DROP FUNCTION IF EXISTS loomplane_native_retirement_connection_uses();
END $native_retirements_down$`)
			if err != nil {
				return fmt.Errorf("drop native retirements: %w", err)
			}
			return nil
		},
	)
}
