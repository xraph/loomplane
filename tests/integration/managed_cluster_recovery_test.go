//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestManagedClusterRecoveryAdmissionAndFences(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',generation=1 WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	vector := cloudinfra.ResourceVector{Servers: 2, CPUMillis: 4000, MemoryBytes: 8 << 30, StorageBytes: 160_000_000_000}
	op := &cloudinfra.ClusterOperation{ID: "operation", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "digest", Principal: security.Principal{OrgID: "org", UserID: "manager"}, Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, PolicyRevision: 1, ConnectionGeneration: 1}, CapacityTarget: vector, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "pending", RequestDigest: "digest"}, {Name: "enrollment", State: "pending", RequestDigest: "digest"}}}
	if err := store.CreateClusterPreview(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptClusterOperation(t.Context(), op, 2); err == nil {
		t.Fatal("missing policy bypassed admission")
	}
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 3, CPUMillis: 6000, MemoryBytes: 12 << 30, StorageBytes: 240_000_000_000}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES('org','cloud',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	quota := int64(10)
	if err = store.SaveClusterAccount(t.Context(), "org", "cloud", cloudinfra.CapacityAccountSnapshot{Provider: "digitalocean", AccountID: "account", CheckedAt: time.Now(), Provisioned: cloudinfra.CapacityResources{Servers: &zero, CPUMillis: &zero, MemoryBytes: &zero, StorageBytes: &zero}, Quota: provider.CapacityQuota{Servers: &quota}}); err != nil {
		t.Fatal(err)
	}
	// Two callers accepting the same preview can create only one reservation.
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			copyOp, readErr := store.GetClusterOperation(t.Context(), "org", "cloud", "operation")
			if readErr != nil {
				results <- readErr
				return
			}
			results <- store.AcceptClusterOperation(t.Context(), copyOp, 2)
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for result := range results {
		if result == nil {
			accepted++
		} else if !errors.Is(result, cloudinfra.ErrConflict) {
			t.Fatal(result)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d", accepted)
	}
	var count int
	var nodes int64
	if err = db.QueryRow(t.Context(), `SELECT count(*),sum(servers) FROM loomplane_cloud_capacity_reservations WHERE org_id='org'`).Scan(&count, &nodes); err != nil || count != 1 || nodes != 2 {
		t.Fatalf("reservations %d/%d: %v", count, nodes, err)
	}
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_infrastructure_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("cluster created fake server history", count, err)
	}
	old, err := store.LeaseClusterOperation(t.Context(), "org", "operation", "old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AttemptClusterCreate(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	// Simulate a process disappearing after its attempted marker and before its receipt.
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(body,'{lease_expires_at}',to_jsonb($1::text)) WHERE id='operation'`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	current, err := store.LeaseClusterOperation(t.Context(), "org", "operation", "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stale := *old
	stale.Revision++
	if err = store.SaveClusterCheckpoint(t.Context(), &stale, old.Revision); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale checkpoint", err)
	}
	if err = store.AttemptClusterCreate(t.Context(), current); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("attempted marker authorized second create", err)
	}
	if len(current.ReservationIDs) != 1 || current.Steps[0].State != "attempted" || current.FencingEpoch <= old.FencingEpoch {
		t.Fatal("restart lost durable state")
	}
	cancelled, err := store.ControlClusterOperation(t.Context(), "org", "cloud", "operation", true)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "waiting_provider" || !cancelled.CancelRequested {
		t.Fatal("accepted mutation cancelled without observation")
	}
	var status string
	if err = db.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id='operation'`).Scan(&status); err != nil || status != "reserved" {
		t.Fatal("ambiguous reservation released", status, err)
	}
	// The replacement can identify and record the physical cluster without enrollment.
	current, err = store.LeaseClusterOperation(t.Context(), "org", "operation", "observer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "remote", Incarnation: "physical", Ownership: "managed", Status: "running", Revision: 1, SchemaVersion: 1}
	expected := current.Revision
	current.Revision++
	current.ResourceID = resource.ID
	current.Status = "cancelled"
	current.Steps[0].State = "completed"
	current.Steps[0].RemoteResourceID = "remote"
	if err = store.CheckpointClusterResource(t.Context(), current, resource, expected); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", "operation")
	if err != nil || saved.ResourceID != "cluster" || saved.Status != "cancelled" || saved.Steps[1].State != "pending" {
		t.Fatalf("recovered resource: %+v %v", saved, err)
	}
	// Reservations survive identification and continue counting against other creates.
	second := *op
	second.ID = "second"
	second.Digest = "second"
	second.Status = "preview"
	second.Revision = 1
	if err = store.CreateClusterPreview(t.Context(), &second); err != nil {
		t.Fatal(err)
	}
	if err = store.AcceptClusterOperation(t.Context(), &second, 2); err == nil {
		t.Fatal("retained cluster reservation did not consume Cloud ceiling")
	}
}

func TestManagedClusterRecoveryAdoptionCannotOverlapCreate(t *testing.T) {
	for _, evidence := range []string{"resource", "receipt", "step"} {
		t.Run(evidence, func(t *testing.T) {
			db := managedClusterDB(t)
			store, _ := managedClusterStore(t, db)
			if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',generation=1 WHERE id='conn'`); err != nil {
				t.Fatal(err)
			}
			ref := provider.ClusterRef{ID: "remote", AccountID: "account", Region: "nyc1", Incarnation: "physical"}
			resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, CreationReceipt: &provider.ClusterMutation{OperationID: "create", Ref: ref}}
			if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			creation := &cloudinfra.ClusterOperation{ID: "create", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "create-digest", Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1}, CreatedAt: now, ExpiresAt: now.Add(time.Minute), Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "pending", RequestDigest: "create-digest"}}}
			if err := store.CreateClusterPreview(t.Context(), creation); err != nil {
				t.Fatal(err)
			}
			creation.Revision++
			creation.Status = "needs_attention"
			switch evidence {
			case "resource":
				creation.ResourceID = resource.ID
			case "receipt":
				creation.Receipts = []provider.ClusterMutation{{OperationID: "create", Ref: ref}}
			case "step":
				creation.Steps[0].RemoteResourceID = ref.ID
				creation.Steps[0].State = "observed"
			}
			if err := store.SaveClusterCheckpoint(t.Context(), creation, 1); err != nil {
				t.Fatal(err)
			}
			adoption := &cloudinfra.ClusterOperation{ID: "adopt", OrgID: "org", CloudID: "cloud", Kind: "adopt-managed", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "adopt-digest", Input: cloudinfra.ClusterInput{Kind: "adopt-managed", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1, Ref: ref, ResourceID: resource.ID, ResourceRevision: 1}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
			if err := store.CreateClusterPreview(t.Context(), adoption); err != nil {
				t.Fatal(err)
			}
			if err := store.AcceptClusterOperation(t.Context(), adoption, 2); !errors.Is(err, cloudinfra.ErrBusy) {
				t.Fatalf("active create identified by %s permitted adoption: %v", evidence, err)
			}
		})
	}
}
