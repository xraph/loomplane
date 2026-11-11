//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/fleet"
)

type scalingJournalHarness struct {
	f fleetFixture
	r *fleet.OperationResult
}

func newScalingJournalHarness(t *testing.T) scalingJournalHarness {
	t.Helper()
	f, r := newScalingJournalFixture(t)
	return scalingJournalHarness{f: f, r: r}
}

func (h scalingJournalHarness) append(t *testing.T, e fleets.ScalingWriterEvent, verifyErr, domainErr error) error {
	t.Helper()
	return scalingTransaction(t, h.f, func(tx driver.Tx) error {
		_, err := h.f.store.AdvanceScalingWriterTx(t.Context(), tx, h.f.scope, h.r.Instance.ID, h.r.Operation.ID, "api", 1, e, scalingJournalTestVerifier{err: verifyErr})
		if err != nil {
			return err
		}
		return domainErr
	})
}

func nextScalingJournalEvent(previous fleets.ScalingWriterEvent, phase, proof string) fleets.ScalingWriterEvent {
	next := previous
	next.Sequence++
	next.Phase = phase
	next.ProofKind = proof
	next.ProofID = fmt.Sprintf("proof-%d", next.Sequence)
	next.ObservedAt = time.Now().UTC()
	next.ExpiresAt = next.ObservedAt.Add(time.Minute)
	if phase == "active" {
		next.WriterUID = "observed-manual-lease"
		next.DeploymentGeneration++
		next.DeploymentResourceVersion = "observed-version-2"
	}
	return next
}

// assertHistory checks persisted payloads, not just the transition result. The
// test verifier supplies synthetic receipts; no native writer is exercised.
func (h scalingJournalHarness) assertHistory(t *testing.T, events ...fleets.ScalingWriterEvent) {
	t.Helper()
	journal, err := h.f.store.GetScalingWriterJournal(t.Context(), h.f.scope, h.r.Instance.ID, h.r.Operation.ID, "api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Policy.OperationID != h.r.Operation.ID || journal.Policy.Generation != h.r.Instance.DesiredGeneration || journal.Policy.OwnershipEpoch != h.r.Instance.OwnershipEpoch || journal.Policy.Input.Policy.Revision != 1 {
		t.Fatalf("recovery lost policy identity: %+v", journal.Policy)
	}
	if len(events) == 0 {
		if journal.LastEvent != nil {
			t.Fatalf("pending policy claims a writer: %+v", journal.LastEvent)
		}
	} else if journal.LastEvent == nil || !reflect.DeepEqual(*journal.LastEvent, events[len(events)-1]) {
		t.Fatalf("recovery changed saved event: %+v", journal.LastEvent)
	}
	rows, err := h.f.sql.Query(t.Context(), `SELECT sequence,phase,payload FROM loomplane_fleet_scaling_writer_events WHERE org_id=$1 AND instance_id=$2 AND service='api' AND policy_revision=1 ORDER BY sequence`, h.f.scope.OrgID, h.r.Instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var seq int64
		var phase, payload string
		if err = rows.Scan(&seq, &phase, &payload); err != nil {
			t.Fatal(err)
		}
		if index >= len(events) {
			t.Fatal("unexpected journal append")
		}
		var saved fleets.ScalingWriterEvent
		if err = json.Unmarshal([]byte(payload), &saved); err != nil {
			t.Fatal(err)
		}
		if seq != int64(index+1) || phase != events[index].Phase || !reflect.DeepEqual(saved, events[index]) {
			t.Fatalf("journal history changed at sequence %d: %+v", seq, saved)
		}
		index++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(events) {
		t.Fatalf("history length: got %d want %d", index, len(events))
	}
}

func TestFleetScaleWriterJournalFullSequence(t *testing.T) {
	h := newScalingJournalHarness(t)
	revoking := scalingJournalEvent()
	quiesced := nextScalingJournalEvent(revoking, "quiesced", "writers_fenced")
	applying := nextScalingJournalEvent(quiesced, "applying", "apply_authorized")
	active := nextScalingJournalEvent(applying, "active", "writer_observed")
	events := []fleets.ScalingWriterEvent{revoking, quiesced, applying, active}
	h.assertHistory(t)
	for index, event := range events {
		t.Run(event.Phase, func(t *testing.T) {
			rollbackErr := errors.New("owning domain transaction failed")
			if err := h.append(t, event, nil, rollbackErr); !errors.Is(err, rollbackErr) {
				t.Fatalf("rollback: %v", err)
			}
			h.assertHistory(t, events[:index]...)
			if err := h.append(t, event, nil, nil); err != nil {
				t.Fatal(err)
			}
			h.assertHistory(t, events[:index+1]...)
			if err := h.append(t, event, nil, nil); err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			h.assertHistory(t, events[:index+1]...)
			changed := event
			changed.ProofID = "changed-proof"
			if err := h.append(t, changed, nil, nil); !errors.Is(err, fleets.ErrConflict) {
				t.Fatalf("changed replay: %v", err)
			}
			if index < len(events)-1 {
				skipped := events[index+1]
				skipped.Sequence++
				if err := h.append(t, skipped, nil, nil); !errors.Is(err, fleets.ErrConflict) {
					t.Fatalf("skipped sequence: %v", err)
				}
			}
			if index < 2 {
				skippedPhase := active
				skippedPhase.Sequence = event.Sequence + 1
				if err := h.append(t, skippedPhase, nil, nil); !errors.Is(err, fleets.ErrConflict) {
					t.Fatalf("activation skipped a phase: %v", err)
				}
			}
			h.assertHistory(t, events[:index+1]...)
		})
	}
	for _, query := range []string{
		`UPDATE loomplane_fleet_scaling_writer_events SET phase='active' WHERE sequence=2`,
		`DELETE FROM loomplane_fleet_scaling_writer_events WHERE sequence=3`,
		`UPDATE loomplane_fleet_scaling_policies SET generation=generation+1`,
	} {
		if _, err := h.f.sql.Exec(t.Context(), query); err == nil {
			t.Fatalf("immutable history mutation accepted: %s", query)
		}
	}
	h.assertHistory(t, events...)
	var status string
	if err := h.f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_fleet_operations WHERE id=$1`, h.r.Operation.ID).Scan(&status); err != nil || status != "provisioning" {
		t.Fatalf("writer activation incorrectly completed operation: %s %v", status, err)
	}
}

func TestFleetScaleWriterJournalAmbiguousApplyRecovery(t *testing.T) {
	h := newScalingJournalHarness(t)
	revoking := scalingJournalEvent()
	quiesced := nextScalingJournalEvent(revoking, "quiesced", "writers_fenced")
	applying := nextScalingJournalEvent(quiesced, "applying", "apply_authorized")
	unknown := nextScalingJournalEvent(applying, "applying", "effect_unknown")
	history := make([]fleets.ScalingWriterEvent, 0, 5)
	history = append(history, revoking, quiesced, applying, unknown)
	for _, event := range history {
		if err := h.append(t, event, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	h.assertHistory(t, history...)
	journal, err := h.f.store.GetScalingWriterJournal(t.Context(), h.f.scope, h.r.Instance.ID, h.r.Operation.ID, "api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if journal.LastEvent.WriterUID != "" || journal.LastEvent.ObservedWriter(journal.Policy.Input.Policy) != "unknown" || journal.LastEvent.Binding != applying.Binding {
		t.Fatalf("ambiguous apply claims activation or lost identity: %+v", journal.LastEvent)
	}
	if err = h.append(t, unknown, nil, nil); err != nil {
		t.Fatalf("unknown result replay: %v", err)
	}
	if err = h.append(t, applying, nil, nil); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("old apply intent replay after ambiguity: %v", err)
	}
	active := nextScalingJournalEvent(unknown, "active", "writer_observed")
	for _, tc := range []struct {
		name      string
		change    func(*fleets.ScalingWriterEvent)
		verifyErr error
		want      error
	}{
		{"intent is not activation", func(e *fleets.ScalingWriterEvent) { e.ProofKind = "apply_authorized" }, nil, fleets.ErrConflict},
		{"unknown is not activation", func(e *fleets.ScalingWriterEvent) { e.ProofKind = "effect_unknown" }, nil, fleets.ErrConflict},
		{"expired observation", func(e *fleets.ScalingWriterEvent) { e.ExpiresAt = e.ObservedAt.Add(-time.Second) }, nil, fleets.ErrConflict},
		{"replacement controller", func(e *fleets.ScalingWriterEvent) { e.Binding.DeploymentUID = "replacement" }, nil, fleets.ErrConflict},
		{"unverified observation", func(*fleets.ScalingWriterEvent) {}, fleets.ErrApprovalRequired, fleets.ErrApprovalRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := active
			tc.change(&candidate)
			if appendErr := h.append(t, candidate, tc.verifyErr, nil); !errors.Is(appendErr, tc.want) {
				t.Fatalf("false activation: %v", appendErr)
			}
			h.assertHistory(t, history...)
		})
	}
	rollbackErr := errors.New("activation domain transaction failed")
	if err = h.append(t, active, nil, rollbackErr); !errors.Is(err, rollbackErr) {
		t.Fatalf("activation rollback: %v", err)
	}
	h.assertHistory(t, history...)
	if err = h.append(t, active, nil, nil); err != nil {
		t.Fatal(err)
	}
	history = append(history, active)
	h.assertHistory(t, history...)
	if err = h.append(t, active, nil, nil); err != nil {
		t.Fatalf("activation replay: %v", err)
	}
	h.assertHistory(t, history...)
}
