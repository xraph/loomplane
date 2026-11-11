//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/fleet"
)

func fleetScalingPolicy() fleet.ScaleInput {
	return fleet.ScaleInput{Service: "api", Policy: fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}
}

func TestFleetScalePolicyRevisionConcurrent(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("scaling"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET kind='scale' WHERE id=$1`, result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	in := fleetScalingPolicy()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			tx, beginErr := f.db.BeginTx(t.Context(), nil)
			if beginErr != nil {
				results <- beginErr
				return
			}
			checkErr := fleets.CheckScalingPolicyRevisionTx(t.Context(), tx, f.scope, result.Instance.ID, in.Service, 0)
			if checkErr == nil {
				checkErr = insertScalingPolicy(t.Context(), f.store, tx, f.scope, *result, in)
			}
			if checkErr == nil {
				checkErr = tx.Commit()
			} else if rollbackErr := tx.Rollback(); rollbackErr != nil {
				checkErr = errors.Join(checkErr, rollbackErr)
			}
			results <- checkErr
		})
	}
	close(start)
	workers.Wait()
	close(results)
	success, conflict := 0, 0
	for resultErr := range results {
		if resultErr == nil {
			success++
		} else if errors.Is(resultErr, fleets.ErrConflict) {
			conflict++
		} else {
			t.Fatal(resultErr)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent first policy revisions: success=%d conflict=%d", success, conflict)
	}
}

func TestFleetScalePolicyScopeAndRollback(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("scaling"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET kind='scale' WHERE id=$1`, result.Operation.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := f.scope
	scope.OrgID = "other"
	err = fleets.CheckScalingPolicyRevisionTx(t.Context(), tx, scope, result.Instance.ID, "api", 0)
	if !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign policy scope: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, err = f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = fleets.CheckScalingPolicyRevisionTx(t.Context(), tx, f.scope, result.Instance.ID, "api", 0); err != nil {
		t.Fatal(err)
	}
	if err = insertScalingPolicy(t.Context(), f.store, tx, f.scope, *result, fleetScalingPolicy()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_scaling_policies`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("policy leaked from rollback: %d %v", count, err)
	}
}

func insertScalingPolicy(ctx context.Context, store *fleets.PgStore, tx driver.Tx, scope fleets.FleetScope, result fleet.OperationResult, in fleet.ScaleInput) error {
	return store.ReserveScalingPolicyTx(ctx, tx, scope, result.Instance.ID, result.Operation.ID, result.Instance.DesiredGeneration, result.Instance.OwnershipEpoch, in)
}

func scalingTransaction(t *testing.T, f fleetFixture, fn func(driver.Tx) error) error {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func newScalingJournalFixture(t *testing.T) (fleetFixture, *fleet.OperationResult) {
	t.Helper()
	f := newFleetFixture(t, 1)
	r, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("scaling"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET kind='scale' WHERE id=$1`, r.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err = scalingTransaction(t, f, func(tx driver.Tx) error {
		return insertScalingPolicy(t.Context(), f.store, tx, f.scope, *r, fleetScalingPolicy())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='provisioning' WHERE id=$1`, r.Operation.ID); err != nil {
		t.Fatal(err)
	}
	return f, r
}

// This verifier exercises journal persistence only. It is never installed in a
// worker and provides no native fencing, scaling or application qualification.
type scalingJournalTestVerifier struct{ err error }

func (v scalingJournalTestVerifier) VerifyScalingWriterTransition(context.Context, driver.Tx, fleets.ScalingPolicyRecord, fleets.ScalingWriterEvent) error {
	return v.err
}

func scalingJournalEvent() fleets.ScalingWriterEvent {
	now := time.Now().UTC()
	return fleets.ScalingWriterEvent{Sequence: 1, Phase: "revoking", ProofKind: "revoke_authorized", ProofID: "proof-1", ObservedAt: now, ExpiresAt: now.Add(time.Minute), DeploymentGeneration: 1, DeploymentResourceVersion: "version-1", Binding: fleets.ScalingWriterBinding{AuthorityID: "authority", AuthorityRevision: 1, ConnectionID: "connection", AttachmentID: "attachment", AttachmentRevision: 1, TrustRevision: 1, Namespace: "namespace", NamespaceUID: "namespace-uid", Deployment: "deployment", DeploymentUID: "deployment-uid", RuntimeRef: "runtime", RuntimeRevision: 1, OldWriter: "hpa", OldWriterUID: "old-hpa-uid"}}
}

func TestFleetScaleWriterJournalConcurrentReplay(t *testing.T) {
	f, r := newScalingJournalFixture(t)
	event := scalingJournalEvent()
	pending, err := f.store.GetScalingWriterJournal(t.Context(), f.scope, r.Instance.ID, r.Operation.ID, "api", 1)
	if err != nil || pending.LastEvent != nil || pending.Policy.Generation != 1 {
		t.Fatalf("pending policy recovery: %+v %v", pending, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			results <- scalingTransaction(t, f, func(tx driver.Tx) error {
				_, advanceErr := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{})
				return advanceErr
			})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if countErr := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_scaling_writer_events`).Scan(&count); countErr != nil || count != 1 {
		t.Fatalf("replay rows: %d %v", count, countErr)
	}
	event.ProofID = "changed-proof"
	err = scalingTransaction(t, f, func(tx driver.Tx) error {
		_, advanceErr := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{})
		return advanceErr
	})
	if !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	recovered, err := f.store.GetScalingWriterJournal(t.Context(), f.scope, r.Instance.ID, r.Operation.ID, "api", 1)
	if err != nil || recovered.LastEvent == nil || recovered.LastEvent.ProofID != "proof-1" || recovered.LastEvent.ObservedWriter(recovered.Policy.Input.Policy) != "unknown" {
		t.Fatalf("ambiguous recovery lost identity: %+v %v", recovered, err)
	}
	wrongScope := f.scope
	wrongScope.ProjectID = "foreign-project"
	if _, err = f.store.GetScalingWriterJournal(t.Context(), wrongScope, r.Instance.ID, r.Operation.ID, "api", 1); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign journal read: %v", err)
	}
}

func TestFleetScaleWriterJournalRollbackAndRevocation(t *testing.T) {
	f, r := newScalingJournalFixture(t)
	event := scalingJournalEvent()
	rollbackErr := errors.New("domain transition rolled back")
	err := scalingTransaction(t, f, func(tx driver.Tx) error {
		if _, appendErr := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{}); appendErr != nil {
			return appendErr
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatal(err)
	}
	err = scalingTransaction(t, f, func(tx driver.Tx) error {
		_, advanceErr := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{err: fleets.ErrApprovalRequired})
		return advanceErr
	})
	if !errors.Is(err, fleets.ErrApprovalRequired) {
		t.Fatalf("revoked authority: %v", err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_scaling_writer_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("uncommitted/unverified writer event leaked: %d %v", count, err)
	}
}

func TestFleetScaleWriterJournalIdentityAndExpiry(t *testing.T) {
	f, r := newScalingJournalFixture(t)
	for _, tc := range []struct {
		name  string
		alter func(*fleets.FleetScope, *fleets.ScalingWriterEvent)
		want  error
	}{
		{"foreign scope", func(scope *fleets.FleetScope, _ *fleets.ScalingWriterEvent) { scope.OrgID = "other" }, fleets.ErrNotFound},
		{"expired receipt", func(_ *fleets.FleetScope, e *fleets.ScalingWriterEvent) { e.ExpiresAt = e.ObservedAt.Add(-time.Second) }, fleets.ErrConflict},
		{"stale receipt", func(_ *fleets.FleetScope, e *fleets.ScalingWriterEvent) {
			e.ObservedAt = e.ObservedAt.Add(-2 * time.Minute)
		}, fleets.ErrConflict},
		{"skip fencing", func(_ *fleets.FleetScope, e *fleets.ScalingWriterEvent) {
			e.Phase = "active"
			e.ProofKind = "writer_observed"
			e.WriterUID = "new-writer"
		}, fleets.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, event := f.scope, scalingJournalEvent()
			tc.alter(&scope, &event)
			err := scalingTransaction(t, f, func(tx driver.Tx) error {
				_, err := f.store.AdvanceScalingWriterTx(t.Context(), tx, scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{})
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("journal gate: %v", err)
			}
		})
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET ownership_epoch=ownership_epoch+1 WHERE id=$1`, r.Instance.ID); err != nil {
		t.Fatal(err)
	}
	err := scalingTransaction(t, f, func(tx driver.Tx) error {
		_, err := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, scalingJournalEvent(), scalingJournalTestVerifier{})
		return err
	})
	if !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("stale ownership epoch: %v", err)
	}
}

func TestFleetScalePolicyHistoryRetained(t *testing.T) {
	f, r := newScalingJournalFixture(t)
	event := scalingJournalEvent()
	if err := scalingTransaction(t, f, func(tx driver.Tx) error {
		_, advanceErr := f.store.AdvanceScalingWriterTx(t.Context(), tx, f.scope, r.Instance.ID, r.Operation.ID, "api", 1, event, scalingJournalTestVerifier{})
		return advanceErr
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE loomplane_fleet_scaling_policies SET revision=2`, `DELETE FROM loomplane_fleet_scaling_policies`, `UPDATE loomplane_fleet_scaling_writer_events SET phase='active'`, `DELETE FROM loomplane_fleet_scaling_writer_events`} {
		if _, err := f.sql.Exec(t.Context(), query); err == nil {
			t.Fatalf("history mutation accepted: %s", query)
		}
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "fleet_scaling_policy_journal" {
			if err := m.Down(t.Context(), pgmigrate.New(f.db)); err == nil {
				t.Fatal("downgrade discarded policy history")
			}
			return
		}
	}
	t.Fatal("scaling migration missing")
}
