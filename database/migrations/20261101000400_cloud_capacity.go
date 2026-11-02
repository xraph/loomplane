package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_capacity_connections_scope").Unique().IfNotExists().
				On("loomplane_cloud_connections").
				Columns("org_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_servers_scope").Unique().IfNotExists().
				On("loomplane_cloud_servers").
				Columns("org_id", "cloud_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_operations_scope").Unique().IfNotExists().
				On("loomplane_infrastructure_operations").
				Columns("org_id", "cloud_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_environments_scope").Unique().IfNotExists().
				On("loomplane_environments").
				Columns("org_id", "project_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_plans_scope").Unique().IfNotExists().
				On("loomplane_plans").
				Columns("org_id", "project_id", "environment_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_capacity_policies").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("enabled", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("require_expansion_approval", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("servers", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("servers>=0")).
				Column("cpu_millis", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("cpu_millis>=0")).
				Column("memory_bytes", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("memory_bytes>=0")).
				Column("storage_bytes", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("storage_bytes>=0")).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Check("NOT enabled OR (servers>0 AND cpu_millis>0 AND memory_bytes>0 AND storage_bytes>0)").
				PrimaryKey("org_id", "cloud_id").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_capacity_inventory").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("pool_id", migrate.Text).
				Column("server_id", migrate.Text).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_id", migrate.Text, migrate.NotNull()).
				Column("resource_id", migrate.Text, migrate.NotNull()).
				Column("servers", migrate.BigInt, migrate.Check("servers>=0")).
				Column("cpu_millis", migrate.BigInt, migrate.Check("cpu_millis>=0")).
				Column("memory_bytes", migrate.BigInt, migrate.Check("memory_bytes>=0")).
				Column("storage_bytes", migrate.BigInt, migrate.Check("storage_bytes>=0")).
				Column("observed_storage_bytes", migrate.BigInt, migrate.Check("observed_storage_bytes>=0")).
				Column("checked_at", migrate.Timestamp).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				PrimaryKey("org_id", "cloud_id", "id").
				Unique("org_id", "provider", "account_id", "resource_id").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "pool_id").References("loomplane_server_pools", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "server_id").References("loomplane_cloud_servers", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_expansions").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("pool_id", migrate.Text, migrate.NotNull()).
				Column("operation_id", migrate.Text).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('awaiting_approval','reserved','provisioning','enrolling','ready','blocked','cancelled')")).
				Column("intent_hash", migrate.Text, migrate.NotNull()).
				Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
				Column("servers", migrate.BigInt, migrate.NotNull(), migrate.Check("servers>=0")).
				Column("cpu_millis", migrate.BigInt, migrate.NotNull(), migrate.Check("cpu_millis>=0")).
				Column("memory_bytes", migrate.BigInt, migrate.NotNull(), migrate.Check("memory_bytes>=0")).
				Column("storage_bytes", migrate.BigInt, migrate.NotNull(), migrate.Check("storage_bytes>=0")).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("connection_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("connection_generation>0")).
				Column("region", migrate.Text, migrate.NotNull()).
				Column("size", migrate.Text, migrate.NotNull()).
				Column("architecture", migrate.Text, migrate.NotNull()).
				Column("image", migrate.Text, migrate.NotNull()).
				Column("approved_by", migrate.Text).
				Column("approved_at", migrate.Timestamp).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "id").
				Unique("org_id", "cloud_id", "intent_hash").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "pool_id").References("loomplane_server_pools", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "connection_id").References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "operation_id").References("loomplane_infrastructure_operations", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_capacity_reservations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("pool_id", migrate.Text).
				Column("expansion_id", migrate.Text).
				Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
				Column("intent_hash", migrate.Text, migrate.NotNull()).
				Column("servers", migrate.BigInt, migrate.NotNull(), migrate.Check("servers>=0")).
				Column("cpu_millis", migrate.BigInt, migrate.NotNull(), migrate.Check("cpu_millis>=0")).
				Column("memory_bytes", migrate.BigInt, migrate.NotNull(), migrate.Check("memory_bytes>=0")).
				Column("storage_bytes", migrate.BigInt, migrate.NotNull(), migrate.Check("storage_bytes>=0")).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "id").
				Unique("org_id", "cloud_id", "intent_hash").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "pool_id").References("loomplane_server_pools", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "expansion_id").References("loomplane_cloud_expansions", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_expansion_demands").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("expansion_id", migrate.Text, migrate.NotNull()).
				Column("intent_hash", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "expansion_id", "intent_hash").
				Unique("org_id", "cloud_id", "intent_hash").
				ForeignKey("org_id", "cloud_id", "expansion_id").References("loomplane_cloud_expansions", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "environment_id", "plan_id").References("loomplane_plans", "org_id", "project_id", "environment_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_frontend_slots").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("target_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "id").
				Unique("org_id", "cloud_id", "target_id", "project_id", "environment_id", "service").
				ForeignKey("org_id", "cloud_id", "target_id").References("loomplane_cloud_targets", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "environment_id", "plan_id").References("loomplane_plans", "org_id", "project_id", "environment_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_deployment_capacity_waits").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("expansion_id", migrate.Text).
				Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
				Column("intent_hash", migrate.Text, migrate.NotNull()).
				Column("reason", migrate.Text, migrate.NotNull(),
					migrate.Check("reason IN ('hard_limit','expansion_approval','provisioning','stale_inventory','provider_quota','provider_failure','placement_constraint','review_required')")).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "id").
				Unique("org_id", "cloud_id", "intent_hash").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "cloud_id", "expansion_id").References("loomplane_cloud_expansions", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "environment_id", "plan_id").References("loomplane_plans", "org_id", "project_id", "environment_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{
				"loomplane_deployment_capacity_waits",
				"loomplane_frontend_slots",
				"loomplane_cloud_expansion_demands",
				"loomplane_cloud_capacity_reservations",
				"loomplane_cloud_expansions",
				"loomplane_cloud_capacity_inventory",
				"loomplane_cloud_capacity_policies",
			} {
				err := db.NewDropTable(table).IfExists().Exec(ctx)
				if err != nil {
					return err
				}
			}

			for _, index := range []string{
				"loomplane_capacity_plans_scope",
				"loomplane_capacity_environments_scope",
				"loomplane_capacity_servers_scope",
				"loomplane_capacity_operations_scope",
				"loomplane_capacity_connections_scope",
			} {
				err := db.NewDropIndex(index).IfExists().Exec(ctx)
				if err != nil {
					return err
				}
			}
			return nil
		},
	)
}
