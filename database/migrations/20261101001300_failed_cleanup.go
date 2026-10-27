package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_failed_cleanups").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("deployment_id", migrate.Text, migrate.PrimaryKey(), migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Restrict)).
			Column("revision", migrate.Text, migrate.NotNull()).
			Column("actor", migrate.JSON, migrate.NotNull()).
			Column("status", migrate.Text, migrate.NotNull(), migrate.Default("pending"), migrate.Check("status IN ('pending','failed','succeeded')")).
			Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Default("1")).
			Column("delivered_attempt", migrate.Integer, migrate.NotNull(), migrate.Default("0")).
			Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
			Column("checkpoint", migrate.Bytes, migrate.NotNull()).
			Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_failed_cleanup_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.org_id<>OLD.org_id OR NEW.deployment_id<>OLD.deployment_id OR NEW.revision<>OLD.revision OR NEW.actor<>OLD.actor THEN
  RAISE EXCEPTION 'accepted cleanup authority cannot change' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_failed_cleanup_guard BEFORE UPDATE OR DELETE ON loomplane_failed_cleanups FOR EACH ROW EXECUTE FUNCTION loomplane_failed_cleanup_guard()`)
		if err != nil {
			return err
		}
		if _, err = db.Exec(ctx, `CREATE FUNCTION loomplane_abandoned_deployment_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_failed_cleanups c WHERE c.deployment_id=OLD.id) AND
  (NEW.status<>'cancelled' OR NEW.steps<>OLD.steps OR NEW.retries<>OLD.retries OR NEW.plan_id<>OLD.plan_id OR NEW.org_id<>OLD.org_id OR NEW.environment_id<>OLD.environment_id) THEN
  RAISE EXCEPTION 'abandoned deployment evidence cannot change' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$`); err != nil {
			return err
		}
		_, err = db.Exec(ctx, `CREATE TRIGGER loomplane_abandoned_deployment_guard BEFORE UPDATE ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_abandoned_deployment_guard()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DROP TRIGGER loomplane_abandoned_deployment_guard ON loomplane_deployments`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_abandoned_deployment_guard()`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_failed_cleanups").Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP FUNCTION loomplane_failed_cleanup_guard()`)
		return err
	})
}
