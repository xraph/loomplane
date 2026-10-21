package migrations

var federationDeliveryUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_deliveries (
 org_id TEXT NOT NULL, agent_id TEXT NOT NULL, state JSONB NOT NULL,
 PRIMARY KEY(org_id,agent_id),
 FOREIGN KEY(org_id,agent_id) REFERENCES loomplane_federation_observers(org_id,agent_id) ON DELETE CASCADE)`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_delivery_retention() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF (OLD.state->>'lease_until') IS NULL OR (OLD.state->>'lease_until')::timestamptz>clock_timestamp() THEN
 RAISE EXCEPTION 'federation delivery lease is still retained' USING ERRCODE='23503';
 END IF;
 RETURN OLD;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_delivery_retention BEFORE DELETE ON loomplane_federation_deliveries
 FOR EACH ROW EXECUTE FUNCTION loomplane_federation_delivery_retention()`,
}
var federationDeliveryDown = []string{`DROP TABLE IF EXISTS loomplane_federation_deliveries`, `DROP FUNCTION IF EXISTS loomplane_federation_delivery_retention()`}
