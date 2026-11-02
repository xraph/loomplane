//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func clusterLedgerPreview(t *testing.T, f *infraFixture, id string) *cloudinfra.ClusterOperation {
	t.Helper()
	now := time.Now().UTC()
	if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','100'::jsonb)`); checkErr != nil {
		t.Fatal(checkErr)
	}
	o := &cloudinfra.ClusterOperation{ID: id, OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "review-" + id, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra-account", CloudRevision: 1, ConnectionGeneration: 1, PolicyRevision: 1, AcknowledgeUnknownCosts: true}, CapacityTarget: cloudinfra.ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 20_000_000_000}, Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "pending", RequestDigest: "review-" + id}}}
	var generation int64
	if checkErr := f.sql.QueryRow(t.Context(), `SELECT generation FROM loomplane_cloud_connections WHERE id='infra-account'`).Scan(&generation); checkErr != nil {
		t.Fatal(checkErr)
	}
	o.Input.ConnectionGeneration = generation
	if checkErr := f.store.CreateClusterPreview(t.Context(), o); checkErr != nil {
		t.Fatal(checkErr)
	}
	return o
}
func TestClusterEnvelopeSharedAccountRace(t *testing.T) {
	f := ledgerFixture(t)
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	policy.Limits.CPUMillis = 10_000
	if err = f.store.SaveCapacityPolicy(t.Context(), policy, 1); err != nil {
		t.Fatal(err)
	}
	a := ledgerPreview(t, f)
	b := clusterLedgerPreview(t, f, "cluster-race")
	alias := &connections.Connection{ID: "account-alias", OrgID: "org", Cloud: connections.DigitalOcean, Name: "Alias", Status: connections.StatusReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = connections.NewPgStore(ledgerDB(t, f)).CreateConnection(t.Context(), alias); err != nil {
		t.Fatal(err)
	}
	b.Input.ConnectionID = alias.ID
	b.Input.ConnectionGeneration = alias.Generation
	b.Input.PolicyRevision = 2
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1 WHERE id=$2`, raw, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','1'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- f.store.Accept(t.Context(), a) }()
	go func() { defer wg.Done(); <-start; results <- f.store.AcceptClusterOperation(t.Context(), b, 2) }()
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("shared server/cluster ceiling winners=%d", winners)
	}
}
func TestClusterCapacityLedgerHandover(t *testing.T) { clusterEnvelopeHandover(t, false) }
func TestClusterScalingZeroInitialEnvelope(t *testing.T) {
	clusterEnvelopeHandover(t, true, false, true)
}
func TestClusterEnvelopeDecreaseWaitsForConvergence(t *testing.T) { clusterEnvelopeHandover(t, true) }
func TestClusterCapacityLedgerMultiPool(t *testing.T)             { clusterEnvelopeHandover(t, false, true) }
func clusterEnvelopeHandover(t *testing.T, native bool, multi ...bool) {
	multiPool := len(multi) > 0 && multi[0]
	f := ledgerFixture(t)
	o := clusterLedgerPreview(t, f, "handover")
	now := time.Now().UTC()
	unit := o.CapacityTarget
	if multiPool {
		policy, e := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
		if e != nil {
			t.Fatal(e)
		}
		policy.Revision++
		policy.Limits.CPUMillis = 4000
		if e = f.store.SaveCapacityPolicy(t.Context(), policy, 1); e != nil {
			t.Fatal(e)
		}
		o.Input.PolicyRevision = 2
		o.CapacityTarget = cloudinfra.ResourceVector{Servers: 2, CPUMillis: 4000, MemoryBytes: 4 << 30, StorageBytes: 40_000_000_000}
	}
	before := now.Add(-time.Second)
	spec := provider.ClusterSpec{OperationID: o.ID, Region: "region", NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "small", Count: 1}}}
	if native {
		spec.NodePools[0].Autoscale = true
		spec.NodePools[0].Min = 1
		spec.NodePools[0].Max = 3
		policy, e := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
		if e != nil {
			t.Fatal(e)
		}
		policy.Revision++
		policy.Limits.CPUMillis = 6000
		if e = f.store.SaveCapacityPolicy(t.Context(), policy, 1); e != nil {
			t.Fatal(e)
		}
		o.Input.PolicyRevision = 2
	}
	if multiPool {
		spec.NodePools = append(spec.NodePools, provider.NodePoolSpec{Name: "second", Size: "small", Count: 1})
	}
	o.Input.Spec = spec
	price := int64(10)
	review := cloudinfra.ClusterPoolEnvelopeReview{Key: "workers", RequestDigest: provider.ClusterPoolRequestDigest(spec, spec.NodePools[0]), Maximum: unit, Initial: unit, Unit: unit, Money: cloudinfra.MoneyEstimate{Components: []cloudinfra.MoneyComponent{{Name: "nodes", Source: "provider_options", Identity: "size", Quantity: 1, HourlyMicroUSD: &price}}}}
	if native {
		review.Maximum = cloudinfra.ResourceVector{Servers: 3, CPUMillis: 6000, MemoryBytes: 6 << 30, StorageBytes: 60_000_000_000}
		review.Money.Components[0].Quantity = 3
	}
	o.Input.EnvelopeReview = &cloudinfra.ClusterEnvelopeReview{Pools: []cloudinfra.ClusterPoolEnvelopeReview{review}}
	if multiPool {
		second := review
		second.Key = "second"
		second.RequestDigest = provider.ClusterPoolRequestDigest(spec, spec.NodePools[1])
		o.Input.EnvelopeReview.Pools = append(o.Input.EnvelopeReview.Pools, second)
	}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1 WHERE id=$2`, raw, o.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
		t.Fatal(err)
	}
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "region", Incarnation: "birth"}
	receipt := provider.ClusterMutation{Kind: "create", OperationID: o.ID, RequestDigest: provider.ClusterRequestDigest(spec), Ref: ref}
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "infra-account", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, CreationReceipt: &receipt, Observation: &provider.ClusterObservation{Ref: ref, ObservedAt: now, Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "small", Count: 1, OperationID: o.ID, RequestDigest: review.RequestDigest}}}}
	if multiPool {
		r.Observation.Pools = append(r.Observation.Pools, provider.NodePoolSpec{ID: "second-pool", Name: "second", Size: "small", Count: 0, OperationID: o.ID, RequestDigest: o.Input.EnvelopeReview.Pools[1].RequestDigest})
	}
	if native {
		r.Observation.Pools[0].Autoscale = true
		r.Observation.Pools[0].Min = 1
		r.Observation.Pools[0].Max = 3
	}
	if _, err = f.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	o.ResourceID = r.ID
	o.Receipts = []provider.ClusterMutation{receipt}
	o.Steps[0].State = "completed"
	o.Steps[0].CompletedAt = &before
	o.Steps[0].RemoteResourceID = ref.ID
	raw, err = json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1,resource_id=$2 WHERE id=$3`, raw, r.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	v := unit
	facts := cloudinfra.CapacityResources{Servers: &v.Servers, CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}
	if err = f.store.SaveClusterAccount(t.Context(), "org", "cloud", cloudinfra.CapacityAccountSnapshot{Provider: r.Provider, AccountID: r.AccountID, CheckedAt: now, Provisioned: facts}); err != nil {
		t.Fatal(err)
	}
	nodes := []cloudinfra.ClusterNodeCapacity{{ExternalVMID: "vm", NodeUID: "node", PoolID: "pool", Provisioned: facts}}
	save := func() {
		revision, e := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
		if e != nil {
			t.Fatal(e)
		}
		snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: ref, ObservationRevision: revision, CheckedAt: now, Complete: true, Nodes: nodes}
		if e = f.store.SaveClusterCapacity(t.Context(), r, snapshot); e != nil {
			t.Fatal(e)
		}
	}
	status := func() string {
		var s string
		if e := f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	save()
	if status() != "reserved" {
		t.Fatal("aggregate account totals released physical hold")
	}
	membership := map[string]any{"complete": true, "provider": r.Provider, "account_id": r.AccountID, "checked_at": now, "servers": []provider.CapacityServer{{ID: "vm", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}}}
	raw, err = json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, raw); err != nil {
		t.Fatal(err)
	}
	save()
	if multiPool {
		if status() != "reserved" {
			t.Fatal("missing second pool released full hold")
		}
		var remaining int64
		if checkErr := f.sql.QueryRow(t.Context(), `SELECT servers FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&remaining); checkErr != nil || remaining != 1 {
			t.Fatal("partial handover counted resolved pool twice", remaining, checkErr)
		}
		var boundResource string
		if checkErr := f.sql.QueryRow(t.Context(), `SELECT body->>'managed_resource_id' FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&boundResource); checkErr != nil || boundResource != r.ID {
			t.Fatal("partial hold lost retirement resource identity", boundResource, checkErr)
		}
		prior := r.Revision
		r.Revision++
		r.Observation.Pools[1].Count = 1
		now = time.Now().UTC()
		r.Observation.ObservedAt = now
		if _, err = f.store.ClaimManagedResource(t.Context(), r, prior); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, cloudinfra.ClusterNodeCapacity{ExternalVMID: "vm2", NodeUID: "node2", PoolID: "second-pool", Provisioned: facts})
		membership["checked_at"] = now
		membership["servers"] = []provider.CapacityServer{{ID: "vm", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}, {ID: "vm2", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}}
		raw, err = json.Marshal(membership)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, raw); err != nil {
			t.Fatal(err)
		}
		save()
	}
	if status() != "materialized" {
		t.Fatal("exact membership and pool receipt retained duplicate hold")
	}
	f.store = cloudinfra.NewPgStore(ledgerDB(t, f))
	save()
	if status() != "materialized" {
		t.Fatal("restart-style repeat changed handover")
	}
	if native {
		if checkErr := f.store.Accept(t.Context(), ledgerPreview(t, f)); checkErr == nil {
			t.Fatal("server writer ignored native envelope remainder")
		}
		var rawEnvelope []byte
		if checkErr := f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, o.ID+":workers").Scan(&rawEnvelope); checkErr != nil {
			t.Fatal(checkErr)
		}
		var envelope cloudinfra.ClusterCapacityEnvelope
		if checkErr := json.Unmarshal(rawEnvelope, &envelope); checkErr != nil {
			t.Fatal(checkErr)
		}
		if envelope.Consumed.Servers != 1 || envelope.Maximum.Servers != 3 || envelope.Pending.Servers != 0 {
			t.Fatal("native actual/max double-counted", envelope)
		}

		if len(multi) > 1 && multi[1] {
			// Exercise scaling admission with no requested nodes. The maximum
			// remains reserved after bounds convergence and physical handover.
			if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET status='succeeded',body=jsonb_set(body,'{status}','"succeeded"'::jsonb) WHERE id=$1`, o.ID); checkErr != nil {
				t.Fatal(checkErr)
			}
			if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','100'::jsonb)`); checkErr != nil {
				t.Fatal(checkErr)
			}
			maximum := envelope.Maximum
			nativeReview := envelope.Review
			nativeReview.EnvelopeID = envelope.ID
			nativeReview.ExpectedRevision = envelope.Revision
			nativeReview.PoolID = envelope.PoolID
			nativeReview.Baseline = envelope.Consumed
			nativeReview.Initial = cloudinfra.ResourceVector{}
			nativeReview.Money.Known = true
			nativeReview.Money.CheckedAt = time.Now().UTC()
			scaling := &cloudinfra.ClusterOperation{ID: "scaling-zero", OrgID: "org", CloudID: "cloud", ResourceID: r.ID, Kind: "node-pool-scaling", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "scaling-zero", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), CapacityTarget: maximum, MoneyEstimate: &nativeReview.Money, Input: cloudinfra.ClusterInput{Kind: "node-pool-scaling", CloudRevision: 1, ConnectionID: o.Input.ConnectionID, ConnectionGeneration: o.Input.ConnectionGeneration, ResourceID: r.ID, ResourceRevision: r.Revision, Ref: ref, Provider: r.Provider, AccountID: r.AccountID, PolicyRevision: 2, Scaling: &cloudinfra.ClusterScalingPolicy{PoolID: "pool"}, Maintenance: &cloudinfra.ClusterMaintenanceInput{PoolID: "pool", ObservedRevision: r.ObservedRevision}, EnvelopeReview: &cloudinfra.ClusterEnvelopeReview{Automatic: true, Pools: []cloudinfra.ClusterPoolEnvelopeReview{nativeReview}}}, Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "pending", RequestDigest: "scaling-zero"}}}
			if checkErr := f.store.CreateClusterPreview(t.Context(), scaling); checkErr != nil {
				t.Fatal(checkErr)
			}
			if checkErr := f.store.AcceptClusterOperation(t.Context(), scaling, 2); checkErr != nil {
				t.Fatal(checkErr)
			}
			pending, envelopeErr := f.store.ClusterScalingEnvelope(t.Context(), "org", r.ID, "pool")
			if envelopeErr != nil || pending.Maximum != maximum || pending.Pending != (cloudinfra.ResourceVector{}) {
				t.Fatal(pending, envelopeErr)
			}
			scaling.Receipts = []provider.ClusterMutation{{Kind: "node-pool-scaling", OperationID: scaling.ID, Ref: ref, PoolID: "pool"}}
			scaling.Steps[0].State = "completed"
			scaling.Steps[0].CompletedAt = &before
			scalingRaw, marshalErr := json.Marshal(scaling)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1 WHERE id=$2`, scalingRaw, scaling.ID); checkErr != nil {
				t.Fatal(checkErr)
			}
			now = time.Now().UTC()
			r.Observation.ObservedAt = now
			prior := r.Revision
			r.Revision++
			if _, checkErr := f.store.ClaimManagedResource(t.Context(), r, prior); checkErr != nil {
				t.Fatal(checkErr)
			}
			membership["checked_at"] = now
			membershipRaw, marshalErr := json.Marshal(membership)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, membershipRaw); checkErr != nil {
				t.Fatal(checkErr)
			}
			save()
			var state string
			if checkErr := f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id=$1`, scaling.ID).Scan(&state); checkErr != nil || state != "materialized" {
				t.Fatal(state, checkErr)
			}
			settled, checkErr := f.store.ClusterScalingEnvelope(t.Context(), "org", r.ID, "pool")
			if checkErr != nil || settled.Maximum != maximum || settled.Consumed.Servers != 1 || settled.Pending.Servers != 0 {
				t.Fatal(settled, checkErr)
			}
			envelope = *settled
		}
		reduced := envelope.Review
		reduced.EnvelopeID = envelope.ID
		reduced.ExpectedRevision = envelope.Revision
		reduced.PoolID = envelope.PoolID
		reduced.Baseline = envelope.Consumed
		reduced.Maximum = envelope.Review.Unit
		reduced.Initial = cloudinfra.ResourceVector{}
		reduced.Money.Components = append([]cloudinfra.MoneyComponent(nil), reduced.Money.Components...)
		reduced.Money.Components[0].Quantity = 1
		decrease := &cloudinfra.ClusterOperation{ID: "decrease", OrgID: "org", CloudID: "cloud", ResourceID: r.ID, Kind: "node-pool-update", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "decrease", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Input: cloudinfra.ClusterInput{Kind: "node-pool-update", ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, PolicyRevision: 2, EnvelopeReview: &cloudinfra.ClusterEnvelopeReview{Pools: []cloudinfra.ClusterPoolEnvelopeReview{reduced}}}, Receipts: []provider.ClusterMutation{{OperationID: "decrease", Ref: ref}}, Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "completed", RequestDigest: "decrease", CompletedAt: &before}}}
		if checkErr := f.store.CreateClusterPreview(t.Context(), decrease); checkErr != nil {
			t.Fatal(checkErr)
		}
		tx, txErr := ledgerDB(t, f).BeginTx(t.Context(), nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		_, err = f.store.AdjustClusterEnvelope(t.Context(), tx, cloudinfra.ClusterEnvelopeInput{OrgID: "org", CloudID: "cloud", ResourceID: r.ID, OperationID: decrease.ID, Provider: r.Provider, AccountID: r.AccountID, ExpectedPolicyRevision: 2, ExpectedEnvelopeRevision: envelope.Revision, Review: reduced})
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		save()
		if err = f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, envelope.ID).Scan(&rawEnvelope); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(rawEnvelope, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Maximum.Servers != 3 || envelope.Money.Components[0].Quantity != 3 {
			t.Fatal("decrease released old maximum or cost before fresh bounds")
		}
		prior := r.Revision
		r.Revision++
		r.Observation.Pools[0].Max = 1
		now = time.Now().UTC()
		r.Observation.ObservedAt = now
		if _, err = f.store.ClaimManagedResource(t.Context(), r, prior); err != nil {
			t.Fatal(err)
		}
		membership["checked_at"] = now
		raw, err = json.Marshal(membership)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, raw); err != nil {
			t.Fatal(err)
		}
		save()
		if err = f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, envelope.ID).Scan(&rawEnvelope); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(rawEnvelope, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Maximum.Servers != 1 || envelope.Money.Components[0].Quantity != 1 {
			t.Fatal("converged decrease retained old ceiling", envelope)
		}
	}
	if !native && !multiPool {
		upgrade := &cloudinfra.ClusterOperation{ID: "surge", OrgID: "org", CloudID: "cloud", ResourceID: r.ID, Kind: "cluster-upgrade", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "surge", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), CapacityTarget: v, Input: cloudinfra.ClusterInput{Kind: "cluster-upgrade", ResourceID: r.ID, Ref: ref, Provider: r.Provider, AccountID: r.AccountID, PolicyRevision: 1, Maintenance: &cloudinfra.ClusterMaintenanceInput{Version: "v2"}}, Receipts: []provider.ClusterMutation{{Kind: "cluster-upgrade", OperationID: "surge", Ref: ref}}, Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "completed", RequestDigest: "surge", CompletedAt: &before}}}
		if checkErr := f.store.CreateClusterPreview(t.Context(), upgrade); checkErr != nil {
			t.Fatal(checkErr)
		}
		if upgrade.Input.EnvelopeReview == nil || upgrade.Input.EnvelopeReview.Temporary == nil {
			t.Fatal("surge preview omitted verified baseline")
		}
		holdRaw, marshalErr := json.Marshal(map[string]any{"operation_id": "surge", "cluster_operation_id": "surge", "managed_resource_id": r.ID, "provider": r.Provider, "account_id": r.AccountID, "delta": v, "target": v})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES('org','cloud','surge',1,'surge',1,2000,2147483648,20000000000,'reserved',$1)`, holdRaw); err != nil {
			t.Fatal(err)
		}
		surgeStatus := func() string {
			var state string
			if checkErr := f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id='surge'`).Scan(&state); checkErr != nil {
				t.Fatal(checkErr)
			}
			return state
		}
		save()
		if surgeStatus() != "reserved" {
			t.Fatal("unconverged upgrade released surge")
		}
		prior := r.Revision
		r.Revision++
		r.Observation.Version = "v2"
		now = time.Now().UTC()
		r.Observation.ObservedAt = now
		if _, err = f.store.ClaimManagedResource(t.Context(), r, prior); err != nil {
			t.Fatal(err)
		}
		nodes[0].ExternalVMID = "replacement-vm"
		nodes[0].NodeUID = "replacement-node"
		membership["checked_at"] = now
		membership["servers"] = []provider.CapacityServer{{ID: "vm", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}, {ID: "replacement-vm", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}}
		raw, err = json.Marshal(membership)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, raw); err != nil {
			t.Fatal(err)
		}
		save()
		if surgeStatus() != "reserved" {
			t.Fatal("billable old VM released surge")
		}
		membership["servers"] = []provider.CapacityServer{{ID: "replacement-vm", CPUMillis: &v.CPUMillis, MemoryBytes: &v.MemoryBytes, StorageBytes: &v.StorageBytes}}
		raw, err = json.Marshal(membership)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1`, raw); err != nil {
			t.Fatal(err)
		}
		save()
		if surgeStatus() != "materialized" {
			t.Fatal("verified replacement cleanup retained surge")
		}
	}
	// A replacement carrying copied markers must not capture the bound envelope.
	oldRevision := r.Revision
	r.Revision++
	r.Observation.Pools[0].ID = "replacement"
	if _, checkErr := f.store.ClaimManagedResource(t.Context(), r, oldRevision); checkErr != nil {
		t.Fatal(checkErr)
	}
	save()
	var bound string
	if checkErr := f.sql.QueryRow(t.Context(), `SELECT body->>'pool_id' FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, o.ID+":workers").Scan(&bound); checkErr != nil || bound != "pool" {
		t.Fatal("replacement captured envelope", bound, checkErr)
	}

	if !native && !multiPool {
		prior := r.Revision
		r.Revision++
		deleted := time.Now().UTC()
		r.DeletedAt = &deleted
		r.Status = "deleted"
		if _, err = f.store.ClaimManagedResource(t.Context(), r, prior); err != nil {
			t.Fatal(err)
		}
		reads := 0
		adapters := cloudinfra.NewAdapters()
		if err = adapters.RegisterServer("digitalocean", &emptyEnvelopeAccount{ledgerProvider: &ledgerProvider{f.provider}, reads: &reads}); err != nil {
			t.Fatal(err)
		}
		f.svc.Adapters = adapters
		if _, err = f.svc.RefreshCapacity(t.Context(), f.p, "cloud"); err != nil {
			t.Fatal(err)
		}
		var state string
		if err = f.sql.QueryRow(t.Context(), `SELECT body->>'state' FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, o.ID+":workers").Scan(&state); err != nil || state == "released" {
			t.Fatal("status-only tombstone released envelope", state, err)
		}
		retired := &cloudinfra.ClusterOperation{ID: "retired", Input: cloudinfra.ClusterInput{ResourceID: r.ID, Ref: ref}, OrgID: "org", CloudID: "cloud", ResourceID: r.ID, Kind: "retire-cluster", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "retirement", CreatedAt: deleted, UpdatedAt: deleted, ExpiresAt: deleted.Add(time.Minute), Receipts: []provider.ClusterMutation{{Kind: "retire-cluster", OperationID: "retired", Ref: ref, RequestDigest: "retirement"}}, Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "completed", RequestDigest: "retirement", CompletedAt: &deleted}}}
		if err = f.store.CreateClusterPreview(t.Context(), retired); err != nil {
			t.Fatal(err)
		}
		retired.Status = "succeeded"
		raw, err = json.Marshal(retired)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET status='succeeded',body=$1 WHERE id='retired'`, raw); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.RefreshCapacity(t.Context(), f.p, "cloud"); err != nil {
			t.Fatal(err)
		}
		if err = f.sql.QueryRow(t.Context(), `SELECT body->>'state' FROM loomplane_cluster_capacity_envelopes WHERE id=$1`, o.ID+":workers").Scan(&state); err != nil || state != "released" || reads != 2 {
			t.Fatal("last retired cluster lost refresh/cleanup path", state, reads, err)
		}
	}
}

type emptyEnvelopeAccount struct {
	*ledgerProvider
	reads *int
}

func (p *emptyEnvelopeAccount) CapacityInventory(_ context.Context, c provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	*p.reads++
	return provider.CapacityInventory{Provider: c.Provider, AccountID: c.AccountID, CheckedAt: time.Now().UTC(), Servers: []provider.CapacityServer{}}, nil
}

func TestClusterEnvelopeLegacyCloudGate(t *testing.T) {
	for _, kind := range []string{"legacy", "temporary", "standing", "live"} {
		t.Run(kind, func(t *testing.T) {
			f := ledgerFixture(t)
			if checkErr := cloudprofiles.NewPgStore(ledgerDB(t, f)).CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "peer", OrgID: "org", ProjectID: "proj", Name: "Peer", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); checkErr != nil {
				t.Fatal(checkErr)
			}
			switch kind {
			case "temporary":
				cluster := clusterLedgerPreview(t, f, "cluster-hold")
				if checkErr := f.store.AcceptClusterOperation(t.Context(), cluster, 2); checkErr != nil {
					t.Fatal(checkErr)
				}
			case "standing":
				e := cloudinfra.ClusterCapacityEnvelope{SchemaVersion: 1, ID: "standing", OrgID: "org", CloudID: "cloud", Provider: "digitalocean", AccountID: "account", Revision: 1, State: "active", Maximum: cloudinfra.ResourceVector{Servers: 3, CPUMillis: 6000}, Consumed: cloudinfra.ResourceVector{Servers: 1, CPUMillis: 2000}}
				raw, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,provider,account_id,revision,body) VALUES('org','cloud','standing','digitalocean','account',1,$1)`, raw); err != nil {
					t.Fatal(err)
				}
			case "live":
				if _, checkErr := f.store.ClaimManagedResource(t.Context(), &cloudinfra.ManagedResource{ID: "live", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "physical", Incarnation: "birth", Ownership: "managed", Status: "ready", Revision: 1}, 0); checkErr != nil {
					t.Fatal(checkErr)
				}
			}
			o, err := f.svc.PreviewServer(t.Context(), f.p, "peer", cloudinfra.ServerInput{Kind: "create", Name: "peer-server", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
			if err != nil {
				t.Fatal(err)
			}
			o.Status = "pending"
			err = f.store.Accept(t.Context(), o)
			if kind == "legacy" {
				if err != nil {
					t.Fatal("legacy account without cluster participation changed", err)
				}
				return
			}
			var blocked *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &blocked) || blocked.Block.Reason != "review_required" || blocked.Block.Resource != "policy" {
				t.Fatal("policy-less peer bypassed shared accounting", err)
			}
			if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_infrastructure_operations SET body=jsonb_set(body,'{attempted}','true') WHERE id=$1`, o.ID); err != nil {
				t.Fatal(err)
			}
			if block, checkErr := ledgerReserve(t, f, o, *o.CapacityTarget); checkErr != nil || block != nil {
				t.Fatal("already attempted reconciliation blocked", block, checkErr)
			}
		})
	}
}

func TestClusterCapacityLedgerLegacyFirstAttemptBarrier(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "modern", true: "historical"}[historical], func(t *testing.T) {
			for _, serverFirst := range []bool{false, true} {
				t.Run(map[bool]string{false: "cluster_wins", true: "server_wins"}[serverFirst], func(t *testing.T) {
					f := ledgerFixture(t)
					if err := cloudprofiles.NewPgStore(ledgerDB(t, f)).CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "peer", OrgID: "org", ProjectID: "proj", Name: "Peer", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
						t.Fatal(err)
					}
					o, err := f.svc.PreviewServer(t.Context(), f.p, "peer", cloudinfra.ServerInput{Kind: "create", Name: "peer-server", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
					if err != nil {
						t.Fatal(err)
					}
					if historical {
						// Insert the representation approved before capacity targets existed.
						o.ID += "-historical"
						o.CapacityTarget = nil
						if err = f.store.CreatePreview(t.Context(), o); err != nil {
							t.Fatal(err)
						}
					}

					o.Status = "pending"
					if err = f.store.Accept(t.Context(), o); err != nil {
						t.Fatal(err)
					}
					early := make(chan error, 1)
					resume := make(chan struct{})
					finished := make(chan error, 1)
					go func() {
						early <- f.store.EnsureInfrastructureCapacity(t.Context(), o)
						<-resume
						o.Attempted = true
						finished <- f.store.SaveOperation(t.Context(), o)
					}()
					if err = <-early; err != nil {
						close(resume)
						<-finished
						t.Fatal(err)
					}
					if serverFirst {
						close(resume)
						if err = <-finished; err != nil {
							t.Fatal(err)
						}
						var holds int
						if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE id=$1 AND status='reserved'`, o.ID).Scan(&holds); err != nil || (!historical && holds != 1) {
							t.Fatal("first effect lacks durable hold", holds, err)
						}
					}
					cluster := clusterLedgerPreview(t, f, "barrier-cluster")
					legacyLedgerAlias(t, f, cluster)
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','1'::jsonb)`); err != nil {
						t.Fatal(err)
					}
					err = f.store.AcceptClusterOperation(t.Context(), cluster, 2)
					if serverFirst {
						if err == nil {
							t.Fatal("cluster ignored unresolved server effect")
						}
						// Historical checkpointing remains available after another account user appears.
						if err = f.store.SaveOperation(t.Context(), o); err != nil {
							t.Fatal("attempted recovery blocked", err)
						}
						return
					}
					if err != nil {
						close(resume)
						<-finished
						t.Fatal(err)
					}
					close(resume)
					if err = <-finished; !errors.Is(err, cloudinfra.ErrConflict) {
						t.Fatal("server crossed first-effect barrier", err)
					}
					saved, err := f.store.GetOperation(t.Context(), "org", "peer", o.ID)
					if err != nil || saved.Attempted {
						t.Fatal("blocked attempt persisted", saved, err)
					}
				})
			}
		})
	}
}

func legacyLedgerOperation(t *testing.T, f *infraFixture, kind, account string) *cloudinfra.Operation {
	t.Helper()
	if err := cloudprofiles.NewPgStore(ledgerDB(t, f)).CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "peer", OrgID: "org", ProjectID: "proj", Name: "Peer", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Minute).UTC()
	o := &cloudinfra.Operation{ID: "historical", OrgID: "org", CloudID: "peer", ServerID: "historical-host", Provider: "digitalocean", AccountID: account, ConnectionID: "infra-account", Review: "original-approved-review", Status: "failed", Attempted: true, Input: cloudinfra.ServerInput{Kind: kind, Size: "small"}, CreatedAt: now, UpdatedAt: now, ExpiresAt: now}
	// Insert an already-attempted old row without passing a new first-attempt hook.
	if err := f.store.CreatePreview(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	return o
}

func legacyLedgerAlias(t *testing.T, f *infraFixture, o *cloudinfra.ClusterOperation) {
	t.Helper()
	alias := &connections.Connection{ID: "legacy-account-alias", OrgID: "org", Cloud: connections.DigitalOcean, Name: "Alias", Status: connections.StatusReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := connections.NewPgStore(ledgerDB(t, f)).CreateConnection(t.Context(), alias); err != nil {
		t.Fatal(err)
	}
	o.Input.ConnectionID = alias.ID
	o.Input.ConnectionGeneration = alias.Generation
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1 WHERE id=$2`, raw, o.ID); err != nil {
		t.Fatal(err)
	}
}

func legacyLedgerMembership(t *testing.T, f *infraFixture, members []provider.CapacityServer, complete bool, checked time.Time) {
	t.Helper()
	gen, err := f.store.BeginCapacityRefresh(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	count := int64(len(members))
	resources := cloudinfra.CapacityResources{Servers: inventoryNumber(count), CPUMillis: inventoryNumber(count * 2000), MemoryBytes: inventoryNumber(count * (2 << 30)), StorageBytes: inventoryNumber(count * 20_000_000_000)}
	if err = f.store.SaveCapacityInventory(t.Context(), "org", "cloud", gen, nil, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: resources, Quota: provider.CapacityQuota{Servers: inventoryNumber(2)}, CheckedAt: checked}}); err != nil {
		t.Fatal(err)
	}
	membership := map[string]any{"provider": "digitalocean", "account_id": "account", "complete": complete, "checked_at": checked, "servers": members}
	raw, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=$1 WHERE org_id='org' AND provider='digitalocean' AND account_id='account'`, raw); err != nil {
		t.Fatal(err)
	}
}

func TestClusterCapacityLedgerHistoricalSettlement(t *testing.T) {
	for _, test := range []struct {
		name, kind, resolution string
		allowed                bool
	}{
		{"lost_create_response", "create", "lost", false},
		{"aggregate_only", "create", "aggregate", false},
		{"legacy_null_membership", "create", "null_membership", false},
		{"partial_create_response", "create", "partial", false},
		{"missing_action_receipt", "create", "no_actions", false},
		{"cancelled_unknown_effect", "create", "cancelled", false},
		{"succeeded_unknown_effect", "create", "succeeded", false},
		{"resize_still_running", "resize", "partial", false},
		{"wrong_action_identity", "resize", "wrong_action", false},
		{"wrong_member_identity", "create", "wrong_member", false},
		{"incomplete_membership", "create", "incomplete", false},
		{"old_membership", "resize", "old", false},
		{"exact_create", "create", "complete", true},
		{"exact_resize", "resize", "complete", true},
		{"empty_snapshot_is_not_absence", "create", "empty", false},
		{"wrong_delete_identity", "create", "wrong_delete", false},
		{"exact_absence", "create", "absent", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := ledgerFixture(t)
			o := legacyLedgerOperation(t, f, test.kind, "account")
			cluster := clusterLedgerPreview(t, f, "legacy-settlement")
			legacyLedgerAlias(t, f, cluster)
			var original string
			if err := f.sql.QueryRow(t.Context(), `SELECT body::text FROM loomplane_infrastructure_operations WHERE id=$1`, o.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			// Aggregate-only fresh inventory cannot resolve the historical attempt.
			if err := f.store.AcceptClusterOperation(t.Context(), cluster, 2); !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatal("already attempted historical effect was omitted", err)
			}
			var unchanged string
			if err := f.sql.QueryRow(t.Context(), `SELECT body::text FROM loomplane_infrastructure_operations WHERE id=$1`, o.ID).Scan(&unchanged); err != nil || unchanged != original {
				t.Fatal("admission rewrote the historical body", err)
			}
			o.ResourceID = "physical-server"
			o.Actions = []provider.InfrastructureAction{{ID: "action", ResourceID: o.ResourceID, Kind: test.kind, Status: "completed"}}
			members := []provider.CapacityServer{{ID: o.ResourceID, CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(2 << 30), StorageBytes: inventoryNumber(20_000_000_000)}}
			checked := time.Now().UTC()
			switch test.resolution {
			case "lost":
				o.ResourceID = ""
				o.Actions = nil
				members = nil
			case "no_actions":
				o.Actions = nil
			case "cancelled", "succeeded":
				o.Status = test.resolution
				o.Actions = nil
			case "partial":
				o.Actions = append(o.Actions, provider.InfrastructureAction{ID: "pending", ResourceID: o.ResourceID, Kind: test.kind, Status: "in-progress"})
			case "wrong_action":
				o.Actions[0].ResourceID = "other-server"
			case "wrong_member":
				members[0].ID = "other-server"
			case "old":
				o.UpdatedAt = checked
				checked = checked.Add(-time.Millisecond)
			case "empty", "wrong_delete", "absent":
				members = nil
			}
			if err := f.store.SaveOperation(t.Context(), o); err != nil {
				t.Fatal("historical resolution checkpoint blocked", err)
			}
			if test.resolution == "absent" || test.resolution == "wrong_delete" {
				deletion := *o
				deletion.ID = "historical-delete"
				deletion.Status = "succeeded"
				deletion.Input.Kind = "delete"
				deletion.AbsenceConfirmedAt = checked.Add(-time.Second)
				if test.resolution == "wrong_delete" {
					deletion.ResourceID = "other-server"
				}
				if err := f.store.CreatePreview(t.Context(), &deletion); err != nil {
					t.Fatal(err)
				}
			}
			if test.resolution != "aggregate" {
				legacyLedgerMembership(t, f, members, test.resolution != "incomplete", checked)
			}
			if test.resolution == "null_membership" {
				if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET membership=NULL`); err != nil {
					t.Fatal(err)
				}
			}
			if test.resolution == "complete" {
				if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','1'::jsonb)`); err != nil {
					t.Fatal(err)
				}
				var blocked *cloudinfra.CapacityAdmissionError
				if err := f.store.AcceptClusterOperation(t.Context(), cluster, 2); !errors.As(err, &blocked) || blocked.Block.Reason != "provider_quota" {
					t.Fatal("settled server disappeared from account usage", err)
				}
				if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_accounts SET body=jsonb_set(body,'{quota,servers}','2'::jsonb)`); err != nil {
					t.Fatal(err)
				}
			}
			err := f.store.AcceptClusterOperation(t.Context(), cluster, 2)
			if test.allowed && err != nil {
				t.Fatal("exact historical settlement did not unblock admission", err)
			}
			if !test.allowed && !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatal("unresolved historical effect admitted", err)
			}
			saved, err := f.store.GetOperation(t.Context(), "org", "peer", o.ID)
			if err != nil || saved.CapacityTarget != nil || saved.Review != "original-approved-review" {
				t.Fatal("historical approval changed", saved, err)
			}
		})
	}
}

func TestClusterCapacityLedgerHistoricalAccountScope(t *testing.T) {
	for _, account := range []string{"account", "unrelated-account"} {
		t.Run(account, func(t *testing.T) {
			f := ledgerFixture(t)
			legacyLedgerOperation(t, f, "create", account)
			server := ledgerPreview(t, f)
			err := f.store.Accept(t.Context(), server)
			if account == "account" {
				if !errors.Is(err, cloudinfra.ErrConflict) {
					t.Fatal("server admission omitted unknown peer effect", err)
				}
			} else if err != nil {
				t.Fatal("unrelated account blocked server admission", err)
			}
		})
	}
	f := ledgerFixture(t)
	legacyLedgerOperation(t, f, "create", "unrelated-account")
	cluster := clusterLedgerPreview(t, f, "unrelated-cluster")
	if err := f.store.AcceptClusterOperation(t.Context(), cluster, 2); err != nil {
		t.Fatal("unrelated account blocked cluster admission", err)
	}
}

func TestClusterCapacityLedgerHistoricalServerRecheck(t *testing.T) {
	f := ledgerFixture(t)
	server := ledgerPreview(t, f)
	if err := f.store.Accept(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	legacyLedgerOperation(t, f, "create", "account")
	server.Attempted = true
	if err := f.store.SaveOperation(t.Context(), server); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("reserved server crossed first-effect barrier beside unknown legacy effect", err)
	}
	saved, err := f.store.GetOperation(t.Context(), "org", "cloud", server.ID)
	if err != nil || saved.Attempted {
		t.Fatal("blocked modern attempt persisted", saved, err)
	}
}

func TestClusterCapacityLedgerHistoricalClusterRecheck(t *testing.T) {
	f := ledgerFixture(t)
	cluster := clusterLedgerPreview(t, f, "historical-recheck")
	legacyLedgerAlias(t, f, cluster)
	if err := f.store.AcceptClusterOperation(t.Context(), cluster, 2); err != nil {
		t.Fatal(err)
	}
	legacyLedgerOperation(t, f, "create", "account")
	leased, err := f.store.LeaseClusterOperation(t.Context(), "org", cluster.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var blocked *cloudinfra.CapacityAdmissionError
	if err = f.store.AttemptClusterCreate(t.Context(), leased); !errors.As(err, &blocked) || blocked.Block.Resource != "account" {
		t.Fatal("accepted cluster crossed first-effect barrier beside unknown legacy effect", err)
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", cluster.ID)
	if err != nil || saved.Steps[0].State != "pending" {
		t.Fatal("blocked cluster attempt persisted", saved, err)
	}
}
