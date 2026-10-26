package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Foreign keys serialize connection deletion with plan/binding writes. The
// retained uses include targets, builders, registries and replacement cleanup.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_connections_org_id").IfNotExists().Unique().
				On("loomplane_cloud_connections").
				Columns("org_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_connection_uses").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("owner_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("kind", "owner_id", "connection_id").
				ForeignKey("org_id", "connection_id").References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_connection_ids(doc JSONB) RETURNS SETOF TEXT
LANGUAGE SQL IMMUTABLE AS $$
SELECT DISTINCT value #>> '{}'
FROM (
	SELECT jsonb_path_query(doc, '$.**.connection_id') AS value
	UNION ALL SELECT jsonb_path_query(doc, '$.**.target')
	UNION ALL SELECT jsonb_path_query(doc, '$.**.builder')
	UNION ALL SELECT jsonb_path_query(doc, '$.**.registry')
) refs
WHERE jsonb_typeof(value) = 'string' AND value #>> '{}' ~ '^conn_[0-7][0-9a-hjkmnp-tv-z]{25}$'
$$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_connection_ids: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_sync_plan_connection_uses(plan TEXT) RETURNS VOID
LANGUAGE plpgsql AS $$
DECLARE
	p loomplane_plans%ROWTYPE;
	doc JSONB;
BEGIN
	DELETE FROM loomplane_connection_uses WHERE kind='plan' AND owner_id=plan;
	SELECT * INTO p FROM loomplane_plans WHERE id=plan;
	IF NOT FOUND OR p.status NOT IN ('planning','ready','approved') THEN RETURN; END IF;
	IF p.status='approved' AND EXISTS (SELECT 1 FROM loomplane_deployments WHERE plan_id=plan AND status='succeeded') THEN RETURN; END IF;

	doc := coalesce(p.result, '{}') || jsonb_build_object('defaults',
		coalesce((SELECT jsonb_agg(jsonb_build_object('connection_id', value)) FROM jsonb_each_text(coalesce(p.result->'connections','{}'))), '[]'));
	INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
		SELECT p.org_id,id,'plan',plan FROM loomplane_connection_ids(doc) id;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_sync_plan_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_plan_connection_uses_trigger() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='plan' AND owner_id=OLD.id;
	ELSE
		PERFORM loomplane_sync_plan_connection_uses(NEW.id);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_plan_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_binding_connection_uses_trigger() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='binding' AND owner_id=OLD.id;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='binding' AND owner_id=NEW.id;
		INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
			SELECT NEW.org_id,id,'binding',NEW.id FROM loomplane_connection_ids(to_jsonb(NEW)) id;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_binding_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_deployment_connection_uses_trigger() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		PERFORM loomplane_sync_plan_connection_uses(OLD.plan_id);
	ELSE
		PERFORM loomplane_sync_plan_connection_uses(NEW.plan_id);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_deployment_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_plan_connection_uses
AFTER INSERT OR UPDATE OR DELETE ON loomplane_plans
FOR EACH ROW EXECUTE FUNCTION loomplane_plan_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_plan_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_binding_connection_uses
AFTER INSERT OR UPDATE OR DELETE ON loomplane_bindings
FOR EACH ROW EXECUTE FUNCTION loomplane_binding_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_binding_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_deployment_connection_uses
AFTER INSERT OR UPDATE OR DELETE ON loomplane_deployments
FOR EACH ROW EXECUTE FUNCTION loomplane_deployment_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_deployment_connection_uses: %w", err)
			}

			// Existing broken references stay visible to the planner. Only extant
			// connections can be backfilled into the deletion guard.
			_, err = db.Exec(ctx, `INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
SELECT b.org_id,refs.id,'binding',b.id
FROM loomplane_bindings b
CROSS JOIN LATERAL loomplane_connection_ids(to_jsonb(b)) refs(id)
JOIN loomplane_cloud_connections c ON c.org_id=b.org_id AND c.id=refs.id
ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill binding connection uses: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
SELECT p.org_id,refs.id,'plan',p.id
FROM loomplane_plans p
CROSS JOIN LATERAL loomplane_connection_ids(coalesce(p.result,'{}') || jsonb_build_object('defaults',
	coalesce((SELECT jsonb_agg(jsonb_build_object('connection_id', value)) FROM jsonb_each_text(coalesce(p.result->'connections','{}'))),'[]'))) refs(id)
JOIN loomplane_cloud_connections c ON c.org_id=p.org_id AND c.id=refs.id
WHERE p.status IN ('planning','ready','approved') AND NOT EXISTS (SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=p.id AND d.status='succeeded')
ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill plan connection uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_plan_connection_uses ON loomplane_plans`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_plan_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_binding_connection_uses ON loomplane_bindings`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_binding_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_deployment_connection_uses ON loomplane_deployments`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_deployment_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_plan_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_plan_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_binding_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_binding_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_deployment_connection_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_deployment_connection_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_sync_plan_connection_uses(TEXT)`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_sync_plan_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_connection_ids(JSONB)`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_connection_ids: %w", err)
			}

			err = db.NewDropTable("loomplane_connection_uses").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropIndex("loomplane_connections_org_id").IfExists().Exec(ctx)
		},
	)
}
