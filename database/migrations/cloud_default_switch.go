package migrations

// A reviewed default switch is shared, like a target switch: one may run per
// Cloud, and no environment joins or leaves that Cloud until it settles.
var cloudDefaultSwitchUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_sync_active_default ON loomplane_cloud_syncs(org_id,cloud_id) WHERE body->>'default'='true' AND status NOT IN ('succeeded','reverted','reverting')`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_assignment_grant_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.cloud_id IS NULL THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.cloud_id IS NOT DISTINCT FROM OLD.cloud_id THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND cloud_id=NEW.cloud_id AND NOT revoked FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Cloud is not granted to project' USING ERRCODE='23503'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND (target_id<>'' OR body->>'default'='true') AND status NOT IN ('succeeded','reverted')) THEN
 RAISE EXCEPTION 'Cloud target has unfinished migration work' USING ERRCODE='55P03'; END IF;
 RETURN NEW; END $$`,
}

// Restores the shared_clouds guard, which only watched target switches.
var cloudDefaultSwitchDown = []string{
	`DROP INDEX IF EXISTS loomplane_cloud_sync_active_default`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_assignment_grant_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.cloud_id IS NULL THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.cloud_id IS NOT DISTINCT FROM OLD.cloud_id THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND cloud_id=NEW.cloud_id AND NOT revoked FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Cloud is not granted to project' USING ERRCODE='23503'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND target_id<>'' AND status NOT IN ('succeeded','reverted')) THEN
 RAISE EXCEPTION 'Cloud target has unfinished migration work' USING ERRCODE='55P03'; END IF;
 RETURN NEW; END $$`,
}
