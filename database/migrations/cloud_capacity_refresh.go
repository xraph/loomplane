package migrations

var cloudCapacityRefreshUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_capacity_refreshes (
 org_id TEXT NOT NULL, cloud_id TEXT NOT NULL,
 issued_generation BIGINT NOT NULL CHECK(issued_generation>0),
 saved_generation BIGINT NOT NULL DEFAULT 0 CHECK(saved_generation>=0 AND saved_generation<=issued_generation),
 PRIMARY KEY(org_id,cloud_id),
 FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE CASCADE)`,
}
var cloudCapacityRefreshDown = []string{`DROP TABLE IF EXISTS loomplane_cloud_capacity_refreshes`}
