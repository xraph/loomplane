//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
)

type retirementVault struct {
	connections.Secrets
	fail bool
}

func (v *retirementVault) Delete(ctx context.Context, org, key string) error {
	if v.fail {
		return errors.New("injected Vault failure")
	}
	return v.Secrets.Delete(ctx, org, key)
}

func TestManagedClusterRetirementDetachRetainsExternalData(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	vault := &retirementVault{Secrets: f.vault().Secrets, fail: true}
	svc, credentials := f.service(f.store, vault)
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	external, err := f.bootstrap.Kube.CoreV1().ConfigMaps(a.Namespace).Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "external-data"}, Data: map[string]string{"keep": "yes"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	o, err := svc.PreviewRuntimeDetach(t.Context(), f.principal, "cloud", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Digest}); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "needs_attention" {
		t.Fatalf("Vault failure status=%s reason=%s", saved.Status, saved.Error)
	}
	current, err := f.store.GetRuntimeAttachment(t.Context(), "org", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeletedAt == nil || current.RetirementOperationID != o.ID {
		t.Fatal("attachment was not atomically disabled")
	}
	var targets int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_targets WHERE connection_id=$1`, a.RuntimeConnectionID).Scan(&targets); err != nil || targets != 0 {
		t.Fatal(targets, err)
	}
	if _, _, err = credentials.Credential(t.Context(), "org", a.RuntimeConnectionID); err == nil {
		t.Fatal("detached runtime credential issued")
	}
	retained, err := f.bootstrap.Kube.CoreV1().ConfigMaps(a.Namespace).Get(t.Context(), external.Name, metav1.GetOptions{})
	if err != nil || retained.UID != external.UID {
		t.Fatal("external object changed", err)
	}
	if _, err = f.bootstrap.Kube.CoreV1().ServiceAccounts(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("owned service account retained", err)
	}
	vault.fail = false
	if _, err = svc.RetryCluster(t.Context(), f.principal, "cloud", o.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = f.store.GetClusterOperation(t.Context(), "org", "cloud", o.ID)
	if err != nil || saved.Status != "succeeded" {
		t.Fatal(saved.Status, saved.Error, err)
	}
	var receiptRevision int64
	if err = f.sql.QueryRow(t.Context(), `SELECT revision FROM loomplane_cluster_retirement_receipts WHERE operation_id=$1`, o.ID).Scan(&receiptRevision); err != nil || receiptRevision != saved.Revision {
		t.Fatal("durable retirement receipt", receiptRevision, err)
	}
}

func TestManagedClusterRetirementDependencyInventory(t *testing.T) {
	f, o, _ := maintenanceAdmissionFixture(t)
	a, err := f.store.GetRuntimeAttachment(t.Context(), "org", "attached")
	if err != nil {
		t.Fatal(err)
	}
	a.ID = "second"
	a.Namespace = "second"
	a.NamespaceUID = "second-uid"
	a.RuntimeConnectionID = "runtime-2"
	a.ServiceAccountUID = "second-sa"
	if err = f.store.SaveRuntimeAttachment(t.Context(), a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org','runtime-1','domain_route','route')`); err != nil {
		t.Fatal(err)
	}
	deps, err := f.store.ClusterRetirementDependencies(t.Context(), "org", o.Input.ResourceID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"attachment:attached": false, "attachment:second": false, "connection_use:domain_route:route": false}
	for _, dep := range deps {
		if _, ok := wanted[dep]; ok {
			wanted[dep] = true
		}
	}
	for dep, found := range wanted {
		if !found {
			t.Fatal("missing dependency", dep, deps)
		}
	}
}

func TestManagedClusterRetirementExpiredWorkerCannotDisable(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, _ := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	o, err := svc.PreviewRuntimeDetach(t.Context(), f.principal, "cloud", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Digest}); err != nil {
		t.Fatal(err)
	}
	o, err = f.store.LeaseClusterOperation(t.Context(), "org", o.ID, "old", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)) WHERE id=$1`, o.ID, time.Now().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = f.store.DisableClusterAttachment(t.Context(), o); err == nil {
		t.Fatal("expired worker disabled runtime")
	}
	current, err := f.store.GetRuntimeAttachment(t.Context(), "org", a.ID)
	if err != nil || current.DeletedAt != nil {
		t.Fatal("expired transaction was not rolled back", err)
	}
}

type retirementCrashStore struct {
	*cloudinfra.PgStore
	fail bool
}

func (s *retirementCrashStore) SaveClusterCheckpoint(ctx context.Context, o *cloudinfra.ClusterOperation, expected int64) error {
	if s.fail && o.Kind == "detach-runtime" {
		for _, step := range o.Steps {
			if step.State == "observed" {
				s.fail = false
				return errors.New("injected checkpoint loss after exact UID delete")
			}
		}
	}
	return s.PgStore.SaveClusterCheckpoint(ctx, o, expected)
}
func TestManagedClusterRetirementDetachCrashReceipts(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	vault := f.vault().Secrets
	svc, _ := f.service(f.store, vault)
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	o, err := svc.PreviewRuntimeDetach(t.Context(), f.principal, "cloud", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Digest}); err != nil {
		t.Fatal(err)
	}
	svc.Clusters = &retirementCrashStore{PgStore: f.store, fail: true}
	if err = svc.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err == nil {
		t.Fatal("checkpoint fault not reached")
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Steps[1].State != "attempted" || saved.Steps[1].AttemptedAt == nil || saved.Steps[1].RemoteResourceID != a.RBACObjects[0].UID {
		t.Fatal("missing exact UID attempt", saved.Steps[1])
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)) WHERE id=$1`, o.ID, time.Now().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	restarted, _ := f.service(f.store, vault)
	if err = restarted.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = f.store.GetClusterOperation(t.Context(), "org", "cloud", o.ID)
	if err != nil || saved.Status != "succeeded" {
		t.Fatal(saved.Status, saved.Error, err)
	}
	for _, step := range saved.Steps[1 : len(saved.Steps)-1] {
		if step.State != "completed" || step.AttemptedAt == nil || step.RemoteResourceID == "" {
			t.Fatal("lost per-object cleanup receipt", step)
		}
	}
}
