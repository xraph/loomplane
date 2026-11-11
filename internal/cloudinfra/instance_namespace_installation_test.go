package cloudinfra

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/observability"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/security"
)

func namespaceOtherInstallation(t *testing.T, f *namespacePGFixture, installedApp, adapterApp string) *InstanceNamespaceTransactions {
	t.Helper()
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	c := &currentauthority.Coordinator{}
	router := currentauthority.NewStoreRouter(c, wardenpg.New(db))
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(currentauthority.NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Shutdown(ctx); err != nil {
			t.Error(err)
			return
		}
		if err := engine.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := currentauthority.InstallBuiltInFleetProfile(t.Context(), c, f.db, engine, f.dsn, installedApp, observability.NewTelemetry("postgresql"), nil); err != nil {
		t.Fatal(err)
	}
	a := NewInstanceNamespaceTransactions(c.FleetAuthority(), f.transactions.fleets, f.transactions.store, adapterApp)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return a
}

func TestNamespacePostgresHistoricalInstallationBinding(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	// A second real installation shares the same qualified main DB and schema.
	other := namespaceOtherInstallation(t, f, "other-app", "app")
	t.Run("historical-native", func(t *testing.T) {
		if result, runErr := other.run(t.Context(), f.action); runErr == nil || result != nil {
			t.Fatalf("foreign installation returned historical reservation: %v", runErr)
		}
	})
	t.Run("historical-public-replay", func(t *testing.T) {
		if result, _, replayErr := other.replayReservation(t.Context(), f.action.Record, f.action.Operation.Input, f.action.Principal); replayErr == nil || result != nil {
			t.Fatalf("foreign installation returned replay deniedReceipt: %v", replayErr)
		}
	})
	replay, err := f.transactions.run(t.Context(), f.action)
	if err != nil || !namespaceEqual(replay, first) {
		t.Fatalf("matching installation lost historical recovery: %v", err)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("foreign installation changed evidence: %d %v", count, err)
	}
}

func TestNamespacePostgresOutcomeInstallationBinding(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	o := namespaceCloneOperation(t, first.Operation)
	at := time.Now().UTC()
	o.Enrollment = &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "runtime-connection", TargetID: "runtime-target", ResourceRevision: 1, State: kubernetes.EnrollmentState{Attempted: map[string]bool{"Namespace": true}, Objects: map[string]string{}}}
	o.Steps[1].State = "attempted"
	o.Steps[1].AttemptedAt = &at
	o.Revision++
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &o, 1); err != nil {
		t.Fatal(err)
	}
	observed := namespaceCloneOperation(t, o)
	observed.Enrollment.State.NamespaceUID = "namespace-uid"
	other := namespaceOtherInstallation(t, f, "other-app", "app")
	for _, tc := range []struct{ name, adapterApp string }{{"adapter-installation-mismatch", "app"}, {"saved-installation-mismatch", "other-app"}} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewInstanceNamespaceTransactions(other.coordinator, other.fleets, other.store, tc.adapterApp)
			if preserveErr := adapter.preserveOutcome(t.Context(), &observed); preserveErr == nil {
				t.Error("foreign installation preserved saved app outcome")
			}
			outcomes, outcomesErr := f.transactions.store.readNamespaceOutcomes(t.Context(), o.OrgID, o.ID)
			if outcomesErr != nil || len(outcomes) != 0 {
				t.Fatalf("foreign installation changed outcomes: %v", outcomesErr)
			}
		})
	}
	// A delayed outcome remains recoverable after the original grant is revoked.
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, f.action.Record.CurrentAuthority.Request.GrantID); err != nil {
		t.Fatal(err)
	}
	if err = f.transactions.preserveOutcome(t.Context(), &observed); err != nil {
		t.Fatalf("matching installation lost delayed outcome: %v", err)
	}
	saved, err := f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID)
	if err != nil || saved.State.NamespaceUID != "namespace-uid" {
		t.Fatalf("missing original outcome: %v", err)
	}
}

func TestNamespacePostgresPublicReceiptInstallationBinding(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	owner := security.Principal{OrgID: "org", UserID: "usr"}
	query := InstanceNamespaceReceiptQuery{OperationID: first.Operation.ID, InstanceID: f.action.Snapshot.InstanceID, Generation: f.action.Snapshot.Generation, OwnershipEpoch: f.action.Snapshot.OwnershipEpoch, SnapshotDigest: f.action.Snapshot.Digest}
	service := func(a *InstanceNamespaceTransactions) *Service {
		return NewService(Deps{Clusters: f.transactions.store, InstanceNamespaces: a, InstanceAuthority: f.transactions.fleets})
	}
	matched := service(f.transactions)
	got, err := matched.GetInstanceNamespaceReceipt(t.Context(), owner, query)
	if err != nil || got == nil || got.Status != "accepted" || got.Receipt.OperationID != query.OperationID {
		t.Fatalf("matching public deniedReceipt: %v", err)
	}
	denied := owner
	denied.UserID = "denied-reader"
	if deniedReceipt, deniedErr := matched.GetInstanceNamespaceReceipt(t.Context(), denied, query); deniedErr == nil || deniedReceipt != nil {
		t.Fatal("denied public reader obtained deniedReceipt")
	}
	stale := query
	stale.SnapshotDigest = "stale"
	if staleReceipt, staleErr := matched.GetInstanceNamespaceReceipt(t.Context(), owner, stale); staleErr == nil || staleReceipt != nil {
		t.Fatal("stale public query obtained deniedReceipt")
	}
	other := namespaceOtherInstallation(t, f, "other-app", "app")
	for _, app := range []string{"app", "other-app"} {
		t.Run(app, func(t *testing.T) {
			adapter := NewInstanceNamespaceTransactions(other.coordinator, other.fleets, other.store, app)
			if foreignReceipt, foreignErr := service(adapter).GetInstanceNamespaceReceipt(t.Context(), owner, query); foreignErr == nil || foreignReceipt != nil {
				t.Fatalf("foreign public deniedReceipt returned: %v", foreignErr)
			}
		})
	}
	next := namespaceCloneOperation(t, first.Operation)
	next.Status = "needs_attention"
	next.Revision++
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &next, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, f.action.Record.CurrentAuthority.Request.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, f.action.Principal.KeyID); err != nil {
		t.Fatal(err)
	}
	got, err = matched.GetInstanceNamespaceReceipt(t.Context(), owner, query)
	if err != nil || got == nil || got.Status != "needs_attention" || got.ErrorCode != "namespace_operation_failed" || got.OperationRevision != 2 {
		t.Fatalf("public recovery status after original revocation: %v", err)
	}
}
