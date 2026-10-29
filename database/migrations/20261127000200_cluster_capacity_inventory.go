package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_capacity_inventory (
 org_id text NOT NULL, cloud_id text NOT NULL, resource_id text NOT NULL,
 issued_revision bigint NOT NULL DEFAULT 0, saved_revision bigint NOT NULL DEFAULT 0,
 checked_at timestamptz, complete boolean NOT NULL DEFAULT false, body jsonb,
 PRIMARY KEY(org_id,resource_id),
 FOREIGN KEY(org_id,resource_id) REFERENCES loomplane_managed_resources(org_id,id),
 CHECK(issued_revision>=saved_revision AND saved_revision>=0)
 );`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_cluster_capacity_inventory`)
		return err
	})
}
