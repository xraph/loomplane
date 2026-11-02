//go:build integration

package migrations

import (
	"net/url"
	"os"
	"testing"

	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/ops"
)

func TestManagedCleanupOrphanSourceVersionCompatibility(t *testing.T) {
	const legacy = "20261206000500"
	const renamed = "20261207000100"
	for _, appliedVersion := range []string{"fresh", legacy, renamed} {
		t.Run(appliedVersion, func(t *testing.T) {
			conn, executor := scratch(t)
			group := migrate.NewGroup(GroupName)
			initial := migrate.NewGroup(GroupName)
			found := 0
			for _, step := range Group().Migrations() {
				if step.Version > renamed {
					continue
				}
				copyStep := *step
				group.MustRegister(&copyStep)
				if step.Version == legacy || step.Version == renamed {
					found++
					if step.Version != appliedVersion {
						continue
					}
				}
				initialStep := *step
				initial.MustRegister(&initialStep)
			}
			if found != 2 {
				t.Fatalf("need both applied migration identities, found %d", found)
			}
			ctx := t.Context()
			if appliedVersion != "fresh" {
				if _, err := migrate.NewOrchestrator(executor, initial).Migrate(ctx); err != nil {
					t.Fatal(err)
				}
				dsn, err := url.Parse(os.Getenv("LOOMPLANE_TEST_DATABASE_URL"))
				if err != nil {
					t.Fatal(err)
				}
				dsn.Path = "/" + conn.Config().Database
				if err := ops.CheckCompatible(ctx, dsn.String(), []*migrate.Group{group}); err != nil {
					t.Fatal(err)
				}
				exec(t, conn, `INSERT INTO loomplane_provider_resource_accounts(org_id,provider,account_identity,sequence) VALUES('retained-org','test','retained-account',17)`)
			}
			if _, err := migrate.NewOrchestrator(executor, group).Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM grove_migrations WHERE "group"='loomplane' AND version IN ($1,$2)`, legacy, renamed).Scan(&count); err != nil || count != 2 {
				t.Fatalf("migration identities: %d, %v", count, err)
			}
			var guarded bool
			if err := conn.QueryRow(ctx, `SELECT pg_get_functiondef('loomplane_check_pending_cleanup_owner()'::regprocedure) LIKE '%CleanupSource%' AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='loomplane_guard_orphan_cleanup_source')`).Scan(&guarded); err != nil || !guarded {
				t.Fatalf("cleanup guards: %v, %v", guarded, err)
			}
			if appliedVersion != "fresh" {
				var sequence int
				if err := conn.QueryRow(ctx, `SELECT sequence FROM loomplane_provider_resource_accounts WHERE org_id='retained-org'`).Scan(&sequence); err != nil || sequence != 17 {
					t.Fatalf("retained account: %d, %v", sequence, err)
				}
			}
			orch := migrate.NewOrchestrator(executor, group)
			if _, err := orch.Migrate(ctx); err != nil {
				t.Fatalf("repeat migration: %v", err)
			}
			if _, err := orch.Rollback(ctx); err != nil {
				t.Fatalf("rollback duplicate identity: %v", err)
			}
			var retained bool
			if err := conn.QueryRow(ctx, `SELECT to_regprocedure('loomplane_guard_orphan_cleanup_source()') IS NOT NULL`).Scan(&retained); err != nil || !retained {
				t.Fatalf("rollback removed shared cleanup guard: %v, %v", retained, err)
			}
			if _, err := orch.Migrate(ctx); err != nil {
				t.Fatalf("reapply duplicate identity: %v", err)
			}
		})
	}
}
