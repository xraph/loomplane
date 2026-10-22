package migrations

var poolRecoveryUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_pool_execution(org_id TEXT NOT NULL,plan_id TEXT NOT NULL,service TEXT NOT NULL,phase TEXT NOT NULL DEFAULT 'reserved',started BOOLEAN NOT NULL DEFAULT false,active BOOLEAN NOT NULL DEFAULT false,revision BIGINT NOT NULL DEFAULT 1,checkpoint BYTEA,storage_retained BOOLEAN NOT NULL DEFAULT false,PRIMARY KEY(org_id,plan_id,service))`,
	`INSERT INTO loomplane_pool_execution(org_id,plan_id,service,started,active) SELECT r.org_id,r.plan_id,r.service,EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=r.plan_id),EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=r.plan_id AND d.status='running') FROM loomplane_capacity_reservations r ON CONFLICT DO NOTHING`,
	`CREATE OR REPLACE FUNCTION loomplane_pool_execution_create() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN INSERT INTO loomplane_pool_execution(org_id,plan_id,service) VALUES(NEW.org_id,NEW.plan_id,NEW.service) ON CONFLICT DO NOTHING; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_pool_execution_create AFTER INSERT ON loomplane_capacity_reservations FOR EACH ROW EXECUTE FUNCTION loomplane_pool_execution_create()`,
}
var poolRecoveryDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_pool_execution_create ON loomplane_capacity_reservations`,
	`DROP FUNCTION IF EXISTS loomplane_pool_execution_create()`,
	`DROP TABLE IF EXISTS loomplane_pool_execution`,
}
