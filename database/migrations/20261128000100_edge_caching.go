package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_route_edge (
 org_id text NOT NULL, route_id text NOT NULL, project_id text NOT NULL,
 revision bigint NOT NULL, body jsonb NOT NULL, principal jsonb NOT NULL,
 PRIMARY KEY(org_id,route_id),
 FOREIGN KEY(route_id) REFERENCES loomplane_domain_routes(id) ON DELETE CASCADE);
 CREATE INDEX loomplane_route_edge_pending ON loomplane_route_edge((body->>'status'));
 CREATE TABLE loomplane_edge_invalidations (
 org_id text NOT NULL, route_id text NOT NULL, project_id text NOT NULL, deployment_id text NOT NULL,
 done boolean NOT NULL DEFAULT false, attempts int NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now(), message text NOT NULL DEFAULT '',
 PRIMARY KEY(org_id,route_id,deployment_id),
 FOREIGN KEY(org_id,route_id) REFERENCES loomplane_route_edge(org_id,route_id) ON DELETE CASCADE);
 CREATE INDEX loomplane_edge_invalidations_due ON loomplane_edge_invalidations(next_attempt) WHERE NOT done`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_edge_invalidations; DROP TABLE loomplane_route_edge`)
		return err
	})
}
