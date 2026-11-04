package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What the platform Cloud's cleanup of a deleted organization remembers when
// the organization's rows can't all go yet: until when it waits before
// trying again, and why, so the platform owner sees the tenant as blocked.
//
// loomplane_platform_placements.starting_until claims a remembered placement
// for one restore's start: no other restore or reclaim touches it until the
// start ends or the claim runs out.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_platform_tenants").
				AddColumn("cleanup_blocked_until", migrate.Timestamp).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewAlterTable("loomplane_platform_tenants").
				AddColumn("cleanup_error", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewAlterTable("loomplane_platform_placements").
				AddColumn("starting_until", migrate.Timestamp).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewAlterTable("loomplane_platform_placements").DropColumn("starting_until").IfExists().Exec(ctx); err != nil {
				return err
			}
			if err := db.NewAlterTable("loomplane_platform_tenants").DropColumn("cleanup_error").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewAlterTable("loomplane_platform_tenants").DropColumn("cleanup_blocked_until").IfExists().Exec(ctx)
		},
	)
}
