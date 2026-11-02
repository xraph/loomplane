//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestExpansionListAndApprovalBehindTheFlag(t *testing.T) {
	f, _, d := gpuExpansionFixtureFor(t, h100Size, provider.GPUStatusReady, true)
	ctx := t.Context()
	e := ensureExpansion(t, f, d)

	list, err := f.svc.ListExpansions(ctx, f.p, "cloud")
	if err != nil || len(list) != 1 || list[0].ID != e.ID || list[0].Status != "awaiting_approval" {
		t.Fatalf("listed expansions: %+v %v", list, err)
	}
	if !list[0].HourlyPriceKnown || list[0].EstimatedHourlyUSD != 3.39 {
		t.Fatalf("an approval needs the new server's price: %+v", list[0])
	}
	if _, err = f.svc.ListExpansions(ctx, security.Principal{OrgID: "org", UserID: "developer"}, "cloud"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a non-manager listed expansions: %v", err)
	}
	if other, listErr := f.svc.ListExpansions(ctx, security.Principal{OrgID: "elsewhere", UserID: "manager"}, "cloud"); listErr == nil && len(other) > 0 {
		t.Fatalf("another workspace saw expansions: %+v", other)
	}

	// With automatic capacity off, nothing is approved, whatever the policy says.
	f.svc.EnableAutomaticCapacity = false
	if f.svc.AutomaticExpansion() {
		t.Fatal("flag reads on")
	}
	if _, err = f.svc.ApproveExpansion(ctx, f.p, "cloud", e.ID, e.Revision); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("approved with automatic capacity off: %v", err)
	}
	if got, getErr := f.store.GetExpansion(ctx, "org", "cloud", e.ID); getErr != nil || got.ApprovedBy != "" || got.OperationID != "" {
		t.Fatalf("a refused approval left an approval behind: %+v %v", got, getErr)
	}
}
