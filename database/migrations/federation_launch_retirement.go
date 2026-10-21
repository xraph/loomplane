package migrations

var federationLaunchRetirementUp = []string{
	`ALTER TABLE loomplane_federation_launch_attempts DROP CONSTRAINT loomplane_federation_launch_attempts_state_check, DROP CONSTRAINT loomplane_federation_launch_attempts_check`,
	`ALTER TABLE loomplane_federation_launch_attempts ADD CONSTRAINT loomplane_federation_launch_attempts_state_check CHECK(state IN ('pending','delivered','retiring','retired')), ADD CONSTRAINT loomplane_federation_launch_attempts_check CHECK((state='pending' AND config_map_uid='') OR (state='delivered' AND config_map_uid<>'') OR state IN ('retiring','retired'))`,
	`DROP INDEX loomplane_federation_launch_one_pending`,
	`CREATE UNIQUE INDEX loomplane_federation_launch_one_pending ON loomplane_federation_launch_attempts(org_id,destination_id) WHERE state IN ('pending','retiring')`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_launch_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='cleanup' AND owner_id='launch:'||OLD.id;
 ELSIF NEW.state IN ('pending','retiring') THEN
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'cleanup','launch:'||NEW.id) ON CONFLICT DO NOTHING;
 ELSE
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='cleanup' AND owner_id='launch:'||NEW.id;
 END IF;
 RETURN NULL;
 END $$`,
}

// Downgrade refuses retained retirement history instead of deleting evidence.
var federationLaunchRetirementDown = []string{
	`ALTER TABLE loomplane_federation_launch_attempts DROP CONSTRAINT loomplane_federation_launch_attempts_state_check, DROP CONSTRAINT loomplane_federation_launch_attempts_check`,
	`ALTER TABLE loomplane_federation_launch_attempts ADD CONSTRAINT loomplane_federation_launch_attempts_state_check CHECK(state IN ('pending','delivered')), ADD CONSTRAINT loomplane_federation_launch_attempts_check CHECK((state='pending' AND config_map_uid='') OR (state='delivered' AND config_map_uid<>''))`,
	`DROP INDEX loomplane_federation_launch_one_pending`,
	`CREATE UNIQUE INDEX loomplane_federation_launch_one_pending ON loomplane_federation_launch_attempts(org_id,destination_id) WHERE state='pending'`,
}
