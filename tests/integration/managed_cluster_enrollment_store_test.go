//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func TestManagedClusterEnrollmentPublicationAndRenewal(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean' WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", ExternalID: "remote", Incarnation: "physical", Ownership: "managed", Status: "running", Revision: 1}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	in := &cloudinfra.AttachmentInput{ProjectID: "proj", Namespace: "runtime", ServiceAccountName: "runtime", TargetSlug: "runtime", CloudRevision: 1, GrantRevision: 1, ResourceRevision: 1}
	op := &cloudinfra.ClusterOperation{ID: "enrollment", OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: "adopt", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "digest", Principal: security.Principal{OrgID: "org", UserID: "manager"}, Input: cloudinfra.ClusterInput{Attachment: in, Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1, ResourceRevision: 1}, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), Steps: []cloudinfra.ClusterStep{{Name: "provider", State: "completed", RequestDigest: "digest"}, {Name: "enrollment", State: "pending", RequestDigest: "digest"}}}
	if err := store.CreateClusterPreview(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	op.Revision++
	op.Status = "accepted"
	if err := store.SaveClusterCheckpoint(t.Context(), op, 1); err != nil {
		t.Fatal(err)
	}
	leased, err := store.LeaseClusterOperation(t.Context(), "org", op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	leased.Enrollment = &cloudinfra.ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "runtime", TargetID: "target", ResourceRevision: 1}
	if err = store.ReserveClusterEnrollment(t.Context(), leased); err != nil {
		t.Fatal(err)
	}
	if err = store.ReserveClusterEnrollment(t.Context(), leased); err != nil {
		t.Fatalf("claim resume: %v", err)
	}
	persisted, err := store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Enrollment == nil || persisted.Input.Attachment.NamespaceUID != "" {
		t.Fatal("materialization changed reviewed input")
	}
	a := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", CloudID: "cloud", ClusterID: "cluster", ProjectID: "proj", Namespace: "runtime", NamespaceUID: "ns", ServiceAccountName: "runtime", ServiceAccountUID: "sa", RuntimeConnectionID: "runtime", TrustRevision: 1, Revision: 1, CredentialVersion: 1, CredentialExpiry: now.Add(time.Hour), VaultKey: "version1", OperationID: op.ID}
	c := &connections.Connection{ID: "runtime", OrgID: "org", Cloud: connections.Kubernetes, Name: "Runtime", Method: connections.MethodManagedKubernetes, Generation: 1, Status: connections.StatusReady, CreatedAt: now, CreatedBy: "manager", Details: map[string]string{"managed_attachment_id": a.ID, "managed_project_id": "proj", "namespace_uid": "ns"}}
	leased.Status = "succeeded"
	leased.Steps[1].State = "completed"
	// A changed parent must roll back the whole publication.
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=2 WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	if err = store.PublishClusterEnrollment(t.Context(), leased, a, c); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("changed parent: %v", err)
	}
	var count int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_connections WHERE id='runtime'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial runtime publication", err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=1 WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	if err = store.PublishClusterEnrollment(t.Context(), leased, a, c); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_targets WHERE id='target' AND managed_resource_id='cluster'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("target not published", err)
	}
	first, err := store.BeginClusterCredentialRefresh(t.Context(), a, "first", "version2")
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(2 * time.Hour)
	if err = store.RecordClusterCredentialExpiry(t.Context(), a, first, expiry); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginClusterCredentialRefresh(t.Context(), a, "concurrent", "wrong-key"); err == nil {
		t.Fatal("two refresh leases")
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_credential_refresh SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.BeginClusterCredentialRefresh(t.Context(), a, "resumed", "unused-key")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.VaultKey != "unused-key" || resumed.RecoveryKey != "version2" || resumed.RecoveryExpiry == nil || !resumed.RecoveryExpiry.Equal(expiry.Truncate(time.Microsecond)) {
		t.Fatal("orphan Vault receipt was lost")
	}
	if err = store.CheckClusterCredentialRefresh(t.Context(), a, first); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("old lease survived")
	}
	if err = store.RecordClusterCredentialExpiry(t.Context(), a, resumed, *resumed.RecoveryExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_credential_refresh SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	second := resumed
	resumed, err = store.BeginClusterCredentialRefresh(t.Context(), a, "third", "final-key")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RecoveryKey != second.VaultKey {
		t.Fatal("takeover lost previous attempt")
	}
	if err = store.RecordClusterCredentialExpiry(t.Context(), a, resumed, *resumed.RecoveryExpiry); err != nil {
		t.Fatal(err)
	}
	vault := &infraSecrets{values: map[string][]byte{}}
	if err = vault.Put(t.Context(), a.OrgID, resumed.VaultKey, []byte("current-token")); err != nil {
		t.Fatal(err)
	}
	a.Revision++
	a.CredentialVersion = 2
	a.VaultKey = resumed.VaultKey
	a.CredentialExpiry = *resumed.ExpiresAt
	if err = store.FinishClusterCredentialRefresh(t.Context(), a, resumed); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err = db.QueryRow(t.Context(), `SELECT generation FROM loomplane_cloud_connections WHERE id='runtime'`).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("rotation changed reviewed generation", err)
	}
	saved, err := store.GetRuntimeAttachment(t.Context(), "org", a.ID)
	if err != nil || saved.CredentialVersion != 2 || saved.TrustRevision != 1 {
		t.Fatal("rotation lost identity", err)
	}
	// A delayed former lease can write its own key but cannot overwrite the active key or publish it.
	if err = vault.Put(t.Context(), a.OrgID, first.VaultKey, []byte("delayed-old-token")); err != nil {
		t.Fatal(err)
	}
	activeRaw, revealErr := vault.Reveal(t.Context(), a.OrgID, saved.VaultKey)
	if revealErr != nil || string(activeRaw) != "current-token" {
		t.Fatal("late writer replaced active material", revealErr)
	}
	stale := *a
	stale.Revision++
	stale.VaultKey = first.VaultKey
	if err = store.FinishClusterCredentialRefresh(t.Context(), &stale, first); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("old lease published", err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_credential_attempts SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	keys, orphanErr := store.OrphanClusterCredentialKeys(t.Context(), a)
	if orphanErr != nil || len(keys) != 2 {
		t.Fatal("orphan attempt history was lost", keys, orphanErr)
	}
	for _, key := range keys {
		if key == a.VaultKey {
			t.Fatal("active key selected for cleanup")
		}
	}
	if _, err = store.BeginClusterCredentialRefresh(t.Context(), a, "next", "in-flight"); err != nil {
		t.Fatal(err)
	}
	keys, orphanErr = store.OrphanClusterCredentialKeys(t.Context(), a)
	if orphanErr != nil {
		t.Fatal(orphanErr)
	}
	for _, key := range keys {
		if key == "in-flight" || key == a.VaultKey {
			t.Fatal("cleanup selected an active or in-flight key")
		}
	}
	// The persisted approval still has no materialized namespace UID.
	raw, err := json.Marshal(persisted.Input.Attachment)
	if err != nil {
		t.Fatal(err)
	}
	var input cloudinfra.AttachmentInput
	if err = json.Unmarshal(raw, &input); err != nil || input.NamespaceUID != "" {
		t.Fatal("approval input changed", err)
	}
}
