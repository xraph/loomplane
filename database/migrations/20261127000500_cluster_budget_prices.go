package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER TABLE loomplane_cloud_capacity_reservations DROP CONSTRAINT loomplane_cloud_capacity_reservations_policy_revision_check;
 ALTER TABLE loomplane_cloud_capacity_reservations ADD CONSTRAINT loomplane_cloud_capacity_reservations_policy_revision_check CHECK(policy_revision>0 OR (policy_revision=0 AND body->>'legacy_attempt'='true'));
 CREATE TABLE loomplane_cloud_budget_prices (
 org_id text NOT NULL, cloud_id text NOT NULL, identity text NOT NULL,
 checked_at timestamptz NOT NULL, body jsonb NOT NULL,
 PRIMARY KEY(org_id,cloud_id,identity),
 FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id))`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER TABLE loomplane_cloud_capacity_reservations DROP CONSTRAINT loomplane_cloud_capacity_reservations_policy_revision_check;
 ALTER TABLE loomplane_cloud_capacity_reservations ADD CONSTRAINT loomplane_cloud_capacity_reservations_policy_revision_check CHECK(policy_revision>0);
 DROP TABLE loomplane_cloud_budget_prices`)
		return err
	})
}
