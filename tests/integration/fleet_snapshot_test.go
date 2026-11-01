//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
)

func TestInstanceSnapshotRevalidated(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	input := f.input("a")
	input.ReleaseID = f.request.ReleaseID
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.store.GetRelease(t.Context(), f.scope, input.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fleets.RenderInstance(*release, reserved.Instance, input)
	if err != nil {
		t.Fatal(err)
	}
	f.request.SnapshotDigest = snapshot.Digest
	authority, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.VerifyInstanceSnapshot(t.Context(), *authority, *snapshot); err != nil {
		t.Fatal(err)
	}
	changed := *snapshot
	changed.Generation++
	if _, err = f.service.VerifyInstanceSnapshot(t.Context(), *authority, changed); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("forged generation: %v", err)
	}
	changed = *snapshot
	changed.Values = map[string]map[string]string{"api": {"UNREVIEWED": "value"}}
	if _, err = f.service.VerifyInstanceSnapshot(t.Context(), *authority, changed); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("forged variables: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=desired_generation+1 WHERE id=$1`, reserved.Instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.VerifyInstanceSnapshot(t.Context(), *authority, *snapshot); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("stale generation: %v", err)
	}
}
