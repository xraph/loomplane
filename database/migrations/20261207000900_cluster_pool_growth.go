package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_pool_growth(org_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,pool_id text NOT NULL,operation_id text NOT NULL,issued_at timestamptz NOT NULL,next_growth_at timestamptz NOT NULL CHECK(next_growth_at>issued_at),PRIMARY KEY(org_id,cloud_id,resource_id,pool_id));
CREATE FUNCTION loomplane_claim_pool_growth(p_org text,p_cloud text,p_resource text,p_pool text,p_op text,p_seconds bigint)
RETURNS timestamptz LANGUAGE plpgsql AS $$
DECLARE issued timestamptz:=clock_timestamp(); deadline timestamptz;
BEGIN
 IF p_seconds NOT BETWEEN 1 AND 86400 THEN RAISE EXCEPTION 'invalid scale-up cooldown'; END IF;
 INSERT INTO loomplane_cluster_pool_growth(org_id,cloud_id,resource_id,pool_id,operation_id,issued_at,next_growth_at)
 VALUES(p_org,p_cloud,p_resource,p_pool,p_op,issued,issued+make_interval(secs=>p_seconds))
 ON CONFLICT(org_id,cloud_id,resource_id,pool_id) DO UPDATE SET operation_id=excluded.operation_id,issued_at=excluded.issued_at,next_growth_at=excluded.next_growth_at
 WHERE loomplane_cluster_pool_growth.next_growth_at<=excluded.issued_at;
 IF FOUND THEN RETURN NULL; END IF;
 SELECT next_growth_at INTO STRICT deadline FROM loomplane_cluster_pool_growth WHERE org_id=p_org AND cloud_id=p_cloud AND resource_id=p_resource AND pool_id=p_pool;
 RETURN deadline;
END $$;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cluster_pool_growth WHERE next_growth_at>clock_timestamp()) THEN RAISE EXCEPTION 'a cluster scale-up cooldown is active'; END IF; END $$;DROP FUNCTION loomplane_claim_pool_growth(text,text,text,text,text,bigint); DROP TABLE loomplane_cluster_pool_growth`)
		return err
	})
}
