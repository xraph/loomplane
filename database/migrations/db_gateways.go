package migrations

// Database gateways: one row per deployed gateway, with the public keys the
// control plane verifies and seals to. Routes are one per opted-in Postgres
// resource and keep their SCRAM salt for good, so a route's verifiers stay
// valid across snapshots. Snapshots hold only the latest version and digest.
// Instances are each running gateway's last report. A gateway owns its
// routes, snapshot and instances, and the environment owns the gateway.
var dbGatewaysUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_environments_scope_id ON loomplane_environments(org_id,project_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_db_gateways (
 org_id TEXT NOT NULL, id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 name TEXT NOT NULL, provider TEXT NOT NULL, region TEXT NOT NULL, suffix TEXT NOT NULL,
 signing_public_key BYTEA NOT NULL, sealing_public_key BYTEA NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,id),
 CONSTRAINT loomplane_db_gateways_environment_name UNIQUE(org_id,environment_id,name),
 CONSTRAINT loomplane_db_gateways_id CHECK(id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
 CONSTRAINT loomplane_db_gateways_signing_key CHECK(octet_length(signing_public_key)=32),
 CONSTRAINT loomplane_db_gateways_sealing_key CHECK(octet_length(sealing_public_key)=32),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE
 )`,
	`CREATE TABLE IF NOT EXISTS loomplane_db_gateway_routes (
 org_id TEXT NOT NULL, gateway_id TEXT NOT NULL, id TEXT NOT NULL, resource TEXT NOT NULL,
 salt BYTEA NOT NULL, state TEXT NOT NULL DEFAULT 'active', updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,gateway_id,id),
 CONSTRAINT loomplane_db_gateway_routes_resource UNIQUE(org_id,gateway_id,resource),
 CONSTRAINT loomplane_db_gateway_routes_id CHECK(id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
 CONSTRAINT loomplane_db_gateway_routes_salt CHECK(octet_length(salt)=16),
 CONSTRAINT loomplane_db_gateway_routes_state CHECK(state IN ('active','paused')),
 FOREIGN KEY(org_id,gateway_id) REFERENCES loomplane_db_gateways(org_id,id) ON DELETE CASCADE
 )`,
	`CREATE TABLE IF NOT EXISTS loomplane_db_gateway_snapshots (
 org_id TEXT NOT NULL, gateway_id TEXT NOT NULL, version BIGINT NOT NULL, digest TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,gateway_id),
 CONSTRAINT loomplane_db_gateway_snapshots_version CHECK(version>0),
 FOREIGN KEY(org_id,gateway_id) REFERENCES loomplane_db_gateways(org_id,id) ON DELETE CASCADE
 )`,
	`CREATE TABLE IF NOT EXISTS loomplane_db_gateway_instances (
 org_id TEXT NOT NULL, gateway_id TEXT NOT NULL, instance_id TEXT NOT NULL,
 snapshot_version BIGINT NOT NULL, draining BOOLEAN NOT NULL DEFAULT false,
 metrics JSONB NOT NULL DEFAULT '[]', last_seen_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(org_id,gateway_id,instance_id),
 CONSTRAINT loomplane_db_gateway_instances_version CHECK(snapshot_version>=0),
 FOREIGN KEY(org_id,gateway_id) REFERENCES loomplane_db_gateways(org_id,id) ON DELETE CASCADE
 )`,
}

// The environment index belongs to the migrations that first needed it, so
// this one leaves it in place.
var dbGatewaysDown = []string{
	`DROP TABLE IF EXISTS loomplane_db_gateway_instances`,
	`DROP TABLE IF EXISTS loomplane_db_gateway_snapshots`,
	`DROP TABLE IF EXISTS loomplane_db_gateway_routes`,
	`DROP TABLE IF EXISTS loomplane_db_gateways`,
}
