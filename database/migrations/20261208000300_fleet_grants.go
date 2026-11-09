package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_grants").
			Column("id", migrate.Text, migrate.PrimaryKey()).Column("org_id", migrate.Text, migrate.NotNull()).Column("project_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("stage_id", migrate.Text, migrate.NotNull()).Column("credential_id", migrate.Text, migrate.NotNull()).Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('active','revoked')")).
			Column("snapshot", migrate.Text, migrate.NotNull()).Column("expires_at", migrate.Timestamp, migrate.NotNull()).Column("created_by", migrate.Text, migrate.NotNull()).Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "fleet_id", "id").ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "stage_id").References("loomplane_fleet_stages", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_authorities").
			Column("id", migrate.Text, migrate.PrimaryKey()).Column("org_id", migrate.Text, migrate.NotNull()).Column("project_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("grant_id", migrate.Text, migrate.NotNull()).Column("grant_revision", migrate.BigInt, migrate.NotNull()).
			Column("credential_id", migrate.Text, migrate.NotNull()).Column("kind", migrate.Text, migrate.NotNull()).Column("external_ref", migrate.Text, migrate.NotNull()).Column("idempotency_key", migrate.Text, migrate.NotNull()).Column("request_hash", migrate.Text, migrate.NotNull()).Column("snapshot", migrate.Text, migrate.NotNull()).
			Column("cpu_millis", migrate.BigInt, migrate.NotNull(), migrate.Check("cpu_millis>0")).Column("memory_mib", migrate.BigInt, migrate.NotNull(), migrate.Check("memory_mib>0")).Column("storage_gib", migrate.BigInt, migrate.NotNull(), migrate.Check("storage_gib>=0")).Column("hourly_cost_micros", migrate.BigInt, migrate.NotNull(), migrate.Check("hourly_cost_micros>=0")).Column("expires_at", migrate.Timestamp, migrate.NotNull()).Column("released_at", migrate.Timestamp).
			Unique("org_id", "fleet_id", "credential_id", "kind", "idempotency_key").Unique("org_id", "fleet_id", "grant_id", "external_ref").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "grant_id").References("loomplane_fleet_grants", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		return db.NewCreateIndex("loomplane_fleet_authority_budget").On("loomplane_fleet_authorities").Columns("org_id", "fleet_id", "grant_id").Where("released_at IS NULL").Exec(ctx)
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_grants,loomplane_fleet_authorities IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_grants) THEN RAISE EXCEPTION 'fleet grant history must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_authorities").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_fleet_grants").Exec(ctx)
	})
}
