package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_scaling_policies").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("environment_id", migrate.Text, migrate.NotNull()).
			Column("operation_id", migrate.Text, migrate.NotNull()).
			Column("service", migrate.Text, migrate.NotNull(), migrate.Check("service ~ '^[a-z][a-z0-9-]{0,39}$'")).
			Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
			Column("ownership_epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("ownership_epoch>0")).
			Column("input", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(input) BETWEEN 1 AND 4096")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			PrimaryKey("org_id", "instance_id", "service", "revision").
			Unique("org_id", "operation_id").
			Unique("org_id", "project_id", "fleet_id", "instance_id", "operation_id", "service", "revision").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "environment_id").References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "instance_id", "operation_id").References("loomplane_fleet_operations", "org_id", "fleet_id", "instance_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_scaling_writer_events").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("operation_id", migrate.Text, migrate.NotNull()).
			Column("service", migrate.Text, migrate.NotNull()).
			Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
			Column("sequence", migrate.BigInt, migrate.NotNull(), migrate.Check("sequence>0")).
			Column("phase", migrate.Text, migrate.NotNull(), migrate.Check("phase IN ('revoking','quiesced','applying','active')")).
			Column("proof_id", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(proof_id) BETWEEN 1 AND 512")).
			Column("payload", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(payload) BETWEEN 1 AND 16384")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			PrimaryKey("org_id", "instance_id", "service", "policy_revision", "sequence").
			Unique("org_id", "proof_id").
			ForeignKey("org_id", "project_id", "fleet_id", "instance_id", "operation_id", "service", "policy_revision").References("loomplane_fleet_scaling_policies", "org_id", "project_id", "fleet_id", "instance_id", "operation_id", "service", "revision").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_scaling_history_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fleet scaling history is immutable' USING ERRCODE='23514'; END $$`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_scaling_policy_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_scaling_policies FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_scaling_history_immutable()`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_scaling_writer_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_scaling_writer_events FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_scaling_history_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_scaling_policies,loomplane_fleet_scaling_writer_events IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_scaling_policies) OR EXISTS(SELECT 1 FROM loomplane_fleet_scaling_writer_events) THEN RAISE EXCEPTION 'fleet scaling history must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_scaling_writer_events").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_scaling_policies").Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_scaling_history_immutable()`)
		return err
	})
}
