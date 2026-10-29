//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"

	"github.com/xraph/loomplane/pkg/provider"

	"github.com/google/uuid"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func TestClusterCapacityInventoryOrdering(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: first, CheckedAt: time.Now().UTC(), Complete: true}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("late sample accepted", err)
	}
	snapshot.ObservationRevision = second
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err != nil {
		t.Fatal(err)
	}
	generation, err := f.store.BeginCapacityRefresh(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(t.Context(), "org", "cloud", generation, nil, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_capacity_inventory WHERE body IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatal("server refresh erased cluster", count, err)
	}

	oldRevision := r.Revision
	r.Revision++
	r.Observation.CAHash = "changed-trust"
	if _, err = f.store.ClaimManagedResource(t.Context(), r, oldRevision); err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Fresh || usage.Provisioned.Servers != nil {
		t.Fatal("changed trust retained usable capacity")
	}
	third, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ObservationRevision = third
	snapshot.CheckedAt = snapshot.CheckedAt.Add(-time.Minute)
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("time reversal accepted", err)
	}
}
func TestClusterCapacityInventoryObserverRBAC(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	name := "capacity-" + uuid.NewString()[:8]
	_, err := f.bootstrap.Kube.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.bootstrap.Kube.CoreV1().Namespaces().Delete(t.Context(), name, metav1.DeleteOptions{})
		_ = f.bootstrap.Kube.RbacV1().ClusterRoleBindings().Delete(t.Context(), name, metav1.DeleteOptions{})
		_ = f.bootstrap.Kube.RbacV1().ClusterRoles().Delete(t.Context(), name, metav1.DeleteOptions{})
	})
	_, err = f.bootstrap.Kube.CoreV1().ServiceAccounts(name).Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "observer"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.bootstrap.Kube.CoreV1().ServiceAccounts(name).CreateToken(t.Context(), "observer", &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	access := f.provider.access
	cfg := &rest.Config{Host: access.Endpoint, BearerToken: token.Status.Token, TLSClientConfig: rest.TLSClientConfig{CAData: access.CAData}}
	client, err := kubeclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := kubernetes.ReadCapacityInventory(t.Context(), client)
	if err == nil || facts.Complete {
		t.Fatal("unprivileged credential listed nodes")
	}
	_, err = f.bootstrap.Kube.RbacV1().ClusterRoles().Create(t.Context(), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: kubernetes.CapacityObserverRules()}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.bootstrap.Kube.RbacV1().ClusterRoleBindings().Create(t.Context(), &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "observer", Namespace: name}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	facts, err = kubernetes.ReadCapacityInventory(t.Context(), client)
	if err != nil || !facts.Complete || len(facts.Nodes) == 0 {
		t.Fatal("management read role failed", err)
	}
	if err = client.CoreV1().Nodes().Delete(t.Context(), facts.Nodes[0].Name, metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}); err == nil {
		t.Fatal("observer can delete nodes")
	}
}

func TestClusterCapacityInventoryRejectsInvalidFacts(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: revision, CheckedAt: time.Now().UTC(), Complete: true, Nodes: []cloudinfra.ClusterNodeCapacity{{ExternalVMID: "42", NodeUID: "uid", PoolID: "pool", Provisioned: cloudinfra.CapacityResources{CPUMillis: &negative}}}}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err == nil {
		t.Fatal("negative physical capacity entered durable ledger")
	}
}

type clusterInventoryAccount struct{ provider.ServerAdapter }

func (clusterInventoryAccount) CapacityInventory(_ context.Context, c provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	cpu, memory, disk, volume := int64(2000), int64(4096), int64(8192), int64(16384)
	return provider.CapacityInventory{Provider: c.Provider, AccountID: c.AccountID, CheckedAt: time.Now().UTC(), Servers: []provider.CapacityServer{{ID: "42", CPUMillis: &cpu, MemoryBytes: &memory, StorageBytes: &disk}}, Volumes: []provider.CapacityVolume{{ID: "durable", StorageBytes: &volume, ServerIDs: []string{"42"}}}}, nil
}
func TestClusterCapacityInventoryRefreshPreservesReservation(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	_, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES('org','cloud',$1,1,'test',1,2000,4096,8192,'reserved',jsonb_build_object('operation_id',$1::text,'cluster_operation_id',$1::text,'provider','digitalocean','account_id','account','delta',jsonb_build_object('servers',1,'cpu_millis',2000,'memory_bytes',4096,'storage_bytes',8192)))`, f.op.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := f.service(f.store, f.vault())
	svc.Store = f.store
	if err = svc.Adapters.RegisterServer("digitalocean", clusterInventoryAccount{}, provider.ServerDiscovery); err != nil {
		t.Fatal(err)
	}
	usage, err := svc.RefreshCapacity(t.Context(), f.principal, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Reserved.Servers == nil || *usage.Reserved.Servers != 1 || usage.Fresh || usage.Provisioned.Servers != nil || usage.Allocatable.CPUMillis != nil || len(usage.Accounts) != 1 {
		t.Fatalf("reservation or unknown cluster lost: %+v", usage)
	}
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	servers, cpu, memory, disk := int64(1), int64(2000), int64(4096), int64(8192)
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: revision, CheckedAt: time.Now().UTC(), Complete: true, Nodes: []cloudinfra.ClusterNodeCapacity{{ExternalVMID: "42", NodeUID: "uid", PoolID: "pool", Provisioned: cloudinfra.CapacityResources{Servers: &servers, CPUMillis: &cpu, MemoryBytes: &memory, StorageBytes: &disk}}}}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err != nil {
		t.Fatal(err)
	}
	usage, err = svc.RefreshCapacity(t.Context(), f.principal, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Provisioned.StorageBytes == nil || *usage.Provisioned.StorageBytes != 24576 || *usage.Provisioned.Servers != 1 {
		t.Fatalf("cluster durable volume attribution failed: %+v", usage)
	}
	var status string
	if err = f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id=$1`, f.op.ID).Scan(&status); err != nil || status != "reserved" {
		t.Fatal("cluster hold released", status, err)
	}
}

type clusterCapacityKindReader struct{ *enrollmentKindProvider }

func (p clusterCapacityKindReader) ObserveClusterCapacity(_ context.Context, _ provider.AccountAccess, ref provider.ClusterRef) (provider.ProviderClusterCapacity, error) {
	return provider.ProviderClusterCapacity{Provider: "digitalocean", Ref: ref, CheckedAt: time.Now().UTC(), Complete: true}, nil
}
func TestClusterCapacityInventoryAuthorizedObservation(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, _ := f.service(f.store, f.vault())
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", clusterCapacityKindReader{f.provider}, provider.ClusterDiscovery, provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = adapters
	snapshot, err := svc.ObserveClusterCapacity(t.Context(), f.principal, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	// kind nodes have no DigitalOcean mapping, so successful reads still leave capacity unknown.
	if snapshot.Complete || snapshot.ObservationRevision != 1 {
		t.Fatalf("missing provider mapping accepted: %+v", snapshot)
	}
	var attempts int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_operations WHERE body->>'kind'='management-observation' AND body->'steps'->0->>'state'='completed'`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("missing durable management issuance", attempts, err)
	}
}

func clusterCapacityInfraSnapshot(t *testing.T, f *infraFixture, vm string) *cloudinfra.ManagedResource {
	t.Helper()
	ref := provider.ClusterRef{ID: "physical-cluster", AccountID: "account", Region: "region", Incarnation: "birth"}
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra-account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "external", Status: "running", Revision: 1, SchemaVersion: 1, Observation: &provider.ClusterObservation{Ref: ref, Endpoint: "https://cluster.test", CAHash: "ca", State: "running"}}
	if _, err := f.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	revision, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	resources := cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(10_000_000_000)}
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: ref, ObservationRevision: revision, CheckedAt: time.Now().UTC(), Complete: true, Nodes: []cloudinfra.ClusterNodeCapacity{{ExternalVMID: vm, NodeUID: "uid", PoolID: "node-pool", Ready: true, Provisioned: resources, Allocatable: cloudinfra.ClusterResourceMetrics{CPUMillis: inventoryNumber(1500), MemoryBytes: inventoryNumber(1 << 30), EphemeralStorageBytes: inventoryNumber(10_000_000_000)}, GPUKinds: map[string]int64{"nvidia.com/gpu": 1}}}}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestClusterCapacityInventoryPreservesServerAccounting(t *testing.T) {
	f := ledgerFixture(t)
	server, record := ledgerHost(t, f, "shared", 2000)
	server.RuntimeConnectionID = "runtime-shared"
	if err := f.connections.CreateConnection(t.Context(), &connections.Connection{ID: server.RuntimeConnectionID, OrgID: "org", Cloud: "server", Name: "runtime-shared", Account: "runtime-shared", Status: "ready", Details: map[string]string{"docker_id": "runtime-shared"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	server.Health = cloudinfra.RuntimeHealth{DockerID: "runtime-shared", EffectiveCPUMillis: 1000, EffectiveMemoryBytes: 1 << 30, GPURuntime: provider.GPUStatusNone, CheckedAt: time.Now()}
	if err := f.store.SaveServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	record.ServerStateRevision = server.StateRevision
	record.GPUs = inventoryNumber(0)
	record.Observation = &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(1000), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(10_000_000_000), FreeStorageBytes: inventoryNumber(8_000_000_000), CheckedAt: time.Now()}
	pool := &cloudinfra.Pool{ID: "shared-pool", OrgID: "org", CloudID: "cloud", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: server.ID, CPUMillis: 800, MemoryBytes: 1 << 30}}}
	if err := f.store.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	policy.Pools = []cloudinfra.PoolExpansionPolicy{{PoolID: pool.ID, MaxServers: 4, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 200, MemoryBytes: 128 << 20}}}
	if err = f.store.SaveCapacityPolicy(t.Context(), policy, 1); err != nil {
		t.Fatal(err)
	}
	ledgerSave(t, f, []cloudinfra.CapacityRecord{record}, nil)
	clusterCapacityInfraSnapshot(t, f, server.ResourceID)
	usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Provisioned.Servers == nil || *usage.Provisioned.Servers != 1 || *usage.Provisioned.CPUMillis != 2000 || usage.Observed.CPUMillis == nil || *usage.Observed.CPUMillis != 1000 || *usage.SystemReserve.CPUMillis != 200 || *usage.Allocatable.CPUMillis != 600 {
		t.Fatalf("independent server accounting lost: %+v", usage)
	}
	if len(usage.Records) != 1 || usage.Records[0].ClusterNode == nil || usage.Records[0].ClusterNode.Allocatable.CPUMillis == nil || *usage.Records[0].ClusterNode.Allocatable.CPUMillis != 1500 {
		t.Fatal("cluster facts lost or VM duplicated")
	}
}
func TestClusterCapacityInventoryGPUAdmission(t *testing.T) {
	for _, overlap := range []bool{true, false} {
		t.Run(fmt.Sprint(overlap), func(t *testing.T) {
			f := gpuCeilingFixture(t)
			vm := "h100-host"
			if !overlap {
				vm = "cluster-only"
			}
			clusterCapacityInfraSnapshot(t, f, vm)
			usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			if len(usage.GPUs) != 1 || usage.GPUs[0].Kind != "nvidia-h100-80gb" {
				t.Fatal("hardware kind fabricated or lost", usage.GPUs)
			}
			if overlap {
				if usage.GPUs[0].Provisioned == nil || *usage.GPUs[0].Provisioned != 1 {
					t.Fatal("known physical GPU count lost", usage.GPUs)
				}
			} else {
				if usage.GPUs[0].Provisioned != nil || usage.Observed.CPUMillis != nil || usage.ObservedStorageBytes != nil {
					t.Fatal("cluster-only unmeasured GPU or usage became known", usage)
				}
			}
			block := ensureBlock(t, f, h100Demand(f, t, "cluster"))
			want := "hard_limit"
			if !overlap {
				want = "stale_inventory"
			}
			if block.Reason != want || block.Resource != "gpus:nvidia-h100-80gb" {
				t.Fatalf("GPU ceiling bypass: %+v", block)
			}
		})
	}
}

type clusterCapacityDriftReader struct {
	provider.ClusterReader
	observation provider.ClusterObservation
	err         error
}

func (r clusterCapacityDriftReader) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return r.observation, r.err
}
func TestClusterCapacityInventoryDriftInvalidatesAdmission(t *testing.T) {
	for _, state := range []string{"missing", "identity_changed", "benign"} {
		t.Run(state, func(t *testing.T) {
			f := ledgerFixture(t)
			policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			policy.Revision++
			policy.Limits.CPUMillis = 10000
			if err = f.store.SaveCapacityPolicy(t.Context(), policy, 1); err != nil {
				t.Fatal(err)
			}
			r := clusterCapacityInfraSnapshot(t, f, "cluster-only")
			initialUsage, initialErr := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
			if initialErr != nil || initialUsage.Observed.CPUMillis != nil || initialUsage.Observed.MemoryBytes != nil || initialUsage.ObservedStorageBytes != nil {
				t.Fatal("cluster-only unmeasured observation became zero", initialErr)
			}
			nextRevision, issueErr := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
			if issueErr != nil {
				t.Fatal(issueErr)
			}
			observation := *r.Observation
			readErr := error(nil)
			switch state {
			case "missing":
				readErr = provider.ErrInfrastructureNotFound
			case "identity_changed":
				observation.Ref.Incarnation = "replacement"
			case "benign":
				observation.Version = "new-observed-version"
			}
			if err = f.svc.Adapters.RegisterCluster("digitalocean", clusterCapacityDriftReader{observation: observation, err: readErr}, provider.ClusterDiscovery); err != nil {
				t.Fatal(err)
			}
			f.svc.Clusters = f.store
			f.svc.ManagedClusters.Enabled = true
			f.svc.Authz = enrollmentServiceAuth{}
			current, err := f.svc.ObserveClusterDrift(t.Context(), f.p, "cloud", r.ID)
			if err != nil {
				t.Fatal(err)
			}
			usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			if state == "benign" {
				if !usage.Fresh || usage.Provisioned.Servers == nil {
					t.Fatal("benign observation invalidated physical facts")
				}
				return
			}
			if current.Status != state || usage.Fresh || usage.Provisioned.Servers != nil {
				t.Fatal("known missing/replaced cluster remained usable", current.Status, usage)
			}
			op := ledgerPreview(t, f)
			err = f.store.Accept(t.Context(), op)
			var blocked *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &blocked) || blocked.Block.Reason != "stale_inventory" || blocked.Block.Resource != "inventory" {
				t.Fatalf("shared admission accepted invalid cluster: %v", err)
			}
			snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: current.ID, Provider: current.Provider, AccountID: current.AccountID, Ref: current.Observation.Ref, ObservationRevision: nextRevision, CheckedAt: time.Now(), Complete: true}
			if saveErr := f.store.SaveClusterCapacity(t.Context(), current, snapshot); !errors.Is(saveErr, cloudinfra.ErrConflict) {
				t.Fatal("save did not fence invalid identity", saveErr)
			}
			if _, err = f.svc.ObserveClusterCapacity(t.Context(), f.p, "cloud", r.ID); !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatal("explicit observation did not fence invalid identity", err)
			}
		})
	}
}
