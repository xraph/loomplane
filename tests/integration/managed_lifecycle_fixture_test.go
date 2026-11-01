//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	apiclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

// lifecycleManualFixture drives real review routes against an isolated provider adapter.
func lifecycleManualFixture(t *testing.T, a *capacityAPI) string {
	t.Helper()
	if _, err := a.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details='{"account_uuid":"account"}' WHERE id='capacity-do'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
	observed := provider.ClusterObservation{Ref: ref, State: "running", Endpoint: "https://cluster.test", CAHash: "ca", Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "s-2", Count: 2, Labels: map[string]string{}, Taints: []provider.NodePoolTaint{}}}, ObservedAt: now}
	original := &cloudinfra.ClusterOperation{ID: "original", OrgID: a.org, CloudID: a.cloud, Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "original", Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "capacity-do", CloudRevision: 1, ConnectionGeneration: 1, Spec: provider.ClusterSpec{OperationID: "original"}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := a.store.CreateClusterPreview(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	receipt := provider.ClusterMutation{OperationID: original.ID, Kind: "create", Ref: ref, RequestDigest: provider.ClusterRequestDigest(original.Input.Spec)}
	original.ResourceID = "fixture-manual-cluster"
	original.Receipts = []provider.ClusterMutation{receipt}
	original.Status = "succeeded"
	original.Revision++
	r := &cloudinfra.ManagedResource{ID: "fixture-manual-cluster", OrgID: a.org, CloudID: a.cloud, ConnectionID: "capacity-do", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, ObservedRevision: 1, SchemaVersion: 1, Observation: &observed, CreationReceipt: &receipt}
	observed.Version = "1.36"
	if _, err := a.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}

	inventoryRevision, inventoryErr := a.store.BeginClusterCapacityObservation(t.Context(), a.org, a.cloud, r.ID)
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	servers, cpu, memory, disk := int64(1), int64(2000), int64(4<<30), int64(80_000_000_000)
	inventory := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: ref, ObservationRevision: inventoryRevision, CheckedAt: time.Now().UTC(), Complete: true}
	for _, id := range []string{"node-1", "node-2"} {
		inventory.Nodes = append(inventory.Nodes, cloudinfra.ClusterNodeCapacity{ExternalVMID: id, NodeUID: id, PoolID: "pool", Provisioned: cloudinfra.CapacityResources{Servers: &servers, CPUMillis: &cpu, MemoryBytes: &memory, StorageBytes: &disk}})
	}
	if inventoryErr = a.store.SaveClusterCapacity(t.Context(), r, inventory); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	if err := a.store.SaveClusterCheckpoint(t.Context(), original, 1); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{OrgID: a.org, CloudID: a.cloud, Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 20000, MemoryBytes: 40 << 30, StorageBytes: 800_000_000_000}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES($1,$2,1,$3)`, a.org, a.cloud, raw); err != nil {
		t.Fatal(err)
	}

	adapter := &maintenanceRecoveryProvider{enrollmentKindProvider: enrollmentKindProvider{observation: observed}}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", adapter, provider.NodePoolUpdate, provider.NodePoolAutoscaling, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	svc, err := forge.InjectType[*cloudinfra.Service](a.r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = a.sql.QueryRow(t.Context(), `SELECT id FROM authsome_users WHERE email='clouds@example.test' AND deleted_at IS NULL`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	pg := openPg(t, a.sql.Config().ConnString())
	svc.ClusterWorkflow = nil
	svc.Adapters = adapters
	svc.Connections = maintenanceRecoveryCredentials{connections.NewPgStore(pg)}
	svc.ClusterCapacity = config.ClusterCapacityConfig{Enabled: true}
	svc.ManagedClusters = config.ManagedClustersConfig{Enabled: true, QualificationOrgID: a.org, QualificationOperatorID: owner, QualificationAccountID: "account", QualificationRegion: "nyc1", MaxNodes: 3, MaxNodeHourlyMicroUSD: 1000000}
	in := apiclient.ClusterInput{Kind: "node-pool-update", Provider: "digitalocean", ConnectionID: "capacity-do", ConnectionGeneration: 1, AccountID: "account", CloudRevision: 1, PolicyRevision: 1, ResourceID: r.ID, ResourceRevision: 1, Ref: ref, OptionsDigest: "options", AcknowledgeNodeCosts: true, AcknowledgeUnknownCosts: true, Maintenance: &apiclient.ClusterMaintenanceInput{ObservedRevision: 1, PoolID: "pool", Pool: &apiclient.NodePoolSpec{Name: "workers", Size: "s-2", Count: 3}}}
	var preview apiclient.CloudClusterOperation
	a.c.must(http.StatusOK, http.MethodPost, a.path("/clusters/preview"), in, &preview)
	if preview.ID == "" || preview.Digest == "" {
		t.Fatal("missing current review")
	}
	// A stale review selector cannot authorize the effect.
	a.c.must(http.StatusConflict, http.MethodPost, a.path("/cluster-operations/"+preview.ID+"/apply"), apiclient.InfrastructureApplyInput{Review: "outdated-review"}, nil)
	a.c.must(http.StatusAccepted, http.MethodPost, a.path("/cluster-operations/"+preview.ID+"/apply"), apiclient.InfrastructureApplyInput{Review: preview.Digest}, nil)
	a.c.must(http.StatusOK, http.MethodGet, a.path("/cluster-operations/"+preview.ID), nil, &preview)
	if preview.Status != "accepted" {
		t.Fatalf("review apply status: %s", preview.Status)
	}
	if adapter.effects != 0 {
		t.Fatal("HTTP approval performed a provider effect")
	}
	// Keep the test cluster available for a new browser review.
	a.c.must(http.StatusOK, http.MethodPost, a.path("/cluster-operations/"+preview.ID+"/cancel"), nil, nil)
	return r.ID
}
