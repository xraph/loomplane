package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What keeps a platform Cloud service billed, and billed once, across a
// redeploy or a move.
//
// loomplane_platform_placements.replaces names the running placement a new
// one is placed to take over from. The old placement stays running, and
// billed, until the new one runs; a new one that never does is released and
// the old one keeps running. A resource has at most one live placement that
// replaces nothing and at most one live placement that replaces another.
//
// loomplane_platform_egress_counters keeps the last value read from each VM's
// egress counter, by organization, instance and counter ID, so a VM that keeps
// sending while its service is placed again is never read from zero under
// the new placement. A counter ID changes whenever a VM's meter is recreated,
// and two VMs of one instance on two hosts have counters of their own. It
// replaces loomplane_platform_egress, which kept the value by placement.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_platform_placements").
				AddColumn("replaces", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			for _, stmt := range []string{
				`DROP INDEX IF EXISTS loomplane_platform_placements_resource`,
				`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_platform_placements_resource ON loomplane_platform_placements (org_id,environment_id,resource) WHERE state<>'released' AND replaces=''`,
				`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_platform_placements_replacing ON loomplane_platform_placements (org_id,environment_id,resource) WHERE state<>'released' AND replaces<>''`,
				`CREATE TABLE IF NOT EXISTS loomplane_platform_egress_counters (
	org_id text NOT NULL,
	instance_id text NOT NULL,
	counter_id text NOT NULL,
	last_bytes bigint NOT NULL DEFAULT 0,
	PRIMARY KEY (org_id,instance_id,counter_id))`,
				`DROP TABLE IF EXISTS loomplane_platform_egress`,
			} {
				if _, err = db.Exec(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, stmt := range []string{
				// The old index allows one live placement per resource: a
				// replacement still waiting to run goes.
				`UPDATE loomplane_platform_placements SET state='released',updated_at=now() WHERE replaces<>'' AND state<>'released'`,
				`DROP INDEX IF EXISTS loomplane_platform_placements_replacing`,
				`DROP INDEX IF EXISTS loomplane_platform_placements_resource`,
				`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_platform_placements_resource ON loomplane_platform_placements (org_id,environment_id,resource) WHERE state<>'released'`,
				`ALTER TABLE loomplane_platform_placements DROP COLUMN IF EXISTS replaces`,
				`CREATE TABLE IF NOT EXISTS loomplane_platform_egress (
	placement_id text NOT NULL REFERENCES loomplane_platform_placements(id) ON DELETE CASCADE,
	counter_id text NOT NULL,
	last_bytes bigint NOT NULL DEFAULT 0,
	PRIMARY KEY (placement_id,counter_id))`,
				`DROP TABLE IF EXISTS loomplane_platform_egress_counters`,
			} {
				if _, err := db.Exec(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
