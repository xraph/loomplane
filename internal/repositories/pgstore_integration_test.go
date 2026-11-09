//go:build integration

package repositories_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/repositories"
)

func TestPgStoreKeepsInstallationPermissions(t *testing.T) {
	ctx := context.Background()
	pg := pgdriver.New()
	if err := pg.Open(ctx, agenttest.FreshDatabase(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	s := repositories.NewPgStore(pg)
	created := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if err := s.SaveInstallation(ctx, &repositories.InstallationRecord{OrgID: "aorg_a", ID: 42, Account: "acme", AccountType: "Organization", CreatedBy: "ausr_a", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListInstallations(ctx, "aorg_a")
	if err != nil || len(recs) != 1 || recs[0].Permissions != nil || recs[0].PermissionsCheckedAt != nil {
		t.Fatalf("before any read: %+v, %v", recs, err)
	}

	perms := map[string]string{"contents": "read", "issues": "write", "metadata": "read"}
	at := created.Add(time.Minute)
	if err = s.SavePermissions(ctx, "aorg_a", 42, perms, at); err != nil {
		t.Fatal(err)
	}
	// Another organization's connection to the same installation is its own.
	if err = s.SavePermissions(ctx, "aorg_b", 42, map[string]string{}, at); err != nil {
		t.Fatal(err)
	}
	recs, err = s.ListInstallations(ctx, "aorg_a")
	if err != nil || len(recs) != 1 || !maps.Equal(recs[0].Permissions, perms) ||
		recs[0].PermissionsCheckedAt == nil || !recs[0].PermissionsCheckedAt.Equal(at) {
		t.Fatalf("after a read: %+v, %v", recs[0], err)
	}

	// A failed read moves only the check time, forward, and only once
	// something was read.
	failed := at.Add(5 * time.Minute)
	if err = s.DeferPermissionsCheck(ctx, "aorg_a", 42, failed); err != nil {
		t.Fatal(err)
	}
	if err = s.DeferPermissionsCheck(ctx, "aorg_a", 42, at); err != nil {
		t.Fatal(err)
	}
	recs, err = s.ListInstallations(ctx, "aorg_a")
	if err != nil || !maps.Equal(recs[0].Permissions, perms) || recs[0].PermissionsCheckedAt == nil || !recs[0].PermissionsCheckedAt.Equal(failed) {
		t.Fatalf("after a failed read: %+v, %v", recs[0], err)
	}
	if err = s.SaveInstallation(ctx, &repositories.InstallationRecord{OrgID: "aorg_c", ID: 7, Account: "initech", AccountType: "Organization", CreatedBy: "ausr_c", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeferPermissionsCheck(ctx, "aorg_c", 7, failed); err != nil {
		t.Fatal(err)
	}
	if recs, err = s.ListInstallations(ctx, "aorg_c"); err != nil || recs[0].PermissionsCheckedAt != nil {
		t.Fatalf("a failed first read was recorded as a check: %+v, %v", recs[0], err)
	}

	// Reconnecting refreshes the account, and keeps what was read.
	if err = s.SaveInstallation(ctx, &repositories.InstallationRecord{OrgID: "aorg_a", ID: 42, Account: "Acme", AccountType: "Organization", CreatedBy: "ausr_a", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	recs, err = s.ListInstallations(ctx, "aorg_a")
	if err != nil || recs[0].Account != "Acme" || !maps.Equal(recs[0].Permissions, perms) {
		t.Fatalf("after reconnecting: %+v, %v", recs[0], err)
	}
}
