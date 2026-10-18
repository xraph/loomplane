package migrations

// Composite keys keep every Cloud, target and assignment within its project.
// A target retains its account through a restrictive foreign key, independently
// of plans and deployment bindings.
var cloudProfilesUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_projects_scope_id ON loomplane_projects(org_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_profiles (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL,
 name TEXT NOT NULL CHECK(length(trim(name)) BETWEEN 1 AND 64),
 revision BIGINT NOT NULL CHECK(revision>0), created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(org_id,project_id,id), UNIQUE(org_id,project_id,name),
 FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE CASCADE
 )`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_targets (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, cloud_id TEXT NOT NULL,
 name TEXT NOT NULL CHECK(length(name)<=48 AND name ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'),
 display_name TEXT NOT NULL CHECK(length(trim(display_name)) BETWEEN 1 AND 64),
 provider TEXT NOT NULL CHECK(provider<>''), connection_id TEXT NOT NULL, region TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL CHECK(revision>0), created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(org_id,project_id,cloud_id,name), UNIQUE(org_id,project_id,cloud_id,id),
 FOREIGN KEY(org_id,project_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_cloud_targets_connection ON loomplane_cloud_targets(org_id,connection_id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_assignments (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, cloud_id TEXT NOT NULL,
 revision BIGINT NOT NULL CHECK(revision>0), PRIMARY KEY(org_id,project_id,environment_id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,project_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,project_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_cloud_assignments_cloud ON loomplane_cloud_assignments(org_id,project_id,cloud_id)`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_revision_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'cloud revision must increment by one' USING ERRCODE='23514'; END IF;
 IF NEW.org_id<>OLD.org_id OR NEW.project_id<>OLD.project_id THEN RAISE EXCEPTION 'cloud scope is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_profile_revision BEFORE UPDATE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_target_revision BEFORE UPDATE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_assignment_revision BEFORE UPDATE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_target_identity_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.id<>OLD.id OR NEW.cloud_id<>OLD.cloud_id OR NEW.name<>OLD.name THEN RAISE EXCEPTION 'target identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_target_identity BEFORE UPDATE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_target_identity_trigger()`,
	// FOR UPDATE conflicts with the assignment's parent FK lock. Deletion and
	// assignment insertion cannot both succeed while observing no assignment.
	`CREATE OR REPLACE FUNCTION loomplane_cloud_target_delete_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND id=OLD.cloud_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_assignments WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND cloud_id=OLD.cloud_id)
 THEN RAISE EXCEPTION 'target belongs to an assigned cloud' USING ERRCODE='23503'; END IF;
 RETURN OLD;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_target_delete BEFORE DELETE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_target_delete_trigger()`,
}

var cloudProfilesDown = []string{
	`DROP TABLE IF EXISTS loomplane_cloud_assignments`,
	`DROP TABLE IF EXISTS loomplane_cloud_targets`,
	`DROP TABLE IF EXISTS loomplane_cloud_profiles`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_target_delete_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_target_identity_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_revision_trigger()`,
	`DROP INDEX IF EXISTS loomplane_projects_scope_id`,
}
