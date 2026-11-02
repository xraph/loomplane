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
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/pkg/provider"
)

func maintenanceAdmissionFixture(t *testing.T) (*poolFixture, *cloudinfra.ClusterOperation, *deployments.Deployment) {
	t.Helper()
	f := newPoolFixture(t)
	now := time.Now().UTC()
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), &connections.Connection{ID: "managed-account", OrgID: "org", Cloud: "digitalocean", Name: "Managed", Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "managed-account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, ObservedRevision: 1, Observation: &provider.ClusterObservation{Ref: ref}, CreationReceipt: &provider.ClusterMutation{Kind: "create", Ref: ref}}
	if _, checkErr := f.store.ClaimManagedResource(t.Context(), r, 0); checkErr != nil {
		t.Fatal(checkErr)
	}
	a := &cloudinfra.RuntimeAttachment{ID: "attached", OrgID: "org", CloudID: "cloud", ClusterID: "cluster", ProjectID: "proj", Namespace: "ns", NamespaceUID: "ns-uid", ServiceAccountUID: "sa", RuntimeConnectionID: "runtime-1", Revision: 1}
	if checkErr := f.store.SaveRuntimeAttachment(t.Context(), a, 0); checkErr != nil {
		t.Fatal(checkErr)
	}
	in := cloudinfra.ClusterInput{Kind: "cluster-upgrade", Provider: "digitalocean", ConnectionID: "managed-account", ConnectionGeneration: 1, AccountID: "account", CloudRevision: 1, ResourceID: "cluster", ResourceRevision: 1, Ref: ref, Maintenance: &cloudinfra.ClusterMaintenanceInput{ObservedRevision: 1, Version: "v2", DrainDigest: "review"}}
	o := &cloudinfra.ClusterOperation{ID: "maintenance", OrgID: "org", CloudID: "cloud", Kind: in.Kind, SchemaVersion: 1, Input: in, Digest: "immutable", Revision: 1, Status: "preview", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "pending", RequestDigest: "immutable"}, {Name: "enrollment", State: "pending", RequestDigest: "immutable"}}}
	if checkErr := f.store.CreateClusterPreview(t.Context(), o); checkErr != nil {
		t.Fatal(checkErr)
	}
	plan := futurePlan(t, f, "maintenance-deploy", "env")
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,connection_id}','"runtime-1"') WHERE id=$1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	d := &deployments.Deployment{ID: "deployment", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: plan.ID, Status: deployments.StatusRunning, StartedAt: now, StartedBy: "usr"}
	return f, o, d
}
func TestManagedClusterMaintenanceConcurrentDeploy(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
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
func TestManagedClusterMaintenancePersistsAdmissionFence(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
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
func TestManagedClusterMaintenanceDeploymentBlocksAcceptance(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
	if _, checkErr := deployments.NewPgStore(f.db).CreateDeployment(t.Context(), d); checkErr != nil {
		t.Fatal(checkErr)
	}
	err := f.store.AcceptClusterOperation(t.Context(), o, 2)
	var blocked *cloudinfra.ClusterMaintenanceAdmissionError
	if !errors.As(err, &blocked) || blocked.Reason != "deployment_in_flight" {
		t.Fatal(err)
	}
}

type guardedManagementProvider struct{ *enrollmentKindProvider }

func (p guardedManagementProvider) BootstrapCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, lifetime time.Duration) (provider.BootstrapAccess, error) {
	if a.BeforeEffect == nil {
		return provider.BootstrapAccess{}, errors.New("missing management effect guard")
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.BootstrapAccess{}, err
	}
	return p.enrollmentKindProvider.BootstrapCluster(ctx, a, ref, lifetime)
}
func TestManagedClusterMaintenanceManagementObservation(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, _ := f.service(f.store, f.vault())
	if checkErr := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); checkErr != nil {
		t.Fatal(checkErr)
	}
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	old := r.Revision
	r.Revision++
	r.Ownership = "managed"
	r.CreationReceipt = &provider.ClusterMutation{Kind: "create", Ref: r.Observation.Ref}
	if _, checkErr := f.store.ClaimManagedResource(t.Context(), r, old); checkErr != nil {
		t.Fatal(checkErr)
	}
	adapters := cloudinfra.NewAdapters()
	if registerErr := adapters.RegisterCluster("digitalocean", guardedManagementProvider{f.provider}, provider.ClusterBootstrapAccess); registerErr != nil {
		t.Fatal(registerErr)
	}
	svc.Adapters = adapters
	review, err := svc.PreviewClusterDrain(t.Context(), f.principal, "cloud", "cluster", "")
	if err != nil {
		t.Fatal(err)
	}
	if review.Digest == "" || len(review.Workloads) == 0 || len(review.Blockers) == 0 {
		t.Fatalf("kind management facts missing: %+v", review)
	}
	var attempts int
	if checkErr := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_operation_steps WHERE name='management-access' AND state='completed' AND body->>'attempted_at' IS NOT NULL`).Scan(&attempts); checkErr != nil {
		t.Fatal(checkErr)
	}
	if attempts != 1 {
		t.Fatalf("management issuance marker: %d", attempts)
	}
}

func TestClusterUpgradeConcurrentDeploy(t *testing.T) {
	TestManagedClusterMaintenanceConcurrentDeploy(t)
}

func TestManagedClusterMaintenanceLeavesUnrelatedTargetsAvailable(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
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

func TestManagedClusterMaintenanceDefaultConnectionFence(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
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

func TestManagedClusterMaintenancePreservesLegacyEmptyPlan(t *testing.T) {
	f, o, d := maintenanceAdmissionFixture(t)
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

func TestManagedClusterMaintenancePlainKeepDoesNotFence(t *testing.T) {
	for _, maintenanceFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(maintenanceFirst), func(t *testing.T) {
			f, o, d := maintenanceAdmissionFixture(t)
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
func TestManagedClusterMaintenanceRetryCapacityLockOrder(t *testing.T) {
	f, _, d := maintenanceAdmissionFixture(t)
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
