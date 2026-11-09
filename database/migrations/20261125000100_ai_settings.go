package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Workspace AI settings: one row per organization with its monthly AI
// budget and embeddings choice, the provider keys its gateways use, and the
// share of the budget each gateway (or the agent) is given.
//
// A provider key row holds only metadata: the key values live in Vault, and
// key_hint is the last four characters of the primary one. An allocation
// has no foreign key to projects; whoever owns its scope deletes it.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_ai_settings").IfNotExists().
				Column("org_id", migrate.Text, migrate.PrimaryKey()).
				Column("monthly_budget_usd", migrate.Numeric(12, 2), migrate.NotNull(), migrate.Default(0),
					migrate.Check("monthly_budget_usd >= 0")).
				Column("embeddings", migrate.JSON, migrate.NotNull(),
					migrate.Default(`{"mode":"local","model":"granite-embedding-107m-multilingual","dimensions":384}`)).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("updated_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_ai_provider_keys").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("type", migrate.Text, migrate.NotNull()).
				Column("fields", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("models", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("key_hint", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("projects", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.Default("{}")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("updated_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Unique("org_id", "name").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_ai_budget_allocations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("scope", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("label", migrate.Text, migrate.NotNull()).
				Column("monthly_budget_usd", migrate.Numeric(12, 2), migrate.NotNull(),
					migrate.Check("monthly_budget_usd > 0")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "scope").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{"loomplane_ai_budget_allocations", "loomplane_ai_provider_keys", "loomplane_ai_settings"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
