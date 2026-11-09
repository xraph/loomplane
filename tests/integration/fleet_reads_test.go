//go:build integration

package integration

import (
	"errors"
	"sort"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetReadPaginationAndFilters(t *testing.T) {
	f := newFleetFixture(t, 4)
	p := security.Principal{OrgID: "org", UserID: "usr", ReadOnly: true}
	ids := make([]string, 0, 3)
	for _, ref := range []string{"first", "second", "third"} {
		result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input(ref))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.Instance.ID)
		op, err := f.service.GetOperation(t.Context(), p, result.Operation.ID)
		if err != nil || op.InstanceID != result.Instance.ID || op.Status != "waiting_approval" {
			t.Fatalf("operation: %+v %v", op, err)
		}
		if _, err = f.service.GetOperation(t.Context(), security.Principal{OrgID: "other", UserID: "usr"}, result.Operation.ID); !errors.Is(err, fleets.ErrNotFound) {
			t.Fatalf("foreign operation: %v", err)
		}
	}
	sort.Strings(ids)
	first, err := f.service.ListInstances(t.Context(), p, f.scope.FleetID, fleet.ListInstancesInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != ids[0] || first.Items[1].ID != ids[1] || first.NextCursor != ids[1] {
		t.Fatalf("first page: %+v", first)
	}
	last, err := f.service.ListInstances(t.Context(), p, f.scope.FleetID, fleet.ListInstancesInput{Limit: 2, After: first.NextCursor})
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != ids[2] || last.NextCursor != "" {
		t.Fatalf("last page: %+v %v", last, err)
	}
	filtered, err := f.service.ListInstances(t.Context(), p, f.scope.FleetID, fleet.ListInstancesInput{ExternalRef: "second"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ExternalRef != "second" {
		t.Fatalf("filter: %+v %v", filtered, err)
	}
	empty, err := f.service.ListInstances(t.Context(), p, f.scope.FleetID, fleet.ListInstancesInput{Status: "ready"})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	foreign, err := f.store.ListInstances(t.Context(), fleets.FleetScope{OrgID: "org", ProjectID: "other", FleetID: f.scope.FleetID}, fleet.ListInstancesInput{})
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign project: %+v %v", foreign, err)
	}
}
