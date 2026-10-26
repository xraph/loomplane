package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_native_sources").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey(), migrate.DefaultExpr("gen_random_uuid()::text")).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("binding_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("region", migrate.Text, migrate.NotNull()).
				Column("physical_id", migrate.Text, migrate.NotNull(), migrate.Check("physical_id<>''")).
				Column("source", migrate.JSON, migrate.NotNull(),
					migrate.Check("jsonb_typeof(source)='object' AND source->>'physical_id'=physical_id")).
				Column("captured_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Unique("org_id", "binding_id", "provider", "connection_id", "region", "physical_id").
				Unique("org_id", "id").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_sources_project").IfNotExists().
				On("loomplane_native_sources").
				Columns("org_id", "project_id", "captured_at", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_capture_native_sources(root JSONB) RETURNS void LANGUAGE sql AS $capture_native_sources$
	WITH RECURSIVE history(source) AS (
		SELECT root
		UNION ALL
		SELECT child.value
		FROM history h
		CROSS JOIN LATERAL jsonb_array_elements(
			CASE WHEN jsonb_typeof(h.source->'replaced')='array' THEN h.source->'replaced' ELSE '[]'::jsonb END) child
	)
	INSERT INTO loomplane_native_sources(org_id,project_id,environment_id,binding_id,name,provider,connection_id,region,physical_id,source)
	SELECT source->>'org_id',source->>'project_id',source->>'environment_id',source->>'id',source->>'name',source->>'provider',source->>'connection_id',source->>'region',source->>'physical_id',source-'url'-'replaced'-'drift_message'
	FROM history
	WHERE source->>'subject'='resource' AND source->>'provider' IN ('rds','cloud-sql') AND coalesce(source->>'physical_id','')<>''
		AND source->>'org_id'=root->>'org_id' AND source->>'project_id'=root->>'project_id' AND source->>'environment_id'=root->>'environment_id'
	ON CONFLICT(org_id,binding_id,provider,connection_id,region,physical_id) DO NOTHING
$capture_native_sources$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_capture_native_sources: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_binding_native_sources() RETURNS trigger LANGUAGE plpgsql AS $binding_native_sources$
BEGIN
	IF TG_OP='UPDATE' THEN
		PERFORM loomplane_capture_native_sources(to_jsonb(OLD));
	END IF;
	PERFORM loomplane_capture_native_sources(to_jsonb(NEW));
	RETURN NEW;
END $binding_native_sources$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_binding_native_sources: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_binding_native_sources ON loomplane_bindings`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_binding_native_sources: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE TRIGGER loomplane_binding_native_sources
	AFTER INSERT OR UPDATE ON loomplane_bindings
	FOR EACH ROW EXECUTE FUNCTION loomplane_binding_native_sources()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_binding_native_sources: %w", err)
			}

			_, err = db.Exec(ctx, `SELECT loomplane_capture_native_sources(to_jsonb(b)) FROM loomplane_bindings b`)
			if err != nil {
				return fmt.Errorf("capture native sources from loomplane_bindings: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_sources_down$
BEGIN
	IF to_regclass('loomplane_native_sources') IS NOT NULL THEN
		LOCK TABLE loomplane_native_sources IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_sources) THEN
			RAISE EXCEPTION 'native recovery sources are still retained' USING ERRCODE='55000';
		END IF;

		DROP TRIGGER IF EXISTS loomplane_binding_native_sources ON loomplane_bindings;
		DROP FUNCTION IF EXISTS loomplane_binding_native_sources();
		DROP FUNCTION IF EXISTS loomplane_capture_native_sources(JSONB);
		DROP TABLE loomplane_native_sources;
	END IF;
END $native_sources_down$`)
			if err != nil {
				return fmt.Errorf("drop native sources: %w", err)
			}
			return nil
		},
	)
}
