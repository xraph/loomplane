package migrations

var cloudInfrastructureReviewUp = []string{
	`ALTER TABLE loomplane_cloud_connections ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1 CHECK(generation>0)`,
	`CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status NOT IN ('ready','deleted','disconnected')) THEN RAISE EXCEPTION 'server is not available' USING ERRCODE='23503'; END IF;
 RETURN NEW; END $$`,
}
var cloudInfrastructureReviewDown = []string{
	`CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status<>'ready') THEN RAISE EXCEPTION 'server is retiring' USING ERRCODE='23503'; END IF;
 RETURN NEW; END $$`,
	`ALTER TABLE loomplane_cloud_connections DROP COLUMN IF EXISTS generation`,
}
