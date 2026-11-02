package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_backup_retirements").
				AddColumn("execution_by", migrate.Text).
				AddColumn("attempt_limit", migrate.Integer, migrate.NotNull(), migrate.Default(3),
					migrate.Check("attempt_limit>=3 AND attempt_limit>=attempt")).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_backup_retirements SET execution_by=created_by`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_backup_retirements.execution_by: %w", err)
			}

			err = db.NewAlterTable("loomplane_backup_retirements").
				SetNotNull("execution_by").
				AddCheck("execution_by<>''").Named("loomplane_backup_retirements_execution").
				AddUnique("org_id", "project_id", "id").Named("loomplane_backup_retirements_scope").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_backup_retirement_events").
				Column("sequence", migrate.BigInt, migrate.Identity(), migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("retirement_id", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull(),
					migrate.Check("kind IN ('imported','requested','retry','claimed','failed','succeeded','expired')")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','failed','succeeded')")).
				Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Check("attempt>=0")).
				Column("attempt_limit", migrate.Integer, migrate.NotNull(), migrate.Check("attempt_limit>=3")).
				Column("actor", migrate.Text, migrate.NotNull(), migrate.Check("actor<>''")).
				Column("previous_actor", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("previous_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("request_id", migrate.Text).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Unique("org_id", "project_id", "request_id").
				Check("(kind='retry')=(request_id IS NOT NULL)").
				ForeignKey("org_id", "project_id", "retirement_id").
				References("loomplane_backup_retirements", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_backup_retirement_events_history").
				On("loomplane_backup_retirement_events").
				Columns("org_id", "retirement_id").
				ColumnDesc("sequence").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Existing records establish current state, not a reconstructed attempt log.
			_, err = db.Exec(ctx, `INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,error)
SELECT org_id,project_id,id,'imported',status,attempt,attempt_limit,execution_by,error
FROM loomplane_backup_retirements`)
			if err != nil {
				return fmt.Errorf("import backup retirement events: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_backup_retirement_events) THEN
		RAISE EXCEPTION 'backup retirement execution history must be preserved';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check backup retirement execution history: %w", err)
			}

			err = db.NewDropTable("loomplane_backup_retirement_events").Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_backup_retirements").
				DropConstraint("loomplane_backup_retirements_scope").
				DropConstraint("loomplane_backup_retirements_execution").
				DropColumn("execution_by").
				DropColumn("attempt_limit").
				Exec(ctx)
		},
	)
}
