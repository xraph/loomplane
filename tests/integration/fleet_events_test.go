//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func appendFleetTestEvent(t *testing.T, f fleetFixture, draft fleets.EventDraft) (*fleet.Event, error) {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := fleets.AppendEventTx(t.Context(), tx, f.scope, draft)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event, nil
}
func TestFleetEventReplayOrdered(t *testing.T) {
	f := newFleetFixture(t, 3)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("second"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, OperationID: result.Operation.ID, Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "running"}}
	first, err := appendFleetTestEvent(t, f, draft)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := appendFleetTestEvent(t, f, draft)
	if err != nil || duplicate.Sequence != first.Sequence || duplicate.ID != first.ID {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	draft.ID = uuid.NewString()
	draft.Lifecycle = &fleet.LifecycleEvent{State: "ready"}
	next, err := appendFleetTestEvent(t, f, draft)
	if err != nil {
		t.Fatal(err)
	}
	if next.Sequence != first.Sequence+1 {
		t.Fatalf("sequence: %d then %d", first.Sequence, next.Sequence)
	}
	p := security.Principal{OrgID: "org", UserID: "usr"}
	page, err := f.service.Events(t.Context(), p, result.Instance.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("page: %+v %v", page, err)
	}
	last, err := f.service.Events(t.Context(), p, result.Instance.ID, page.NextCursor, 1)
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != next.ID {
		t.Fatalf("last: %+v %v", last, err)
	}
	if _, err = f.service.Events(t.Context(), p, second.Instance.ID, page.NextCursor, 1); !errors.Is(err, fleets.ErrInvalid) {
		t.Fatalf("cursor scope: %v", err)
	}
	if _, err = f.service.Events(t.Context(), security.Principal{OrgID: "other", UserID: "usr"}, result.Instance.ID, "", 1); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign scope: %v", err)
	}
	draft.Lifecycle = &fleet.LifecycleEvent{State: "failed"}
	if _, err = appendFleetTestEvent(t, f, draft); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("id reuse: %v", err)
	}
}
func TestFleetEventStaleGeneration(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=2 WHERE id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, Generation: 2, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "running"}}
	if _, err = appendFleetTestEvent(t, f, draft); err != nil {
		t.Fatal(err)
	}
	draft.ID = uuid.NewString()
	draft.Generation = 1
	if _, err = appendFleetTestEvent(t, f, draft); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("stale generation: %v", err)
	}
	draft.Generation = 3
	if _, err = appendFleetTestEvent(t, f, draft); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("future generation: %v", err)
	}
}

func TestFleetEventRollbackAndRetention(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fleets.AppendEventTx(t.Context(), tx, f.scope, draft); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	p := security.Principal{OrgID: "org", UserID: "usr"}
	empty, err := f.service.Events(t.Context(), p, result.Instance.ID, "", 10)
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("rollback leaked event: %+v %v", empty, err)
	}
	saved, err := appendFleetTestEvent(t, f, draft)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Sequence != 1 {
		t.Fatalf("rollback consumed sequence: %d", saved.Sequence)
	}
	// Clearing the payload models retention without changing its immutable receipt.
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_events SET payload=NULL,delivery_state='accepted' WHERE id=$1`, draft.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := appendFleetTestEvent(t, f, draft)
	if err != nil || replay.Sequence != 1 || !replay.OccurredAt.Equal(saved.OccurredAt) {
		t.Fatalf("retained replay: %+v %v", replay, err)
	}
	draft.ID = uuid.NewString()
	draft.Lifecycle = &fleet.LifecycleEvent{State: "degraded"}
	if _, err = appendFleetTestEvent(t, f, draft); err != nil {
		t.Fatal(err)
	}
	page, err := f.service.Events(t.Context(), p, result.Instance.ID, "", 10)
	if err != nil || len(page.Items) != 1 || !page.ReplayTruncated || len(page.Acceptance) != 1 || page.Acceptance[0].State != "pending" {
		t.Fatalf("retention gap: %+v %v", page, err)
	}
}

func TestFleetDeliveryBoundedRetryAndLease(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if _, err = appendFleetTestEvent(t, f, draft); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 10; attempt++ {
		claimed, claimErr := f.store.ClaimEventDelivery(t.Context(), draft.ID)
		if claimErr != nil || claimed == nil {
			t.Fatalf("claim %d: %+v %v", attempt, claimed, claimErr)
		}
		duplicate, duplicateErr := f.store.ClaimEventDelivery(t.Context(), draft.ID)
		if duplicateErr != nil || duplicate != nil {
			t.Fatalf("lease double-claimed: %+v %v", duplicate, duplicateErr)
		}
		if err = f.store.FinishEventDelivery(t.Context(), draft.ID, "wrong-lease", true, ""); !errors.Is(err, fleets.ErrConflict) {
			t.Fatalf("stale lease accepted: %v", err)
		}
		if err = f.store.FinishEventDelivery(t.Context(), draft.ID, claimed.Lease, false, "acceptance_failed"); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_events SET next_delivery_at=clock_timestamp() WHERE id=$1`, draft.ID); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := f.store.ClaimEventDelivery(t.Context(), draft.ID)
	if err != nil || claimed != nil {
		t.Fatalf("retry limit: %+v %v", claimed, err)
	}
	page, err := f.service.Events(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, result.Instance.ID, "", 10)
	if err != nil || page.Acceptance[0].State != "dead_letter" || page.Acceptance[0].Attempts != 10 {
		t.Fatalf("dead letter invisible: %+v %v", page, err)
	}
}

func TestFleetDeliveryLostAcknowledgementKeepsIdentity(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if _, err = appendFleetTestEvent(t, f, draft); err != nil {
		t.Fatal(err)
	}
	first, err := f.store.ClaimEventDelivery(t.Context(), draft.ID)
	if err != nil || first == nil {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_events SET delivery_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, draft.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.ClaimEventDelivery(t.Context(), draft.ID)
	if err != nil || replay == nil || replay.Lease == first.Lease || replay.Event.ID != first.Event.ID || replay.Event.Sequence != first.Event.Sequence {
		t.Fatalf("lost acknowledgement: %+v %v", replay, err)
	}
	if err = f.store.FinishEventDelivery(t.Context(), draft.ID, first.Lease, true, ""); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("old worker acknowledged new lease: %v", err)
	}
	if err = f.store.FinishEventDelivery(t.Context(), draft.ID, replay.Lease, true, ""); err != nil {
		t.Fatal(err)
	}
	again, err := f.store.ClaimEventDelivery(t.Context(), draft.ID)
	if err != nil || again != nil {
		t.Fatalf("accepted event reclaimed: %+v %v", again, err)
	}
}

func TestFleetEventConcurrentSequence(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() {
			tx, beginErr := f.db.BeginTx(t.Context(), nil)
			if beginErr != nil {
				results <- beginErr
				return
			}
			_, appendErr := fleets.AppendEventTx(t.Context(), tx, f.scope, fleets.EventDraft{ID: uuid.NewString(), InstanceID: result.Instance.ID, Generation: 1, Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "running"}})
			if appendErr != nil {
				results <- errors.Join(appendErr, tx.Rollback())
				return
			}
			results <- tx.Commit()
		}()
	}
	for range 8 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.service.Events(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, result.Instance.ID, "", 10)
	if err != nil || len(page.Items) != 8 {
		t.Fatalf("concurrent history: %+v %v", page, err)
	}
	for index, event := range page.Items {
		if event.Sequence != int64(index+1) {
			t.Fatalf("sequence at %d: %d", index, event.Sequence)
		}
	}
}

func TestFleetEventReleaseTargetPinsAreSeparateFromEnvelope(t *testing.T) {
	f := newFleetFixture(t, 2)
	first, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=2 WHERE id=$1`, first.Instance.ID); err != nil {
		t.Fatal(err)
	}
	draft := fleets.EventDraft{ID: uuid.NewString(), InstanceID: first.Instance.ID, Generation: 2, Kind: "release", Release: &fleet.ReleaseEvent{CampaignID: "campaign", Wave: 0, State: "paused", Reason: "generation_conflict", TargetGeneration: 1, TargetOperationID: first.Operation.ID}}
	event, err := appendFleetTestEvent(t, f, draft)
	if err != nil {
		t.Fatal(err)
	}
	if event.Generation != 2 || event.OperationID != "" || event.Release.TargetGeneration != 1 || event.Release.TargetOperationID != first.Operation.ID {
		t.Fatalf("pins: %+v", event)
	}
	for _, which := range []string{"foreign-instance", "wrong-target-generation", "wrong-envelope-operation"} {
		t.Run(which, func(t *testing.T) {
			changed := draft
			changed.ID = uuid.NewString()
			release := *draft.Release
			changed.Release = &release
			switch which {
			case "foreign-instance":
				release.TargetOperationID = other.Operation.ID
			case "wrong-target-generation":
				release.TargetGeneration = 2
			case "wrong-envelope-operation":
				changed.OperationID = first.Operation.ID
			}
			if _, err := appendFleetTestEvent(t, f, changed); err == nil {
				t.Fatal("mismatched operation pins accepted")
			}
		})
	}
}
