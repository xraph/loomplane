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

func reserveFleetCreate(t *testing.T, f fleetFixture, ref string) (*fleet.OperationResult, fleet.CreateInstanceInput) {
	t.Helper()
	raw := []byte(`schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      build: {image: registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 1}
`)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	release, err := f.service.PublishRelease(t.Context(), p, f.scope.FleetID, raw)
	if err != nil {
		t.Fatal(err)
	}
	in := f.input(ref)
	in.ReleaseID = release.ID
	result, err := f.service.CreateInstance(t.Context(), p, f.scope.FleetID, in)
	if err != nil {
		t.Fatal(err)
	}
	return result, in
}

func TestFleetCreateDuplicateRequest(t *testing.T) {
	f := newFleetFixture(t, 1)
	first, in := reserveFleetCreate(t, f, "synthetic")
	second, err := f.service.CreateInstance(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, in)
	if err != nil || first.Operation.ID != second.Operation.ID || first.Instance.ID != second.Instance.ID {
		t.Fatalf("replay: %+v %v", second, err)
	}
	var inputs, outbox int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_operation_inputs),(SELECT count(*) FROM loomplane_fleet_operation_outbox)`).Scan(&inputs, &outbox); err != nil {
		t.Fatal(err)
	}
	if inputs != 1 || outbox != 1 {
		t.Fatalf("duplicate intent: inputs=%d outbox=%d", inputs, outbox)
	}
	in.ExternalRef = "changed"
	if _, err = f.service.CreateInstance(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, in); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
}

func TestFleetCreateCrashResume(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, in := reserveFleetCreate(t, f, "synthetic")
	var raw []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT payload FROM loomplane_fleet_operation_inputs WHERE operation_id=$1`, result.Operation.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var intent fleets.CreateIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Principal.UserID != "usr" || intent.Principal.OrgID != "org" || intent.Input.ReleaseID != in.ReleaseID || intent.Snapshot.InstanceID != result.Instance.ID || intent.Snapshot.EnvironmentID != result.Instance.EnvironmentID || intent.Deadline.IsZero() {
		t.Fatalf("incomplete restart input: %+v", intent)
	}
	// A new store after process loss must find the same operation and dispatch item.
	store := fleets.NewPgStore(f.db)
	replay, err := store.ReserveCreate(t.Context(), f.scope, intent.Principal, in)
	if err != nil || replay.Operation.ID != result.Operation.ID {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	var pending int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_operation_outbox WHERE operation_id=$1 AND published_at IS NULL`, result.Operation.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("accepted work was lost: %d", pending)
	}
}

func TestFleetCreateOutboxRollback(t *testing.T) {
	f := newFleetFixture(t, 2)
	_, in := reserveFleetCreate(t, f, "first")
	if _, err := f.sql.Exec(t.Context(), `ALTER TABLE loomplane_fleet_operation_outbox ADD CONSTRAINT deny_dispatch CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	in.ExternalRef, in.IdempotencyKey = "second", "create-second"
	if _, err := f.service.CreateInstance(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, in); err == nil {
		t.Fatal("accepted without dispatch intent")
	}
	var instances, environments, operations int
	if err := f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_instances),(SELECT count(*) FROM loomplane_environments WHERE name LIKE 'instance-%'),(SELECT count(*) FROM loomplane_fleet_operations)`).Scan(&instances, &environments, &operations); err != nil {
		t.Fatal(err)
	}
	if instances != 1 || environments != 1 || operations != 1 {
		t.Fatalf("partial transaction: %d %d %d", instances, environments, operations)
	}
}

func TestFleetCreateIntentImmutable(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, _ := reserveFleetCreate(t, f, "synthetic")
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operation_inputs SET payload='{}' WHERE operation_id=$1`, result.Operation.ID); err == nil {
		t.Fatal("saved create authority/input was rewritten")
	}
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_fleet_operation_inputs WHERE operation_id=$1`, result.Operation.ID); err == nil {
		t.Fatal("saved recovery input was removed")
	}
}

func TestFleetCreateCancelQueued(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, _ := reserveFleetCreate(t, f, "synthetic")
	p := security.Principal{OrgID: "org", UserID: "usr"}
	if _, err := f.service.CancelOperation(t.Context(), p, result.Operation.ID, 2); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("stale cancellation: %v", err)
	}
	op, err := f.service.CancelOperation(t.Context(), p, result.Operation.ID, 1)
	if err != nil || op.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", op, err)
	}
	instance, err := f.service.GetInstance(t.Context(), p, result.Instance.ID)
	if err != nil || instance.Status != "deleted" || instance.Revision != 2 {
		t.Fatalf("cancelled reservation: %+v %v", instance, err)
	}
	reserveFleetCreate(t, f, "replacement")
}

func TestFleetCreateCancelAfterEffectsRequiresRecovery(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, _ := reserveFleetCreate(t, f, "synthetic")
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='provisioning' WHERE id=$1`, result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CancelOperation(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, result.Operation.ID, 1); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("unsafe immediate cancellation: %v", err)
	}
}

func TestFleetCreateCancelAdmittedAuthorityRequiresRecovery(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	in := f.input("a")
	in.ReleaseID = f.request.ReleaseID
	result, err := f.service.CreateInstance(t.Context(), f.principal, f.scope.FleetID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CancelOperation(t.Context(), f.principal, result.Operation.ID, 1); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("admitted authority discarded: %v", err)
	}
}
