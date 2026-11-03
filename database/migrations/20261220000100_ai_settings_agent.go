package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The workspace's infra agent settings, as columns on its AI settings row:
// whether it's on, the provider key it uses (a loomplane_ai_provider_keys
// name, empty for the server's key), its model (empty for the default),
// whether it uses knowledge, and the nexus tenant it's billed under. Its
// budget is the "agent" row in loomplane_ai_budget_allocations, so it has
// no column. No two workspaces share a tenant; many have none until the
// agent first turns on.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_ai_settings").
				AddColumn("agent_enabled", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("agent_provider_key", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("agent_model", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("agent_knowledge", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("nexus_tenant_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewCreateIndex("loomplane_ai_settings_nexus_tenant").IfNotExists().
				On("loomplane_ai_settings").
				Columns("nexus_tenant_id").
				Unique().
				Where("nexus_tenant_id <> ''").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropIndex("loomplane_ai_settings_nexus_tenant").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewAlterTable("loomplane_ai_settings").
				DropColumn("nexus_tenant_id").IfExists().
				DropColumn("agent_knowledge").IfExists().
				DropColumn("agent_model").IfExists().
				DropColumn("agent_provider_key").IfExists().
				DropColumn("agent_enabled").IfExists().
				Exec(ctx)
		},
	)
}
