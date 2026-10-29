package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_workload_holds (
 org_id text NOT NULL, cloud_id text NOT NULL, resource_id text NOT NULL,
 id text NOT NULL, plan_id text NOT NULL, service text NOT NULL, release_id text NOT NULL,
 body jsonb NOT NULL, released boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,id), UNIQUE(org_id,plan_id,service),
 FOREIGN KEY(org_id,resource_id) REFERENCES loomplane_managed_resources(org_id,id)
);`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_cluster_workload_holds`)
		return err
	})
}
