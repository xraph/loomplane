package migrations

// Tags let a manifest select a target by what it is. A Cloud's default
// placement is one provider and account, kept on the Cloud row itself, so a
// Cloud without targets cannot hold a second provider. Its connection is
// retained like a target's.
var cloudTagsUp = []string{
	`CREATE OR REPLACE FUNCTION loomplane_cloud_tags_valid(tags TEXT[]) RETURNS BOOLEAN LANGUAGE SQL IMMUTABLE AS $$
 SELECT cardinality(tags)<=16 AND NOT EXISTS(SELECT 1 FROM unnest(tags) t WHERE t IS NULL OR length(t)>32 OR t !~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$')
 AND cardinality(tags)=(SELECT count(DISTINCT t) FROM unnest(tags) t) $$`,
	`ALTER TABLE loomplane_cloud_targets ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}'`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_target_tags') THEN
 ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_cloud_target_tags CHECK(loomplane_cloud_tags_valid(tags));
 END IF; END $$`,
	`ALTER TABLE loomplane_cloud_profiles ADD COLUMN IF NOT EXISTS default_provider TEXT`,
	`ALTER TABLE loomplane_cloud_profiles ADD COLUMN IF NOT EXISTS default_connection_id TEXT`,
	`ALTER TABLE loomplane_cloud_profiles ADD COLUMN IF NOT EXISTS default_region TEXT NOT NULL DEFAULT ''`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_default_placement') THEN
 ALTER TABLE loomplane_cloud_profiles ADD CONSTRAINT loomplane_cloud_default_placement CHECK(
 (default_provider IS NULL AND default_connection_id IS NULL AND default_region='')
 OR (default_provider IS NOT NULL AND default_connection_id IS NOT NULL AND default_provider<>'' AND default_connection_id<>''));
 END IF; END $$`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_cloud_default_connection') THEN
 ALTER TABLE loomplane_cloud_profiles ADD CONSTRAINT loomplane_cloud_default_connection FOREIGN KEY(org_id,default_connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT;
 END IF; END $$`,
	`CREATE INDEX IF NOT EXISTS loomplane_cloud_profiles_default_connection ON loomplane_cloud_profiles(org_id,default_connection_id) WHERE default_connection_id IS NOT NULL`,
}

var cloudTagsDown = []string{
	`DROP INDEX IF EXISTS loomplane_cloud_profiles_default_connection`,
	`ALTER TABLE loomplane_cloud_profiles DROP CONSTRAINT IF EXISTS loomplane_cloud_default_connection`,
	`ALTER TABLE loomplane_cloud_profiles DROP CONSTRAINT IF EXISTS loomplane_cloud_default_placement`,
	`ALTER TABLE loomplane_cloud_profiles DROP COLUMN IF EXISTS default_region`,
	`ALTER TABLE loomplane_cloud_profiles DROP COLUMN IF EXISTS default_connection_id`,
	`ALTER TABLE loomplane_cloud_profiles DROP COLUMN IF EXISTS default_provider`,
	`ALTER TABLE loomplane_cloud_targets DROP CONSTRAINT IF EXISTS loomplane_cloud_target_tags`,
	`ALTER TABLE loomplane_cloud_targets DROP COLUMN IF EXISTS tags`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_tags_valid(TEXT[])`,
}
