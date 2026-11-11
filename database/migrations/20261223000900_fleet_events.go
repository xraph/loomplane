package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_event_heads").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("sequence", migrate.BigInt, migrate.NotNull(), migrate.Check("sequence>=0")).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>=0")).
			PrimaryKey("org_id", "instance_id").
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_events").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("sequence", migrate.BigInt, migrate.NotNull(), migrate.Check("sequence>0")).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
			Column("occurred_at", migrate.Timestamp, migrate.NotNull()).
			Column("payload_hash", migrate.Text, migrate.NotNull()).
			Column("payload", migrate.JSON).
			Column("delivery_state", migrate.Text, migrate.NotNull(), migrate.Default("pending"), migrate.Check("delivery_state IN ('pending','accepted','dead_letter')")).
			Column("delivery_attempts", migrate.Integer, migrate.NotNull(), migrate.DefaultExpr("0"), migrate.Check("delivery_attempts BETWEEN 0 AND 10")).
			Column("next_delivery_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
			Column("delivery_lease", migrate.Text, migrate.NotNull(), migrate.Default("")).
			Column("delivery_lease_until", migrate.Timestamp).
			Column("delivery_error", migrate.Text, migrate.NotNull(), migrate.Default(""), migrate.Check("delivery_error IN ('','acceptance_failed','endpoint_unavailable','lease_expired','event_expired')")).
			Unique("org_id", "instance_id", "sequence").
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("idx_fleet_events_pending").On("loomplane_fleet_events").Columns("next_delivery_at", "id").Where("delivery_state='pending'").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("idx_fleet_events_retention").On("loomplane_fleet_events").Columns("occurred_at", "id").Where("payload IS NOT NULL").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_event_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
   IF ROW(NEW.id,NEW.org_id,NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.sequence,NEW.generation,NEW.occurred_at,NEW.payload_hash) IS DISTINCT FROM ROW(OLD.id,OLD.org_id,OLD.project_id,OLD.fleet_id,OLD.instance_id,OLD.sequence,OLD.generation,OLD.occurred_at,OLD.payload_hash) OR (NEW.payload IS DISTINCT FROM OLD.payload AND NEW.payload IS NOT NULL) THEN RAISE EXCEPTION 'fleet event receipt is immutable' USING ERRCODE='23514'; END IF;
   RETURN NEW;
  END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER fleet_event_immutable BEFORE UPDATE ON loomplane_fleet_events FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_event_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewDropTable("loomplane_fleet_events").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_event_immutable()`); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_fleet_event_heads").Exec(ctx)
	})
}
