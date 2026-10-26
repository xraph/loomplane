package migrations

func init() {
	App.MustRegister(statements("20261103000300", "cloud_sign_in_people", cloudSignInPeopleUp, cloudSignInPeopleDown))
}

// Who signed in, and what each sign-in needs. signed_in_by is the
// Loomplane user behind an OAuth connection, so the connection can be
// flagged when they leave. A Vercel app has a slug; a pending sign-in
// carries the settings picked before it started and its PKCE verifier; and
// a problem says why an organization's app stopped working. Nothing
// secret is stored here.
var cloudSignInPeopleUp = []string{
	`ALTER TABLE loomplane_cloud_connections ADD COLUMN IF NOT EXISTS signed_in_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_oauth_apps ADD COLUMN IF NOT EXISTS slug TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_oauth_sign_ins ADD COLUMN IF NOT EXISTS settings JSONB NOT NULL DEFAULT '{}'`,
	`ALTER TABLE loomplane_oauth_sign_ins ADD COLUMN IF NOT EXISTS verifier TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS loomplane_oauth_app_problems (
 org_id TEXT NOT NULL,cloud TEXT NOT NULL,message TEXT NOT NULL,at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(org_id,cloud))`,
	`CREATE INDEX IF NOT EXISTS loomplane_oauth_sign_ins_expires_at ON loomplane_oauth_sign_ins(expires_at)`,
}

var cloudSignInPeopleDown = []string{
	`DROP INDEX IF EXISTS loomplane_oauth_sign_ins_expires_at`,
	`DROP TABLE IF EXISTS loomplane_oauth_app_problems`,
	`ALTER TABLE loomplane_oauth_sign_ins DROP COLUMN IF EXISTS verifier`,
	`ALTER TABLE loomplane_oauth_sign_ins DROP COLUMN IF EXISTS settings`,
	`ALTER TABLE loomplane_oauth_apps DROP COLUMN IF EXISTS slug`,
	`ALTER TABLE loomplane_cloud_connections DROP COLUMN IF EXISTS signed_in_by`,
}
