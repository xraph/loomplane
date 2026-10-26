package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/grove/migrate"
)

// retainedCompensationSQL is the retained compensation check this migration
// installs. cloud_compensation_progress builds its own version from it.
const retainedCompensationSQL = `CREATE OR REPLACE FUNCTION loomplane_retained_compensation(p loomplane_plans,a JSONB) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
	SELECT coalesce((a->'compensation'->>'retained')::boolean,false) AND loomplane_accepted_pool_sync(p) AND EXISTS(
		SELECT 1 FROM loomplane_cloud_syncs c
		JOIN loomplane_cloud_syncs o ON o.org_id=c.org_id AND o.id=c.body->>'reverts'
		JOIN loomplane_plans op ON op.org_id=o.org_id AND op.id=a->'compensation'->>'plan_id' AND op.result->>'cloud_sync_id'=o.id AND op.project_id=p.project_id AND op.environment_id=p.environment_id
		JOIN loomplane_bindings b ON b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND b.subject=a->>'subject' AND b.name=a->>'name'
		WHERE c.org_id=p.org_id AND c.id=p.result->>'cloud_sync_id' AND o.status='reverting'
			AND EXISTS(SELECT 1 FROM jsonb_array_elements(op.result->'actions') oa
				WHERE oa->>'id'=a->'compensation'->>'action_id' AND oa->>'id'=a->>'id' AND oa->>'name'=a->>'name' AND oa->>'subject'=a->>'subject' AND oa->>'provider'='server'
					AND coalesce((a->'compensation'->>'server_attempt')::boolean,false) AND a->'compensation'->'source'->>'provider'='server'
					AND (oa->>'op'<>'keep' OR coalesce((oa->>'adopt_cloud_placement')::boolean,false)))
			AND b.plan_id=a->'compensation'->'candidate'->>'plan_id'
			AND a->'compensation'->'source'->>'id'=b.id AND a->'compensation'->'source'->>'org_id'=b.org_id AND a->'compensation'->'source'->>'project_id'=b.project_id
			AND a->'compensation'->'source'->>'environment_id'=b.environment_id AND a->'compensation'->'source'->>'subject'=b.subject AND a->'compensation'->'source'->>'name'=b.name
			AND loomplane_compensation_identity(to_jsonb(b))=loomplane_compensation_identity(a->'compensation'->'candidate')
			AND EXISTS(SELECT 1 FROM jsonb_array_elements(o.snapshot->'environments') e CROSS JOIN LATERAL jsonb_array_elements(e->'bindings') old
				WHERE e->>'environment_id'=p.environment_id AND old->>'plan_id'=a->'compensation'->'source'->>'plan_id'
					AND loomplane_compensation_identity(old)=loomplane_compensation_identity(a->'compensation'->'source'))
			AND (loomplane_compensation_identity(to_jsonb(b))=loomplane_compensation_identity(a->'compensation'->'source')
				OR EXISTS(SELECT 1 FROM jsonb_array_elements(b.replaced) old WHERE loomplane_compensation_identity(old)=loomplane_compensation_identity(a->'compensation'->'source')))
			AND (coalesce(a->'compensation'->'source'->'config'->'cloud_placement'->>'pool_id','')=''
				OR EXISTS(SELECT 1 FROM loomplane_capacity_reservations r
					WHERE r.org_id=p.org_id AND r.project_id=p.project_id AND r.environment_id=p.environment_id AND r.service=a->>'name'
						AND r.plan_id=a->'compensation'->'source'->>'plan_id' AND r.binding_id=b.id
						AND r.server_id=a->'compensation'->'source'->'config'->'cloud_placement'->>'server_id'
						AND r.pool_id=a->'compensation'->'source'->'config'->'cloud_placement'->>'pool_id'
						AND r.connection_id=a->'compensation'->'source'->>'connection_id'
						AND r.cpu_millis=(r.body->>'cpu_millis')::bigint AND r.memory_bytes=(r.body->>'memory_bytes')::bigint
						AND r.body=a->'compensation'->'source'->'config'->'cloud_placement'))
	) $$`

// compensatedReservePoolPlanSQL is the pool admission function with retained
// compensation let through. cloud_capacity_intents builds on it and puts it
// back on the way down.
var compensatedReservePoolPlanSQL = mustReplaceSQL(reservePoolPlanSQL, "pin:=a->'service'->'cloud_placement';", `IF coalesce((a->'compensation'->>'retained')::boolean,false) THEN
			IF NOT loomplane_retained_compensation(NEW,a) THEN RAISE EXCEPTION 'retained compensation identity changed' USING ERRCODE='23514'; END IF;
			CONTINUE;
		END IF;
		pin:=a->'service'->'cloud_placement';`)

// mustReplaceSQL replaces the first old in sql with with. A migration that
// derives its SQL from another's panics when the text it edits is gone,
// rather than installing the function unchanged.
func mustReplaceSQL(sql, old, with string) string {
	if !strings.Contains(sql, old) {
		panic(fmt.Sprintf("migrations: %.60q no longer contains %q", sql, old))
	}
	return strings.Replace(sql, old, with, 1)
}

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_route_handoffs").
				AddColumn("compensation_plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_unstarted_cloud_compensation(org TEXT, sync TEXT, env TEXT, service_name TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
	SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs o CROSS JOIN LATERAL jsonb_array_elements(o.snapshot->'environments') e
			WHERE o.org_id=org AND o.id=sync AND e->>'environment_id'=env AND coalesce(e->'accepted_plan','null'::jsonb)='null'::jsonb)
		AND NOT EXISTS(SELECT 1 FROM loomplane_plans p
			WHERE p.org_id=org AND p.environment_id=env AND (p.id=sync||'.'||env OR p.result->>'cloud_sync_id'=sync))
		AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d
			WHERE d.org_id=org AND d.environment_id=env AND d.plan_id=sync||'.'||env)
		AND NOT EXISTS(SELECT 1 FROM loomplane_pool_execution x
			WHERE x.org_id=org AND x.plan_id=sync||'.'||env
				AND (x.started OR x.active OR x.generation<>0 OR x.phase<>'reserved' OR x.checkpoint IS NOT NULL OR x.storage_retained))
		AND NOT EXISTS(SELECT 1 FROM loomplane_workload_attempts w
			WHERE w.org_id=org AND w.environment_id=env AND (service_name='' OR w.service=service_name)
				AND (w.active OR NOT w.settled OR w.legacy_unfenced OR w.plan_id=sync||'.'||env)) $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_unstarted_cloud_compensation: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_unstarted_compensation_origin(org TEXT, sync TEXT, env TEXT, service_name TEXT) RETURNS TEXT LANGUAGE SQL STABLE AS $$
	SELECT coalesce((SELECT o.id FROM loomplane_cloud_syncs c
		JOIN loomplane_cloud_syncs o ON o.org_id=c.org_id AND o.id=c.body->>'reverts'
		CROSS JOIN LATERAL jsonb_array_elements(o.snapshot->'environments') e
		WHERE c.org_id=org AND c.id=sync AND e->>'environment_id'=env AND coalesce(e->'accepted_plan','null'::jsonb)='null'::jsonb
			AND EXISTS(SELECT 1 FROM jsonb_array_elements(e->'plan'->'actions') a
				WHERE a->>'name'=service_name AND a->>'provider'='server' AND a->>'op'<>'keep' AND coalesce(a->'service'->'cloud_placement'->>'pool_id','')<>'')), '') $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_unstarted_compensation_origin: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_guard_unstarted_compensation() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	a JSONB;
	original TEXT;
BEGIN
	IF NEW.status<>'approved' THEN RETURN NEW; END IF;

	FOR a IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) LOOP
		IF a->>'op'='keep' AND coalesce((a->>'adopt_cloud_placement')::boolean,false) AND coalesce(a->'compensation','null'::jsonb)='null'::jsonb THEN
			original:=loomplane_unstarted_compensation_origin(NEW.org_id,NEW.result->>'cloud_sync_id',NEW.environment_id,a->>'name');
			IF original<>'' AND NOT loomplane_unstarted_cloud_compensation(NEW.org_id,original,NEW.environment_id,a->>'name') THEN
				RAISE EXCEPTION 'legacy compensation has an unresolved original attempt' USING ERRCODE='23514';
			END IF;
		END IF;
	END LOOP;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_guard_unstarted_compensation: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE TRIGGER loomplane_unstarted_compensation_admission
BEFORE INSERT OR UPDATE ON loomplane_plans
FOR EACH ROW EXECUTE FUNCTION loomplane_guard_unstarted_compensation()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_unstarted_compensation_admission: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_compensation_identity(b JSONB) RETURNS JSONB LANGUAGE SQL IMMUTABLE AS $$
	SELECT jsonb_build_array(b->>'id',b->>'org_id',b->>'project_id',b->>'environment_id',b->>'subject',b->>'name',b->>'provider',b->>'region',b->>'engine',
		b->>'connection_id',b->>'external_id',coalesce(b->>'physical_id',''),b->>'revision',b->>'commit_sha',b->>'url',b->'config') $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_compensation_identity: %w", err)
			}

			_, err = db.Exec(ctx, retainedCompensationSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_retained_compensation: %w", err)
			}

			_, err = db.Exec(ctx, compensatedReservePoolPlanSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_reserve_pool_plan: %w", err)
			}
			return nil
		},
		nil,
	)
}
