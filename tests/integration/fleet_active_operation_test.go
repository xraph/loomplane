//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
)

func TestFleetActiveOperationDiscoveryAndRecovery(t *testing.T) {
	f := newFleetFixture(t, 1)
	created, _ := reserveFleetCreate(t, f, "synthetic")
	p := security.Principal{OrgID: "org", UserID: "usr"}
	op, err := f.service.GetActiveOperation(t.Context(), p, created.Instance.ID)
	if err != nil || op.ID != created.Operation.ID {
		t.Fatalf("discover: %+v %v", op, err)
	}
	scope := f.scope
	scope.ProjectID = "other"
	if _, err = f.store.GetActiveOperation(t.Context(), scope, created.Instance.ID); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign project disclosed operation: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=2 WHERE id=$1`, created.Instance.ID); err != nil {
		t.Fatal(err)
	}
	op, err = f.service.GetActiveOperation(t.Context(), p, created.Instance.ID)
	if err != nil || op.Generation != 1 {
		t.Fatalf("older recovery operation hidden: %+v %v", op, err)
	}
	if _, err = f.service.CancelOperation(t.Context(), p, op.ID, 1); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("stale generation cancelled: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=1 WHERE id=$1`, created.Instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CancelOperation(t.Context(), p, op.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.GetActiveOperation(t.Context(), p, created.Instance.ID); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("terminal operation still active: %v", err)
	}
	if _, err = f.service.GetOperation(t.Context(), p, op.ID); err != nil {
		t.Fatalf("cancelled history hidden: %v", err)
	}
}
