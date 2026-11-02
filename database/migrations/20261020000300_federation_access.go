package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_policy_versions").IfNotExists().
				Column("org_id", migrate.Text, migrate.PrimaryKey()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(1), migrate.Check("generation>0")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_exports").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("state", migrate.JSON, migrate.NotNull()).
				PrimaryKey("org_id", "id").
				Unique("org_id", "project_id", "environment_id", "id").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_export_destinations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("export_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("org_id", "export_id", "destination_id").
				ForeignKey("org_id", "project_id", "environment_id", "export_id").
				References("loomplane_federation_exports", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_grants").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("export_id", migrate.Text, migrate.NotNull()).
				Column("source_project_id", migrate.Text, migrate.NotNull()).
				Column("source_environment_id", migrate.Text, migrate.NotNull()).
				Column("source_destination_id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("state", migrate.JSON, migrate.NotNull()).
				PrimaryKey("org_id", "id").
				ForeignKey("org_id", "project_id", "environment_id", "export_id").
				References("loomplane_federation_exports", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				ForeignKey("org_id", "source_project_id", "source_environment_id", "source_destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_grant_sources").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("grant_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("org_id", "grant_id", "project_id", "environment_id", "destination_id").
				ForeignKey("org_id", "grant_id").
				References("loomplane_federation_grants", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_grants_source").IfNotExists().
				On("loomplane_federation_grants").
				Columns("org_id", "source_project_id", "source_environment_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_policy_history").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("entity_id", migrate.Text, migrate.NotNull()).
				Column("actor_id", migrate.Text, migrate.NotNull()).
				Column("state", migrate.JSON, migrate.NotNull()).
				Column("occurred_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "generation").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_policy_change(org TEXT,kind TEXT,entity TEXT,actor TEXT,body JSONB) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE
	revision BIGINT;
BEGIN
	UPDATE loomplane_federation_policy_versions SET generation=generation+1 WHERE org_id=org RETURNING generation INTO revision;
	IF revision IS NULL THEN
		RAISE EXCEPTION 'policy version is missing';
	END IF;

	INSERT INTO loomplane_federation_policy_history(org_id,generation,kind,entity_id,actor_id,state)
	VALUES(org,revision,kind,entity,actor,body);
	PERFORM loomplane_append_event(org,'','federation.policy.changed',entity,jsonb_build_object('generation',revision));
	RETURN revision;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_policy_change: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_quarantine_policy() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO loomplane_federation_policy_versions(org_id) VALUES(NEW.org_id) ON CONFLICT DO NOTHING;
	PERFORM loomplane_federation_policy_change(NEW.org_id,'destination.quarantine',NEW.id,'system',
		jsonb_build_object('destination_id',NEW.id,'quarantined',NEW.quarantined));
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_quarantine_policy: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_quarantine_policy
	AFTER UPDATE OF quarantined ON loomplane_federation_destinations
	FOR EACH ROW
	WHEN (OLD.quarantined IS DISTINCT FROM NEW.quarantined)
	EXECUTE FUNCTION loomplane_federation_quarantine_policy()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_quarantine_policy: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_federation_quarantine_policy ON loomplane_federation_destinations`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_federation_quarantine_policy: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_quarantine_policy()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_quarantine_policy: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_policy_change(TEXT,TEXT,TEXT,TEXT,JSONB)`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_policy_change: %w", err)
			}

			err = db.NewDropTable("loomplane_federation_grant_sources").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_grants").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_export_destinations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_exports").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_policy_history").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_federation_policy_versions").IfExists().Exec(ctx)
		},
	)
}
