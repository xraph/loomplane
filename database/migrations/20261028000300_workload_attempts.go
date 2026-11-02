package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_workload_attempts").
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("active", migrate.Boolean, migrate.NotNull(), migrate.Default(true)).
				Column("settled", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("legacy_unfenced", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				PrimaryKey("org_id", "environment_id", "service").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_workload_attempts(org_id,project_id,environment_id,service,plan_id,deployment_id,generation,legacy_unfenced)
SELECT DISTINCT ON(d.org_id,d.environment_id,a->>'name') d.org_id,d.project_id,d.environment_id,a->>'name',d.plan_id,d.id,0,
	NOT EXISTS(SELECT 1 FROM loomplane_pool_execution x WHERE x.org_id=d.org_id AND x.plan_id=d.plan_id AND x.service=a->>'name')
FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id
CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a
WHERE d.status IN ('running','failed') AND a->>'provider'='server' AND a->>'op'<>'keep'
	AND NOT (d.status='failed' AND d.finished_at IS NOT NULL AND EXISTS(
		SELECT 1 FROM jsonb_array_elements(d.steps) step WHERE step->>'action_id'=a->>'id'
			AND step->>'status'='succeeded' AND step->>'finished_at' IS NOT NULL
			AND coalesce(step->'replaced','null'::jsonb)='null'::jsonb AND coalesce(step->'warnings','[]'::jsonb)='[]'::jsonb)
	AND EXISTS(
		SELECT 1 FROM loomplane_bindings b JOIN loomplane_deployments successor
			ON successor.org_id=b.org_id AND successor.project_id=b.project_id AND successor.environment_id=b.environment_id AND successor.plan_id=b.plan_id
		WHERE b.org_id=d.org_id AND b.project_id=d.project_id AND b.environment_id=d.environment_id
			AND b.subject='service' AND b.name=a->>'name' AND b.provider='server' AND b.external_id<>'' AND b.replaced='[]'::jsonb
			AND successor.status='succeeded' AND successor.started_at>d.finished_at))
ORDER BY d.org_id,d.environment_id,a->>'name',d.started_at DESC,d.id DESC`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_workload_attempts: %w", err)
			}
			return nil
		},
		nil,
	)
}
