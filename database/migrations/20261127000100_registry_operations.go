package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, registryOperationsSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_registry_states WHERE status<>'removed') THEN RAISE EXCEPTION 'remove managed registries before downgrade'; END IF; END $$; DROP TABLE loomplane_registry_states;`)
		return err
	})
}

const registryOperationsSchema = `
CREATE TABLE loomplane_registry_states (
 org_id text NOT NULL,
 connection_id text NOT NULL,
 status text NOT NULL,
 zone_id text,
 body jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,connection_id),
 FOREIGN KEY(org_id,zone_id) REFERENCES loomplane_dns_zones(org_id,id) ON DELETE RESTRICT
);
CREATE INDEX loomplane_registry_states_pending ON loomplane_registry_states(updated_at) WHERE status<>'removed';
`
