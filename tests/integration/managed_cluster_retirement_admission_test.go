//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
)

func retirementAdmissionFixture(t *testing.T) (*poolFixture, *cloudinfra.ClusterOperation, *deployments.Deployment) {
	t.Helper()
	f, o, d := maintenanceAdmissionFixture(t)
	// The shared pool fixture registers server ownership of runtime-1. This
	// attachment fixture represents a managed runtime, so remove that unrelated use.
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_connection_uses WHERE org_id='org' AND connection_id='runtime-1'`); err != nil {
		t.Fatal(err)
	}
	a, err := f.store.GetRuntimeAttachment(t.Context(), "org", "attached")
	if err != nil {
		t.Fatal(err)
	}
	o.ID = "retirement"
	o.Kind = "detach-runtime"
	o.Input.Kind = o.Kind
	o.Input.Maintenance = nil
	o.Input.Retirement = &cloudinfra.ClusterRetirementInput{Attachment: a}
	o.Digest = "retirement-digest"
	o.Steps = []cloudinfra.ClusterStep{{Name: "disable", State: "pending", RequestDigest: o.Digest}, {Name: "vault", State: "pending", RequestDigest: o.Digest}}
	if err := f.store.CreateClusterPreview(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	return f, o, d
}
func TestManagedClusterRetirementConcurrentDeploy(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- f.store.AcceptClusterOperation(t.Context(), o, 2) }()
	go func() {
		defer wg.Done()
		<-start
		_, err := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d)
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("want exactly one admitted, got %d", success)
	}
}
func TestManagedClusterRetirementPersistsAdmissionFence(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	if checkErr := f.store.AcceptClusterOperation(t.Context(), o, 2); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, err := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); err == nil {
		t.Fatal("restart bypassed maintenance fence")
	}
	if _, checkErr := f.store.ControlClusterOperation(t.Context(), "org", "cloud", o.ID, true); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); checkErr != nil {
		t.Fatal(checkErr)
	}
}
func TestManagedClusterRetirementDeploymentBlocksAcceptance(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	if _, checkErr := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); checkErr != nil {
		t.Fatal(checkErr)
	}
	err := f.store.AcceptClusterOperation(t.Context(), o, 2)
	if !errors.Is(err, cloudinfra.ErrBusy) {
		t.Fatal(err)
	}
}

func TestManagedClusterRetirementLeavesUnrelatedTargetsAvailable(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,connection_id}','"runtime-2"') WHERE id=$1`, d.PlanID); err != nil {
		t.Fatal(err)
	}
	if checkErr := f.store.AcceptClusterOperation(t.Context(), o, 2); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); checkErr != nil {
		t.Fatalf("static target blocked by unrelated maintenance: %v", checkErr)
	}
}

func TestManagedClusterRetirementDefaultConnectionFence(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result#-'{actions,0,connection_id}','{connections}','{"server":"runtime-1"}') WHERE id=$1`, d.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); err == nil {
		t.Fatal("default managed connection bypassed fence")
	}
}

func TestManagedClusterRetirementPreservesLegacyEmptyPlan(t *testing.T) {
	f, o, d := retirementAdmissionFixture(t)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result='{"actions":null}' WHERE id=$1`, d.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); err != nil {
		t.Fatalf("legacy empty plan rejected: %v", err)
	}
}

func TestManagedClusterRetirementPlainKeepDoesNotFence(t *testing.T) {
	for _, maintenanceFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(maintenanceFirst), func(t *testing.T) {
			f, o, d := retirementAdmissionFixture(t)
			if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions}','[{"id":"service:kept","name":"kept","subject":"service","op":"keep","connection_id":"runtime-1"},{"id":"service:changed","name":"changed","subject":"service","op":"update","connection_id":"runtime-2"}]') WHERE id=$1`, d.PlanID); err != nil {
				t.Fatal(err)
			}
			if maintenanceFirst {
				if err := f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); err != nil {
				t.Fatal("plain keep blocked unrelated deployment", err)
			}
			if !maintenanceFirst {
				if err := f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
					t.Fatal("plain keep blocked maintenance", err)
				}
			}
		})
	}
}
func TestManagedClusterRetirementRetryCapacityLockOrder(t *testing.T) {
	f, _, d := retirementAdmissionFixture(t)
	d.Status = deployments.StatusFailed
	st := deployments.NewPgStore(f.db)
	if _, err := st.CreateDeployment(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	tx, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var holder int
	if err = tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, reopenErr := st.Reopen(t.Context(), "org", d.ID); done <- reopenErr }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND $1=ANY(pg_blocking_pids(pid)))`, holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not reach organization gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, lockErr := tx.Exec(t.Context(), `SELECT id FROM loomplane_deployments WHERE id=$1 FOR UPDATE NOWAIT`, d.ID)
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if lockErr != nil {
		t.Fatalf("retry held row while waiting for capacity organization gate: %v", lockErr)
	}
}

func TestManagedClusterRetirementCheckpointRejectsConcurrentTrustChange(t *testing.T) {
	f, o, _ := retirementAdmissionFixture(t)
	if err := f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
		t.Fatal(err)
	}
	leased, err := f.store.LeaseClusterOperation(t.Context(), "org", o.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	current.Observation.CAHash = "new-trust"
	current.Revision++
	if _, err = f.store.ClaimManagedResource(t.Context(), current, current.Revision-1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stale.DeletedAt = &now
	stale.Status = "deleted"
	stale.Revision++
	expected := leased.Revision
	leased.Revision++
	leased.Status = "succeeded"
	if err = f.store.CheckpointClusterResource(t.Context(), leased, stale, expected); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale tombstone overwrote current trust", err)
	}
	saved, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeletedAt != nil || saved.Observation.CAHash != "new-trust" {
		t.Fatal("current trust lost", saved)
	}
	savedOp, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if savedOp.Status != "running" || savedOp.Revision != expected {
		t.Fatal("failed CAS partially checkpointed", savedOp)
	}
}
