package migrations

var cloudExpansionAuthorityUp = []string{
	`ALTER TABLE loomplane_cloud_capacity_policies ADD COLUMN authority JSONB NOT NULL DEFAULT 'null'`,
	`ALTER TABLE loomplane_cloud_expansions ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0)`,
	`ALTER TABLE loomplane_cloud_expansions ADD COLUMN approval_principal JSONB NOT NULL DEFAULT 'null'`,
	`CREATE UNIQUE INDEX loomplane_expansion_operation ON loomplane_cloud_expansions(org_id,operation_id) WHERE operation_id IS NOT NULL`,
	`CREATE TABLE loomplane_cloud_expansion_leases (
 org_id TEXT NOT NULL, cloud_id TEXT NOT NULL, pool_id TEXT NOT NULL, owner TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(org_id,cloud_id,pool_id),
 FOREIGN KEY(org_id,cloud_id,pool_id) REFERENCES loomplane_server_pools(org_id,cloud_id,id) ON DELETE CASCADE)`,
}
var cloudExpansionAuthorityDown = []string{
	`DROP TABLE loomplane_cloud_expansion_leases`,
	`DROP INDEX loomplane_expansion_operation`,
	`ALTER TABLE loomplane_cloud_expansions DROP COLUMN approval_principal, DROP COLUMN revision`,
	`ALTER TABLE loomplane_cloud_capacity_policies DROP COLUMN authority`,
}
