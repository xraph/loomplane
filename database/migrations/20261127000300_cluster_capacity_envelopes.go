package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_capacity_envelopes (
 org_id text NOT NULL, cloud_id text NOT NULL, id text NOT NULL, resource_id text,
 provider text NOT NULL, account_id text NOT NULL, pool_id text,
 revision bigint NOT NULL CHECK(revision>0), body jsonb NOT NULL,
 PRIMARY KEY(org_id,id), FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id));
 ALTER TABLE loomplane_cloud_capacity_accounts ADD COLUMN membership jsonb;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_cluster_capacity_envelopes; ALTER TABLE loomplane_cloud_capacity_accounts DROP COLUMN membership;`)
		return err
	})
}
