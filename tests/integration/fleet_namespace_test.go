//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/provider"
)

func instanceNamespaceFixture(t *testing.T) (fleetAuthorityFixture, *cloudinfra.PgStore, cloudinfra.InstanceNamespaceRecord, *cloudinfra.ClusterOperation) {
	t.Helper()
	f := newFleetAuthorityFixture(t)
	input := f.input("a")
	input.ReleaseID = f.request.ReleaseID
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.store.GetRelease(t.Context(), f.scope, input.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fleets.RenderInstance(*release, reserved.Instance, input)
	if err != nil {
		t.Fatal(err)
	}
	f.request.SnapshotDigest = snapshot.Digest
	authority, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	conn := &connections.Connection{ID: "infra", OrgID: "org", Cloud: "digitalocean", Name: "Cluster account", Account: "account", Status: connections.StatusReady, Generation: 1, Details: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	if err = connections.NewPgStore(f.db).CreateConnection(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	resource := cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra", ExternalID: "remote-cluster", Incarnation: "physical-cluster", Ownership: "external", Revision: 1, Status: "running", Observation: &provider.ClusterObservation{Ref: provider.ClusterRef{ID: "remote-cluster", AccountID: "account", Region: "region-a", Incarnation: "physical-cluster"}, State: "running", CAHash: "ca", Endpoint: "https://cluster.example.test"}}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,connection_id,external_id,incarnation,ownership,status,revision,body) VALUES('cluster','org','cloud','cluster','digitalocean','account','infra','remote-cluster','physical-cluster','external','running',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	var grantRevision int64
	if err = f.sql.QueryRow(t.Context(), `SELECT revision FROM loomplane_cloud_project_grants WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`).Scan(&grantRevision); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	pins := cloudinfra.InstanceNamespacePins{FleetID: f.scope.FleetID, InstanceID: snapshot.InstanceID, EnvironmentID: snapshot.EnvironmentID, Generation: 1, OwnershipEpoch: 1, SnapshotDigest: snapshot.Digest, QualificationID: "qualification"}
	record := cloudinfra.InstanceNamespaceRecord{OrgID: "org", ProjectID: "proj", CloudID: "cloud", ClusterID: "cluster", OperationID: id, Namespace: "instance-a", Pins: pins, CreationAuthority: *authority, CurrentAuthority: *authority, CurrentGeneration: 1, CurrentSnapshotDigest: snapshot.Digest}
	in := cloudinfra.ClusterInput{Kind: cloudinfra.InstanceNamespaceKind, Provider: "digitalocean", ConnectionID: "infra", AccountID: "account", ResourceID: "cluster", ResourceRevision: 1, ConnectionGeneration: conn.Generation, CloudRevision: 1, Ref: resource.Observation.Ref, Attachment: &cloudinfra.AttachmentInput{Instance: &pins, InstancePolicy: &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.35", CPUMillis: 1000, MemoryMiB: 512, EphemeralMiB: 128, MaxPods: 2}, RuntimeAccess: &provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, LifecycleJobs: true}, TargetSlug: "instance-a", CloudRevision: 1, ResourceRevision: 1, GrantRevision: grantRevision, TrustFingerprint: "ca", ProjectID: "proj", Namespace: "instance-a", ServiceAccountName: "runtime"}}
	raw, err = json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	operation := &cloudinfra.ClusterOperation{ID: id, OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: cloudinfra.InstanceNamespaceKind, SchemaVersion: 1, Input: in, Digest: digest, Revision: 1, Status: "accepted", Principal: f.principal, Attempt: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: authority.ExpiresAt, Steps: []cloudinfra.ClusterStep{{Name: "resolve-cluster", State: "completed", CompletedAt: &now, RequestDigest: digest}, {Name: "enroll-runtime", State: "pending", RequestDigest: digest}}}
	return f, cloudinfra.NewPgStore(f.db), record, operation
}

func namespaceTxProbe(ctx context.Context, tx driver.Tx) error {
	var one int
	return tx.QueryRow(ctx, `SELECT 1`).Scan(&one)
}

func TestInstanceNamespaceReservationReplay(t *testing.T) {
	f, store, record, operation := instanceNamespaceFixture(t)
	if _, err := store.ReserveInstanceNamespaceOperation(t.Context(), record, operation, nil); err == nil {
		t.Fatal("missing transactional authority accepted")
	}
	first, err := store.ReserveInstanceNamespaceOperation(t.Context(), record, operation, namespaceTxProbe)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := cloudinfra.NewPgStore(f.db).ReserveInstanceNamespaceOperation(t.Context(), record, operation, namespaceTxProbe)
	if err != nil || first.ID != recovered.ID {
		t.Fatal(recovered, err)
	}
	if err = store.CheckInstanceNamespaceRecord(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_namespaces SET namespace='other' WHERE operation_id=$1`, operation.ID); err == nil {
		t.Fatal("namespace identity changed")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, f.grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveInstanceNamespaceOperation(t.Context(), record, operation, namespaceTxProbe); err == nil {
		t.Fatal("revoked grant replay accepted")
	}
}

func TestInstanceNamespaceStaleEpoch(t *testing.T) {
	f, store, record, operation := instanceNamespaceFixture(t)
	if _, err := store.ReserveInstanceNamespaceOperation(t.Context(), record, operation, namespaceTxProbe); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET ownership_epoch=ownership_epoch+1 WHERE id=$1`, record.Pins.InstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckInstanceNamespaceRecord(t.Context(), record); err == nil {
		t.Fatal("stale namespace authority accepted")
	}
	operation.Revision++
	if err := store.SaveInstanceNamespaceCheckpoint(t.Context(), operation, 1, record, namespaceTxProbe); err == nil {
		t.Fatal("stale namespace checkpoint accepted")
	}
}
