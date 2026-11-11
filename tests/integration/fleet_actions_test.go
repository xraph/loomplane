//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func readyFleetActionFixture(t *testing.T) (fleetFixture, *fleet.OperationResult, fleet.ActionInput) {
	t.Helper()
	f := newFleetFixture(t, 2)
	created, input := reserveFleetCreate(t, f, "synthetic")
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='succeeded' WHERE id=$1`, created.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET status='ready',observed_generation=desired_generation,observed_release_id=desired_release_id WHERE id=$1`, created.Instance.ID); err != nil {
		t.Fatal(err)
	}
	in := fleet.ActionInput{Kind: "update", IdempotencyKey: "update-synthetic", ExpectedRevision: 1, ExpectedGeneration: 1, Update: &fleet.UpdateInput{ReleaseID: input.ReleaseID}}
	return f, created, in
}

func TestFleetActionReplayPrecedesVersionChecks(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	op, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
	if err != nil || op.Generation != 2 || op.Status != "waiting_approval" {
		t.Fatalf("accept: %+v %v", op, err)
	}
	replay, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
	if err != nil || replay.ID != op.ID {
		t.Fatalf("replay after advance: %+v %v", replay, err)
	}
	found, err := f.service.FindAction(t.Context(), p, created.Instance.ID, in)
	if err != nil || found.ID != op.ID {
		t.Fatalf("lost receipt lookup: %+v %v", found, err)
	}
	changed := in
	changed.ExpectedRevision = 2
	if _, err = f.service.FindAction(t.Context(), p, created.Instance.ID, changed); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("changed body lookup: %v", err)
	}
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, changed); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("changed body replay: %v", err)
	}
	changed.IdempotencyKey = "next"
	changed.ExpectedGeneration = 2
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, changed); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("overlapping mutation: %v", err)
	}
	var operations, inputs, outbox int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_operations),(SELECT count(*) FROM loomplane_fleet_operation_inputs),(SELECT count(*) FROM loomplane_fleet_operation_outbox)`).Scan(&operations, &inputs, &outbox); err != nil {
		t.Fatal(err)
	}
	if operations != 2 || inputs != 2 || outbox != 2 {
		t.Fatalf("duplicate intent: %d %d %d", operations, inputs, outbox)
	}
}

func TestFleetActionOutboxFailurePreservesInstance(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	if _, err := f.sql.Exec(t.Context(), `ALTER TABLE loomplane_fleet_operation_outbox ADD CONSTRAINT deny_action_dispatch CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestAction(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, created.Instance.ID, in); err == nil {
		t.Fatal("accepted without durable dispatch")
	}
	instance, err := f.store.GetInstance(t.Context(), "org", created.Instance.ID)
	if err != nil || instance.Revision != 1 || instance.DesiredGeneration != 1 || instance.Status != "ready" {
		t.Fatalf("failed admission changed instance: %+v %v", instance, err)
	}
}

func TestFleetRestoreDoesNotMutateSourceIdentity(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	in.Kind = "restore"
	in.Update = nil
	in.Restore = &fleet.RestoreInput{RecoveryPointID: "point", TargetStageID: f.stage.ID, TargetProfileID: "small"}
	if _, err := f.service.RequestAction(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, created.Instance.ID, in); !errors.Is(err, fleets.ErrApprovalRequired) {
		t.Fatalf("unqualified restore accepted: %v", err)
	}
	instance, err := f.store.GetInstance(t.Context(), "org", created.Instance.ID)
	if err != nil || instance.Revision != 1 || instance.DesiredGeneration != 1 || instance.EnvironmentID != created.Instance.EnvironmentID {
		t.Fatalf("restore changed source: %+v %v", instance, err)
	}
}

func TestFleetActionConcurrentRequestsReserveOnce(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	type result struct {
		op  *fleet.Operation
		err error
	}
	results := make(chan result, 6)
	var workers sync.WaitGroup
	for n := 0; n < 6; n++ {
		workers.Go(func() {
			op, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
			results <- result{op, err}
		})
	}
	workers.Wait()
	close(results)
	id := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == "" {
			id = result.op.ID
		}
		if result.op.ID != id {
			t.Fatal("concurrent replay created a second operation")
		}
	}
	instance, err := f.store.GetInstance(t.Context(), "org", created.Instance.ID)
	if err != nil || instance.Revision != 2 || instance.DesiredGeneration != 2 {
		t.Fatalf("duplicate version advance: %+v %v", instance, err)
	}
}

func TestFleetActionReplayPreservesOriginalActor(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	op, err := f.service.RequestAction(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, created.Instance.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.ReserveAction(t.Context(), f.scope, security.Principal{OrgID: "org", UserID: "second-authorized-operator"}, created.Instance.ID, in)
	if err != nil || replay.ID != op.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	var raw []byte
	if err = f.sql.QueryRow(t.Context(), `SELECT payload FROM loomplane_fleet_operation_inputs WHERE operation_id=$1`, op.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var intent fleets.ActionIntent
	if err = json.Unmarshal(raw, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Principal.UserID != "usr" || intent.PreviousStatus != "ready" || intent.Snapshot.Generation != 2 {
		t.Fatalf("original intent replaced: %+v", intent)
	}
}

func TestFleetActionRepairRequiresReleasedMutationLease(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	failed, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='needs_attention' WHERE id=$1`, failed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET status='failed' WHERE id=$1`, created.Instance.ID); err != nil {
		t.Fatal(err)
	}
	repair := in
	repair.IdempotencyKey = "repair"
	repair.ExpectedGeneration = 2
	repair.ExpectedRevision = 2
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, repair); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("repair overlapped unresolved work: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='failed' WHERE id=$1`, failed.ID); err != nil {
		t.Fatal(err)
	}
	next, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, repair)
	if err != nil || next.Generation != 3 || next.Status != "waiting_approval" {
		t.Fatalf("reviewed repair intent: %+v %v", next, err)
	}
}

func TestFleetActionReplayRequiresSavedIntent(t *testing.T) {
	f, created, in := readyFleetActionFixture(t)
	p := security.Principal{OrgID: "org", UserID: "usr"}
	accepted, err := f.service.RequestAction(t.Context(), p, created.Instance.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	// Model an older terminal reservation that predates durable action inputs.
	// Reuse a valid request hash so only the missing recovery intent differs.
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_fleet_operations(id,org_id,fleet_id,instance_id,kind,status,idempotency_key,request_hash,generation,created_at) VALUES('legacy-action',$1,$2,$3,'update','failed','legacy-key',$4,2,clock_timestamp())`, f.scope.OrgID, f.scope.FleetID, created.Instance.ID, accepted.RequestHash); err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "legacy-key"
	if _, err = f.service.RequestAction(t.Context(), p, created.Instance.ID, in); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("unjournaled reservation accepted as replay: %v", err)
	}
}
