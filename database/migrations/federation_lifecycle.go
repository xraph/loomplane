package migrations

// Reference insertion and destination removal serialize through the composite
// foreign key. Ownership records must be explicitly released after revocation
// or cleanup; deleting a display alias cannot cascade away runtime authority.
var federationLifecycleUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_federation_destinations_scope_id ON loomplane_federation_destinations(org_id,project_id,environment_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_destination_refs (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, destination_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('agent','export','placement','network','policy','trust','cleanup')),
 owner_id TEXT NOT NULL CHECK(owner_id<>''),
 PRIMARY KEY(org_id,project_id,environment_id,destination_id,kind,owner_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id)
 REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT
 )`,
}

var federationLifecycleDown = []string{
	`DROP TABLE IF EXISTS loomplane_federation_destination_refs`,
	`DROP INDEX IF EXISTS loomplane_federation_destinations_scope_id`,
}
