package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_retention_policies").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("anchor_id", migrate.Text, migrate.NotNull()).
				Column("source_key", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.Integer, migrate.NotNull(), migrate.Check("revision>0")).
				Column("policy", migrate.JSON, migrate.NotNull()).
				Column("enabled", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("created_by", migrate.Text, migrate.NotNull(), migrate.Check("created_by<>''")).
				Column("authorized_by", migrate.Text, migrate.NotNull(), migrate.Check("authorized_by<>''")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("next_at", migrate.Timestamp, migrate.NotNull()).
				Column("last_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("last_retirement_id", migrate.Text).
				Unique("org_id", "source_key").
				Unique("org_id", "project_id", "id").
				ForeignKey("org_id", "project_id", "anchor_id").
				References("loomplane_service_backups", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "last_retirement_id").
				References("loomplane_backup_retirements", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_retention_policies_due").IfNotExists().
				On("loomplane_retention_policies").
				Columns("next_at", "id").
				Where("enabled=true").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_retention_policy_revisions").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("policy_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.Integer, migrate.NotNull(), migrate.Check("revision>0")).
				Column("policy", migrate.JSON, migrate.NotNull()).
				Column("enabled", migrate.Boolean, migrate.NotNull()).
				Column("authorized_by", migrate.Text, migrate.NotNull(), migrate.Check("authorized_by<>''")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "project_id", "policy_id", "revision").
				ForeignKey("org_id", "project_id", "policy_id").
				References("loomplane_retention_policies", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_backup_retirements").
				AddColumn("retention_policy_id", migrate.Text).IfNotExists().
				AddColumn("retention_revision", migrate.Integer).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_backup_retirements'::regclass AND conname='loomplane_retirement_policy_reference') THEN
		ALTER TABLE loomplane_backup_retirements
			ADD CONSTRAINT loomplane_retirement_policy_reference CHECK((retention_policy_id IS NULL)=(retention_revision IS NULL)),
			ADD CONSTRAINT loomplane_retirement_policy_revision FOREIGN KEY(org_id,project_id,retention_policy_id,retention_revision)
				REFERENCES loomplane_retention_policy_revisions(org_id,project_id,policy_id,revision) ON DELETE RESTRICT;
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add retirement retention policy references: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Keep the emptiness decision and destruction in one transaction. The table
			// lock also fences a policy writer that did not acquire the migration lock.
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('loomplane_retention_policies') IS NOT NULL THEN
		LOCK TABLE loomplane_retention_policies IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_retention_policies) THEN
			RAISE EXCEPTION 'retention policy authorization history must be preserved';
		END IF;
	END IF;

	ALTER TABLE loomplane_backup_retirements
		DROP CONSTRAINT IF EXISTS loomplane_retirement_policy_reference,
		DROP CONSTRAINT IF EXISTS loomplane_retirement_policy_revision,
		DROP COLUMN IF EXISTS retention_policy_id,
		DROP COLUMN IF EXISTS retention_revision;
	DROP TABLE IF EXISTS loomplane_retention_policy_revisions;
	DROP TABLE IF EXISTS loomplane_retention_policies;
END $$`)
			if err != nil {
				return fmt.Errorf("drop retention policies: %w", err)
			}
			return nil
		},
	)
}
