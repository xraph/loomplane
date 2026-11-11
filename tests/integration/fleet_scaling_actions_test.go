//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func readyFleetScalingAction(t *testing.T) (fleetFixture, *fleet.OperationResult, fleet.ActionInput) {
	t.Helper()
	f, created, in := readyFleetActionFixture(t)
	in.Kind = "scale"
	in.IdempotencyKey = "scale-request"
	in.Update = nil
	policy := fleetScalingPolicy()
	in.Scale = &policy
	return f, created, in
}

func TestFleetScaleActionReplayPreservesPolicyAndActor(t *testing.T) {
	f, created, in := readyFleetScalingAction(t)
	op, err := f.service.RequestAction(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, created.Instance.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.ReserveAction(t.Context(), f.scope, security.Principal{OrgID: "org", UserID: "second-authorized-operator"}, created.Instance.ID, in)
	if err != nil || replay.ID != op.ID {
		t.Fatalf("scale replay: %+v %v", replay, err)
	}
	var raw string
	if err = f.sql.QueryRow(t.Context(), `SELECT payload FROM loomplane_fleet_operation_inputs WHERE operation_id=$1`, op.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var intent fleets.ActionIntent
	if err = json.Unmarshal([]byte(raw), &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Principal.UserID != "usr" || intent.Snapshot.Generation != 2 || intent.Input.Scale.Policy.Revision != 1 {
		t.Fatalf("original scale intent changed: %+v", intent)
	}
	var policies, operations, inputs, outbox int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_scaling_policies),(SELECT count(*) FROM loomplane_fleet_operations),(SELECT count(*) FROM loomplane_fleet_operation_inputs),(SELECT count(*) FROM loomplane_fleet_operation_outbox)`).Scan(&policies, &operations, &inputs, &outbox); err != nil {
		t.Fatal(err)
	}
	if policies != 1 || operations != 2 || inputs != 2 || outbox != 2 {
		t.Fatalf("replayed reservation: %d %d %d %d", policies, operations, inputs, outbox)
	}
}

func TestFleetScaleActionPolicyAndOutboxRollback(t *testing.T) {
	f, created, in := readyFleetScalingAction(t)
	if _, err := f.sql.Exec(t.Context(), `ALTER TABLE loomplane_fleet_operation_outbox ADD CONSTRAINT deny_scale_dispatch CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestAction(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, created.Instance.ID, in); err == nil {
		t.Fatal("scale accepted without outbox")
	}
	i, err := f.store.GetInstance(t.Context(), "org", created.Instance.ID)
	if err != nil || i.Revision != 1 || i.DesiredGeneration != 1 {
		t.Fatalf("failed scale advanced instance: %+v %v", i, err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_scaling_policies`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("policy survived action rollback: %d %v", count, err)
	}
}

// This uses synthetic terminal storage state. It does not exercise or qualify
// a scale cancellation workflow, which the current cancellation API lacks.
func TestFleetScaleActionCancelledRevisionNotReused(t *testing.T) {
	f, created, in := readyFleetScalingAction(t)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	op, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='cancelled' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET status='ready' WHERE id=$1`, created.Instance.ID); err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "next-scale"
	in.ExpectedGeneration = 2
	in.ExpectedRevision = 2
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, in); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("cancelled policy revision reused: %v", err)
	}
	in.Scale.ExpectedPolicyRevision = 1
	in.Scale.Policy.Revision = 2
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, in); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_scaling_policies`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cancelled history missing: %d %v", count, err)
	}
}
