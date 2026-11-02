package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_releases").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("digest", migrate.Text, migrate.NotNull(), migrate.Check("digest ~ '^sha256:[a-f0-9]{64}$'")).
			Column("snapshot", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(snapshot) BETWEEN 1 AND 524288")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "fleet_id", "id").Unique("org_id", "fleet_id", "digest").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_release_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'published fleet releases are immutable' USING ERRCODE='23514'; END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_release_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_releases FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_release_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_releases IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_releases) THEN RAISE EXCEPTION 'published fleet releases must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_releases").Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_release_immutable()`)
		return err
	})
}
