package migrations

// Connecting a cloud by signing in. method says how a connection got its
// credential. An app is the OAuth client an organization registered with a
// cloud; only its client ID is here, the secret is in Vault. A sign-in is
// one pending attempt, and its state is single use. Nothing secret is
// stored in any of these tables.
var cloudSignInUp = []string{
	`ALTER TABLE loomplane_cloud_connections ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT 'token'`,
	`CREATE TABLE IF NOT EXISTS loomplane_oauth_apps (
 org_id TEXT NOT NULL,cloud TEXT NOT NULL,client_id TEXT NOT NULL,updated_by TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,cloud))`,
	`CREATE TABLE IF NOT EXISTS loomplane_oauth_sign_ins (
 state TEXT PRIMARY KEY,org_id TEXT NOT NULL,user_id TEXT NOT NULL,cloud TEXT NOT NULL,
 name TEXT NOT NULL,connection_id TEXT NOT NULL DEFAULT '',expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`,
	`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'deployment'`,
}

var cloudSignInDown = []string{
	`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS kind`,
	`DROP TABLE IF EXISTS loomplane_oauth_sign_ins`,
	`DROP TABLE IF EXISTS loomplane_oauth_apps`,
	`ALTER TABLE loomplane_cloud_connections DROP COLUMN IF EXISTS method`,
}
