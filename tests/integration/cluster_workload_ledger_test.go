//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cppostgres "github.com/xraph/ctrlplane/store/postgres"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/clouds/owned"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/provider"
)

func clusterWorkloadFixture(t *testing.T) (*poolFixture, *cloudinfra.ManagedResource, cloudinfra.ClusterWorkloadInput, kube.ControllerReceipt) {
	t.Helper()
	f := newPoolFixture(t)
	now := time.Now().UTC()
	ca := strings.Repeat("a", 64)
	c := &connections.Connection{ID: "cluster-runtime", OrgID: "org", Cloud: "kubernetes", Method: connections.MethodManagedKubernetes, Name: "cluster-runtime", Status: "ready", Details: map[string]string{"namespace": "apps", "namespace_uid": "namespace", "cluster_ca": ca, "managed_attachment_id": "attachment", "managed_resource_id": "cluster"}, CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "region", Incarnation: "birth"}
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "runtime-1", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "external", Status: "running", Revision: 1, Observation: &provider.ClusterObservation{Ref: ref}}
	if _, err := f.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	a := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", CloudID: "cloud", ClusterID: r.ID, ProjectID: "proj", Namespace: "apps", NamespaceUID: "namespace", ServiceAccountUID: "account", RuntimeConnectionID: c.ID, TrustFingerprint: ca, TrustRevision: 1, Revision: 1}
	if err := f.store.SaveRuntimeAttachment(t.Context(), a, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "cluster-target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "cluster", DisplayName: "Cluster", Provider: "kubernetes", Runtime: "managed", ManagedResourceID: r.ID, ConnectionID: c.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	futurePlan(t, f, "workload", "env")
	acceptFuture(t, f, "workload")
	in := cloudinfra.ClusterWorkloadInput{ID: "hold", OrgID: "org", CloudID: "cloud", ResourceID: r.ID, TargetID: "cluster-target", AttachmentID: a.ID, ProjectID: "proj", EnvironmentID: "env", ConnectionID: c.ID, PlanID: "workload", Service: "api", ActionID: "service:api", RuntimeRef: "org/env/api/runtime_attempt", RuntimeRevision: "deployment/service:api", ReviewDigest: "review", TrustRevision: 1, Maximum: cloudinfra.ClusterWorkloadVector{CPUMillis: 1000, MemoryBytes: 1000, Pods: 2}}
	in.ReleaseID = kube.WorkloadReleaseID(in.RuntimeRef, in.RuntimeRevision)
	receipt := kube.ControllerReceipt{Scope: domain.Scope{OrgID: in.OrgID, ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID}, ConnectionID: c.ID, InstanceID: id.New(id.PrefixInstance).String(), PlanID: in.PlanID, RuntimeRef: in.RuntimeRef, ServiceID: in.Service, Container: "app", Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Namespace: "apps", NamespaceUID: a.NamespaceUID, ClusterCA: ca, ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 1, RuntimeRevision: in.RuntimeRevision, ReleaseID: in.ReleaseID}
	clusterWorkloadSnapshot(t, f, r, nil)
	in.RuntimeRef, in.RuntimeRevision, in.ReleaseID = "", "", ""
	return f, r, in, receipt
}
func clusterWorkloadSnapshot(t *testing.T, f *poolFixture, r *cloudinfra.ManagedResource, pods []cloudinfra.ClusterObservedWorkload) {
	t.Helper()
	revision, err := f.store.BeginClusterCapacityObservation(t.Context(), r.OrgID, r.CloudID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: revision, CheckedAt: time.Now().UTC(), Complete: true, WorkloadsComplete: true, Workloads: pods, Nodes: []cloudinfra.ClusterNodeCapacity{{ExternalVMID: "vm", NodeUID: "node", PoolID: "pool", Ready: true, Provisioned: cloudinfra.CapacityResources{}, Allocatable: cloudinfra.ClusterResourceMetrics{CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(4000), EphemeralStorageBytes: inventoryNumber(10000), Pods: inventoryNumber(10)}}}}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err != nil {
		t.Fatal(err)
	}
}
func clusterWorkloadGuard(_ context.Context, _ driver.Tx, in cloudinfra.ClusterWorkloadInput) error {
	if in.ReviewDigest != "review" {
		return cloudinfra.ErrConflict
	}
	return nil
}
func workloadTx(t *testing.T, f *poolFixture, fn func(driver.Tx) error) error {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func TestClusterWorkloadLedgerHandover(t *testing.T) {
	f, r, in, receipt := clusterWorkloadFixture(t)
	reserve := func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, in, clusterWorkloadGuard)
		return err
	}
	if err := workloadTx(t, f, reserve); err != nil {
		t.Fatal(err)
	}
	if err := workloadTx(t, f, reserve); err != nil {
		t.Fatal("replay", err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: in.OrgID, Project: in.ProjectID, Environment: in.EnvironmentID, Service: in.Service, Plan: in.PlanID, Deployment: "deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = workloadTx(t, f, func(tx driver.Tx) error { return f.store.AttachClusterWorkloadAttempt(t.Context(), tx, in, *attempt) }); err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); err != nil {
		t.Fatal(err)
	}
	if err = kube.NewControllerStore(f.db).RecordController(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,revision,plan_id,created_at,updated_at) VALUES('cluster-binding','org','proj','env','service','api','kubernetes','ctrlplane',$1,$2,$3,$4,now(),now())`, receipt.InstanceID, in.ConnectionID, receipt.Image, in.PlanID); err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(pgmigrate.New(f.db), cppostgres.Migrations).Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	labels, _ := json.Marshal(map[string]string{owned.LabelOrg: in.OrgID, owned.LabelProject: in.ProjectID, owned.LabelEnvironment: in.EnvironmentID, owned.LabelConnection: in.ConnectionID, owned.LabelService: in.Service, owned.LabelPlan: in.PlanID, owned.LabelRuntime: receipt.RuntimeRef, owned.LabelRevision: receipt.RuntimeRevision})
	services, _ := json.Marshal([]map[string]string{{"name": receipt.Container, "image": receipt.Image}})
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name,kind,services,labels) VALUES($1,$2,'api','api','running',$3,'deployment',$4,$5)`, receipt.InstanceID, in.OrgID, "kubernetes."+in.ConnectionID, services, labels); err != nil {
		t.Fatal(err)
	}
	if err = workloadTx(t, f, func(tx driver.Tx) error {
		return f.store.BindClusterWorkloadReceipt(t.Context(), tx, in, "cluster-binding")
	}); err != nil {
		t.Fatal(err)
	}
	pod := cloudinfra.ClusterObservedWorkload{PodUID: "pod", NamespaceUID: receipt.NamespaceUID, DeploymentUID: receipt.DeploymentUID, DeploymentGeneration: 1, ReplicaSetUID: "rs", ReleaseID: receipt.ReleaseID, Requests: cloudinfra.ClusterWorkloadVector{CPUMillis: 500, MemoryBytes: 500, Pods: 1}}
	external := cloudinfra.ClusterObservedWorkload{PodUID: "external", Requests: cloudinfra.ClusterWorkloadVector{CPUMillis: 200, MemoryBytes: 200, Pods: 1}}
	for _, mode := range []string{"partial", "repeat", "old-release", "wrong-namespace", "wrong-uid", "old-generation", "instance-runtime-replaced", "trust-replaced"} {
		t.Run(mode, func(t *testing.T) {
			current := pod
			want := int64(1200)
			switch mode {
			case "old-release":
				current.ReleaseID = "old"
				want = 1700
			case "wrong-namespace":
				current.NamespaceUID = "wrong"
				want = 1700
			case "wrong-uid":
				current.DeploymentUID = "wrong"
				want = 1700
			case "old-generation":
				current.DeploymentGeneration = 0
				want = 1700
			case "instance-runtime-replaced":
				if _, updateErr := f.sql.Exec(t.Context(), `UPDATE cp_instances SET labels=jsonb_set(labels,'{loomplane.runtime.ref}','"org/env/api/runtime_other"') WHERE id=$1`, receipt.InstanceID); updateErr != nil {
					t.Fatal(updateErr)
				}
				defer func() {
					if _, restoreErr := f.sql.Exec(t.Context(), `UPDATE cp_instances SET labels=$2 WHERE id=$1`, receipt.InstanceID, labels); restoreErr != nil {
						t.Error(restoreErr)
					}
				}()
				want = 1700
			case "trust-replaced":
				newer := receipt
				newer.DeploymentGeneration++
				newer.RuntimeRevision = "new-release"
				newer.ReleaseID = kube.WorkloadReleaseID(newer.RuntimeRef, newer.RuntimeRevision)
				if err := kube.NewControllerStore(f.db).RecordController(t.Context(), newer); err != nil {
					t.Fatal(err)
				}
				want = 1700
			}
			clusterWorkloadSnapshot(t, f, r, []cloudinfra.ClusterObservedWorkload{current, external})
			// A new store reads durable holds instead of retaining in-memory credit.
			reopened := pgdriver.New()
			if openErr := reopened.Open(t.Context(), f.sql.Config().ConnString()); openErr != nil {
				t.Fatal(openErr)
			}
			defer func() { _ = reopened.Close() }()
			restarted := cloudinfra.NewPgStore(reopened)
			err := workloadTx(t, f, func(_ driver.Tx) error {
				freshTx, beginErr := reopened.BeginTx(t.Context(), nil)
				if beginErr != nil {
					return beginErr
				}
				defer func() { _ = freshTx.Rollback() }()
				usage, _, e := restarted.ClusterWorkloadUsage(t.Context(), freshTx, in)
				if e == nil && usage.CPUMillis != want {
					t.Fatalf("usage=%+v want CPU %d", usage, want)
				}
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClusterWorkloadLedgerUnstartedRelease(t *testing.T) {
	for _, mode := range []string{"settled", "missing", "active", "effects", "stale-attempt", "guard-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, _, in, _ := clusterWorkloadFixture(t)
			if err := workloadTx(t, f, func(tx driver.Tx) error {
				_, err := f.store.ReserveClusterWorkload(t.Context(), tx, in, clusterWorkloadGuard)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if mode != "missing" {
				a, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: in.OrgID, Project: in.ProjectID, Environment: in.EnvironmentID, Service: in.Service, Plan: in.PlanID, Deployment: "deployment"}, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				if err = workloadTx(t, f, func(tx driver.Tx) error { return f.store.AttachClusterWorkloadAttempt(t.Context(), tx, in, *a) }); err != nil {
					t.Fatal(err)
				}
				if mode == "effects" {
					if err = f.store.MarkWorkloadEffects(t.Context(), *a); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "active" {
					if err = f.store.FinishWorkloadAttempt(t.Context(), *a, true); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "stale-attempt" {
					if _, err = f.store.BeginWorkloadAttempt(t.Context(), *a, nil, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			guard := clusterWorkloadGuard
			if mode == "guard-denied" {
				guard = func(context.Context, driver.Tx, cloudinfra.ClusterWorkloadInput) error { return cloudinfra.ErrConflict }
			}
			err := workloadTx(t, f, func(tx driver.Tx) error { return f.store.ReleaseUnstartedClusterWorkload(t.Context(), tx, in, guard) })
			if (err == nil) != (mode == "settled") {
				t.Fatalf("release mode %s: %v", mode, err)
			}
			var released bool
			if err = f.sql.QueryRow(t.Context(), `SELECT released FROM loomplane_cluster_workload_holds WHERE id=$1`, in.ID).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if released != (mode == "settled") {
				t.Fatal("incorrect durable release", released)
			}
		})
	}
}

func TestClusterWorkloadLedgerAdmission(t *testing.T) {
	f, r, in, _ := clusterWorkloadFixture(t)
	// An external pending pod consumes capacity even without an owner chain.
	clusterWorkloadSnapshot(t, f, r, []cloudinfra.ClusterObservedWorkload{{PodUID: "external", Requests: cloudinfra.ClusterWorkloadVector{CPUMillis: 1000, Pods: 1}}})
	if err := workloadTx(t, f, func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, in, clusterWorkloadGuard)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other := in
	other.ID = "second"
	futurePlan(t, f, "second-workload", "env")
	acceptFuture(t, f, "second-workload")
	other.PlanID = "second-workload"
	if err := workloadTx(t, f, func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, other, clusterWorkloadGuard)
		return err
	}); err == nil {
		t.Fatal("unmatched pod and hold exceeded supply")
	}
	if err := workloadTx(t, f, func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, in, nil)
		return err
	}); err == nil {
		t.Fatal("missing reviewed demand guard accepted")
	}
	changed := in
	changed.Maximum.CPUMillis = 500
	if err := workloadTx(t, f, func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, changed, clusterWorkloadGuard)
		return err
	}); err == nil {
		t.Fatal("hold maximum changed on retry")
	}
	forged := in
	forged.ActionID = "service:other"
	if err := workloadTx(t, f, func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, forged, clusterWorkloadGuard)
		return err
	}); err == nil {
		t.Fatal("caller-invented action accepted")
	}
	// Starting a newer observation invalidates old data until it completes.
	if _, err := f.store.BeginClusterCapacityObservation(t.Context(), in.OrgID, in.CloudID, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := workloadTx(t, f, func(tx driver.Tx) error { _, _, err := f.store.ClusterWorkloadUsage(t.Context(), tx, in); return err }); err == nil {
		t.Fatal("stale observation admitted workload")
	}
}

func TestClusterWorkloadLedgerSharedSupplyRace(t *testing.T) {
	f, _, in, _ := clusterWorkloadFixture(t)
	in.Maximum.CPUMillis = 1500
	other := in
	other.ID = "competing"
	futurePlan(t, f, "second-workload", "env")
	acceptFuture(t, f, "second-workload")
	other.PlanID = "second-workload"
	second := pgdriver.New()
	if err := second.Open(t.Context(), f.sql.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	start := make(chan struct{})
	result := make(chan error, 2)
	for index, input := range []cloudinfra.ClusterWorkloadInput{in, other} {
		db := f.db
		if index == 1 {
			db = second
		}
		go func() {
			<-start
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				result <- err
				return
			}
			defer func() { _ = tx.Rollback() }()
			_, err = f.store.ReserveClusterWorkload(t.Context(), tx, input, clusterWorkloadGuard)
			if err == nil {
				err = tx.Commit()
			}
			result <- err
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		if err := <-result; err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("atomic shared supply admitted %d winners", winners)
	}
}
