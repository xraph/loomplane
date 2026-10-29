//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

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
