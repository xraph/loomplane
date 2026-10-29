//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type maintenanceRecoveryCredentials struct{ store *connections.PgStore }

func (c maintenanceRecoveryCredentials) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	conn, err := c.store.GetConnection(ctx, org, id)
	return conn, []byte("test-only"), err
}

type maintenanceRecoveryProvider struct {
	enrollmentKindProvider
	effects int
}

func (p *maintenanceRecoveryProvider) ObserveClusterMutation(context.Context, provider.AccountAccess, provider.ClusterMutation) (provider.ClusterObservation, error) {
	return p.observation, nil
}
func (*maintenanceRecoveryProvider) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	price := 0.02
	return provider.ClusterOptions{Digest: "options", CheckedAt: time.Now().Add(-time.Second), Regions: []string{"nyc1"}, NodeSizes: []string{"s-2"}, NodeSizeFacts: []provider.ClusterNodeSize{{ID: "s-2", CPUs: 2, MemoryMiB: 4096, DiskGB: 80, HourlyUSD: &price, Regions: []string{"nyc1"}}}}, nil
}
func (*maintenanceRecoveryProvider) CapacityInventory(context.Context, provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	quota := int64(20)
	return provider.CapacityInventory{Provider: "digitalocean", AccountID: "account", CheckedAt: time.Now(), Quota: provider.CapacityQuota{Servers: &quota}}, nil
}
func (*maintenanceRecoveryProvider) CreateNodePool(context.Context, provider.AccountAccess, provider.ClusterRef, provider.NodePoolSpec) (provider.ClusterMutation, error) {
	return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
}
func (*maintenanceRecoveryProvider) DeleteNodePool(context.Context, provider.AccountAccess, provider.ClusterRef, string) (provider.ClusterMutation, error) {
	return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
}
func (p *maintenanceRecoveryProvider) UpdateNodePool(ctx context.Context, a provider.AccountAccess, _ provider.ClusterRef, _ string, spec provider.NodePoolSpec) (provider.ClusterMutation, error) {
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.effects++
	p.observation.Pools[0].Count = spec.Count
	// The provider accepted the request, but its response never reached the worker.
	return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
}
func TestManagedClusterMaintenanceServiceResponseLossRestart(t *testing.T) {
	db := managedClusterDB(t)
	store, clouds := managedClusterStore(t, db)
	pg := openPg(t, db.Config().ConnString())
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',generation=1,details='{"account_uuid":"account"}' WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
	observed := provider.ClusterObservation{Ref: ref, State: "running", Endpoint: "https://cluster.test", CAHash: "ca", Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "s-2", Count: 2, Labels: map[string]string{}, Taints: []provider.NodePoolTaint{}}}, ObservedAt: now}
	original := &cloudinfra.ClusterOperation{ID: "original", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "original", Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1, Spec: provider.ClusterSpec{OperationID: "original"}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateClusterPreview(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	receipt := provider.ClusterMutation{OperationID: original.ID, Kind: "create", Ref: ref, RequestDigest: provider.ClusterRequestDigest(original.Input.Spec)}
	original.ResourceID = "cluster"
	original.Receipts = []provider.ClusterMutation{receipt}
	original.Status = "succeeded"
	original.Revision++
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "conn", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, ObservedRevision: 1, SchemaVersion: 1, Observation: &observed, CreationReceipt: &receipt}
	if _, err := store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveClusterCheckpoint(t.Context(), original, 1); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 20000, MemoryBytes: 40 << 30, StorageBytes: 800_000_000_000}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES('org','cloud',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	adapter := &maintenanceRecoveryProvider{enrollmentKindProvider: enrollmentKindProvider{observation: observed}}
	makeService := func() *cloudinfra.Service {
		adapters := cloudinfra.NewAdapters()
		if e := adapters.RegisterCluster("digitalocean", adapter, provider.NodePoolUpdate, provider.ClusterMutationObservation); e != nil {
			t.Fatal(e)
		}
		return cloudinfra.NewService(cloudinfra.Deps{Clusters: cloudinfra.NewPgStore(pg), Clouds: clouds, Connections: maintenanceRecoveryCredentials{connections.NewPgStore(pg)}, Authz: enrollmentServiceAuth{}, Adapters: adapters, ManagedClusters: config.ManagedClustersConfig{Enabled: true, QualificationOrgID: "org", QualificationOperatorID: "manager", QualificationAccountID: "account", QualificationRegion: "nyc1", MaxNodes: 3, MaxNodeHourlyMicroUSD: 1000000}})
	}
	svc := makeService()
	principal := security.Principal{OrgID: "org", UserID: "manager"}
	in := cloudinfra.ClusterInput{Kind: "node-pool-update", Provider: "digitalocean", ConnectionID: "conn", ConnectionGeneration: 1, AccountID: "account", CloudRevision: 1, PolicyRevision: 1, ResourceID: "cluster", ResourceRevision: 1, Ref: ref, OptionsDigest: "options", AcknowledgeNodeCosts: true, AcknowledgeUnknownCosts: true, Maintenance: &cloudinfra.ClusterMaintenanceInput{ObservedRevision: 1, PoolID: "pool", Pool: &provider.NodePoolSpec{Name: "workers", Size: "s-2", Count: 3}}}
	op, err := svc.PreviewCluster(t.Context(), principal, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Steps[0].State != "attempted" || saved.Steps[0].AttemptedAt == nil || len(saved.Receipts) != 0 || adapter.effects != 1 {
		t.Fatalf("lost response state: %+v, effects %d", saved, adapter.effects)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)),'{next_attempt_at}',to_jsonb($2::text)) WHERE id=$1`, op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = makeService().ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "succeeded" || adapter.effects != 1 || len(saved.Receipts) != 0 || current.Observation.Pools[0].ID != "pool" || current.Observation.Pools[0].Count != 3 {
		t.Fatalf("restart replayed or failed convergence: %+v effects=%d", saved, adapter.effects)
	}
	var status string
	var nodes int64
	if err = db.QueryRow(t.Context(), `SELECT status,servers FROM loomplane_cloud_capacity_reservations WHERE id=$1`, op.ID).Scan(&status, &nodes); err != nil || status != "reserved" || nodes != 1 {
		t.Fatalf("reservation not retained: %s %d %v", status, nodes, err)
	}
}
