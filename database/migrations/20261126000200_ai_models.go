package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Model deployments, one row per model resource in an environment: what
// was asked for, where it's going, its budget and what it cost this month.
// The manifest and bindings say what runs; this table holds what they
// don't. A deployment runs only in its environment. No key or token is
// stored here: the model's API key lives in Vault.
//
// Custom models are entries built from a pasted Hugging Face repo, kept per
// workspace once one is deployed, under "hf:<repo>@<sha12>".
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_ai_model_deployments").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("entry_id", migrate.Text, migrate.NotNull()).
				Column("quantization", migrate.Text, migrate.NotNull()).
				Column("engine", migrate.Text, migrate.NotNull()).
				Column("monthly_budget_usd", migrate.Numeric(12, 2), migrate.Check("monthly_budget_usd > 0")).
				Column("hourly_usd_override", migrate.Numeric(10, 4), migrate.Check("hourly_usd_override >= 0")).
				Column("phase", migrate.Text, migrate.NotNull(),
					migrate.Check("phase IN ('waiting_for_server', 'planned', 'active', 'needs_attention')")).
				Column("server_cloud_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("server_operation_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("server_name", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("message", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("request", migrate.JSON).
				Column("requested_by", migrate.JSON).
				Column("month", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("month_cost_usd", migrate.Numeric(12, 4), migrate.NotNull(), migrate.Default(0)).
				Column("accrued_at", migrate.Timestamp).
				Column("alerted_projected", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("alerted_crossed", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Unique("org_id", "project_id", "environment_id", "resource").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_ai_model_deployments_project").IfNotExists().
				On("loomplane_ai_model_deployments").
				Columns("org_id", "project_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_ai_model_deployments_waiting").IfNotExists().
				On("loomplane_ai_model_deployments").
				Columns("phase").
				Where("phase = 'waiting_for_server'").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_ai_custom_models").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("entry_id", migrate.Text, migrate.NotNull()).
				Column("entry", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "entry_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{"loomplane_ai_custom_models", "loomplane_ai_model_deployments"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
