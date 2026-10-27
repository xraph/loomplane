package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A per-server GPU budget held in the database. A reservation copies its
// GPU kind and count from the reviewed pin, and a trigger checks them
// against the member budget, the pool's kind and the host's runtime.
//
// Inventory gpus stays nullable: a count Loomplane couldn't read is
// unknown, and rows written before this migration have no count at all.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_server_pool_members").
				AddColumn("gpus", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("gpus>=0")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			// GPU pools saved before this column kept each member's budget in
			// the pool body.
			_, err = db.Exec(ctx, `UPDATE loomplane_server_pool_members m SET gpus=(x->>'gpus')::bigint FROM loomplane_server_pools p CROSS JOIN LATERAL jsonb_array_elements(coalesce(p.body->'members','[]')) x
 WHERE p.id=m.pool_id AND x->>'server_id'=m.server_id AND coalesce((x->>'gpus')::bigint,0)>0 AND m.gpus=0`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_server_pool_members.gpus: %w", err)
			}

			for _, table := range []string{"loomplane_capacity_reservations", "loomplane_cloud_expansions"} {
				err = db.NewAlterTable(table).
					AddColumn("gpu_kind", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
					AddColumn("gpus", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("gpus>=0")).IfNotExists().
					Exec(ctx)
				if err != nil {
					return err
				}
				_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='`+table+`_gpu_kind') THEN
		ALTER TABLE `+table+` ADD CONSTRAINT `+table+`_gpu_kind CHECK((gpus=0)=(gpu_kind=''));
	END IF;
END $$`)
				if err != nil {
					return fmt.Errorf("add constraint %s_gpu_kind: %w", table, err)
				}
			}

			err = db.NewAlterTable("loomplane_cloud_capacity_inventory").
				AddColumn("gpu_kind", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("gpus", migrate.BigInt, migrate.Check("gpus>=0")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_capacity_inventory_gpu_kind') THEN
		ALTER TABLE loomplane_cloud_capacity_inventory ADD CONSTRAINT loomplane_cloud_capacity_inventory_gpu_kind CHECK((coalesce(gpus,0)=0)=(gpu_kind=''));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add constraint loomplane_cloud_capacity_inventory_gpu_kind: %w", err)
			}

			// Counted like loomplane_server_capacity: each retained allocation
			// once, and an in-place rollover keeps its largest demand, since the
			// old container stops before the new one takes its devices.
			// skip_plan and skip_service leave out the row a trigger is about to
			// write.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_server_gpus(host_id TEXT, extra_environment TEXT DEFAULT '', extra JSONB DEFAULT '[]', skip_plan TEXT DEFAULT '', skip_service TEXT DEFAULT '') RETURNS BIGINT LANGUAGE SQL STABLE AS $$
 SELECT coalesce(sum(g),0)::bigint FROM (
 SELECT max(gpus) g FROM (
 SELECT org_id,project_id,environment_id,service,coalesce(body->>'allocation','') allocation,gpus FROM loomplane_capacity_reservations WHERE server_id=host_id AND gpus>0 AND NOT (plan_id=skip_plan AND service=skip_service)
 UNION ALL SELECT coalesce(nullif(p->>'org_id',''),e.org_id),coalesce(nullif(p->>'project_id',''),e.project_id),coalesce(nullif(p->>'environment_id',''),extra_environment),p->>'service',coalesce(p->>'allocation',''),coalesce((p->>'gpus')::bigint,0) FROM jsonb_array_elements(extra) p LEFT JOIN loomplane_environments e ON e.id=coalesce(nullif(p->>'environment_id',''),extra_environment) WHERE p->>'server_id'=host_id AND coalesce((p->>'gpus')::bigint,0)>0
 ) demands GROUP BY org_id,project_id,environment_id,service,allocation) allocated $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_server_gpus: %w", err)
			}

			// The member-row lock below doesn't serialize two pools that share a
			// server: each pool has its own member row. Safety rests on
			// loomplane_reserve_pool_plan, which locks the server rows FOR UPDATE (and
			// takes the host-admission lock) before it inserts any reservation, so two
			// plans reserving GPUs on one server can't both pass the budget check.
			// Kept as a Go comment so the migration's SQL text doesn't change.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_reservation_gpus() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE member loomplane_server_pool_members%ROWTYPE; kind TEXT; used BIGINT;
 BEGIN
 NEW.gpu_kind:=coalesce(NEW.body->>'gpu_kind','');
 NEW.gpus:=coalesce((NEW.body->>'gpus')::bigint,0);
 -- Recording the binding after a deploy rewrites the row without moving it.
 -- That never rechecks GPUs, so a toolkit lost later can't fail the deploy.
 IF TG_OP='UPDATE' AND NEW.server_id=OLD.server_id AND NEW.gpus=OLD.gpus AND NEW.gpu_kind=OLD.gpu_kind THEN RETURN NEW; END IF;
 SELECT coalesce(p.body->>'accelerator_kind','') INTO kind FROM loomplane_server_pools p WHERE p.id=NEW.pool_id;
 IF NEW.gpus=0 THEN
 IF coalesce(kind,'')<>'' THEN RAISE EXCEPTION 'a GPU pool takes only GPU workloads' USING ERRCODE='23514'; END IF;
 RETURN NEW; END IF;
 IF coalesce(kind,'')='' THEN RAISE EXCEPTION 'a CPU pool takes no GPU workloads' USING ERRCODE='23514'; END IF;
 IF kind<>NEW.gpu_kind THEN RAISE EXCEPTION 'this pool has % GPUs, not %', kind, NEW.gpu_kind USING ERRCODE='23514'; END IF;
 SELECT * INTO member FROM loomplane_server_pool_members WHERE org_id=NEW.org_id AND pool_id=NEW.pool_id AND server_id=NEW.server_id FOR UPDATE;
 IF member.server_id IS NULL THEN RAISE EXCEPTION 'server is not in the pool' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM loomplane_cloud_servers s WHERE s.org_id=NEW.org_id AND s.id=NEW.server_id AND s.body->'health'->>'gpu_runtime'='ready') THEN RAISE EXCEPTION 'the server''s GPU runtime isn''t ready' USING ERRCODE='23514'; END IF;
 used:=loomplane_server_gpus(NEW.server_id,'',jsonb_build_array(jsonb_build_object('org_id',NEW.org_id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'service',NEW.service,'server_id',NEW.server_id,'allocation',coalesce(NEW.body->>'allocation',''),'gpus',NEW.gpus)),
 CASE WHEN TG_OP='UPDATE' THEN OLD.plan_id ELSE NEW.plan_id END, CASE WHEN TG_OP='UPDATE' THEN OLD.service ELSE NEW.service END);
 IF used>member.gpus THEN RAISE EXCEPTION 'insufficient GPUs: % of % on this server', used, member.gpus USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_reservation_gpus: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_reservation_gpus BEFORE INSERT OR UPDATE ON loomplane_capacity_reservations FOR EACH ROW EXECUTE FUNCTION loomplane_reservation_gpus()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_reservation_gpus: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE gpus>0) THEN
		RAISE EXCEPTION 'remove GPU reservations before downgrading';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check for GPU reservations: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_reservation_gpus ON loomplane_capacity_reservations`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_reservation_gpus: %w", err)
			}
			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_reservation_gpus()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_reservation_gpus: %w", err)
			}
			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_server_gpus(TEXT,TEXT,JSONB,TEXT,TEXT)`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_server_gpus: %w", err)
			}

			for _, table := range []string{"loomplane_cloud_capacity_inventory", "loomplane_cloud_expansions", "loomplane_capacity_reservations"} {
				err = db.NewAlterTable(table).
					DropConstraint(table + "_gpu_kind").IfExists().
					DropColumn("gpus").IfExists().
					DropColumn("gpu_kind").IfExists().
					Exec(ctx)
				if err != nil {
					return err
				}
			}

			return db.NewAlterTable("loomplane_server_pool_members").
				DropColumn("gpus").IfExists().
				Exec(ctx)
		},
	)
}
