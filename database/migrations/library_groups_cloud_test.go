//go:build cloud

package migrations

import (
	"slices"
	"testing"

	"github.com/xraph/grove/migrate"
)

// editionWantGroups are the groups only this edition adds after the
// libraries' groups: ledger's store, then Loomplane's billing tables.
func editionWantGroups() []string { return []string{"ledger", "loomplane_billing"} }

func TestBillingMigrationRunsAfterLoomplaneAndLedger(t *testing.T) {
	g := billingGroup()
	if g.Name() != "loomplane_billing" {
		t.Fatalf("group %q", g.Name())
	}
	if deps := g.DependsOnGroups(); !slices.Equal(deps, []string{GroupName, "ledger"}) {
		t.Fatalf("billing depends on %v", deps)
	}
	all := All()
	ledgerAt := slices.IndexFunc(all, func(g *migrate.Group) bool { return g.Name() == "ledger" })
	billingAt := slices.IndexFunc(all, func(g *migrate.Group) bool { return g.Name() == "loomplane_billing" })
	if ledgerAt < 0 || billingAt != ledgerAt+1 {
		t.Fatalf("ledger at %d, billing at %d", ledgerAt, billingAt)
	}
}
