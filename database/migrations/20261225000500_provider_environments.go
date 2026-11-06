package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Which provider environment an environment of ours deploys into, per
// provider connection and scope. mode says whether it got the provider's own
// environment (native) or shares one (fallback), and permanent marks a
// fallback the provider can never lift. project_ref and environment_ref are the
// provider's ids, empty for a fallback. notified_at is set once the owner has
// been told about a fallback. Rows go with their environment.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_provider_environments (
	org_id TEXT NOT NULL,
	project_id TEXT NOT NULL,
	environment_id TEXT NOT NULL,
	provider TEXT NOT NULL,
	connection_id TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT '',
	mode TEXT NOT NULL CHECK (mode IN ('native','fallback')),
	permanent BOOLEAN NOT NULL DEFAULT false,
	project_ref TEXT NOT NULL DEFAULT '',
	environment_ref TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	notified_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (org_id, environment_id, provider, connection_id, scope),
	FOREIGN KEY (org_id, project_id, environment_id) REFERENCES loomplane_environments (org_id, project_id, id) ON DELETE CASCADE
)`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_provider_environments_ref ON loomplane_provider_environments (org_id, provider, connection_id, project_ref)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		return db.NewDropTable("loomplane_provider_environments").IfExists().Exec(ctx)
	})
}
