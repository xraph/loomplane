//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ctrlplane/id"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/testfixtures/cluster"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceKubernetesCatalogRecovery(t *testing.T) {
	path, _, admin, _, _ := cluster.New(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Storage.URL = "file://" + t.TempDir()
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection struct {
		ID, Status string
		Details    map[string]string
		Missing    []string
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "kubernetes", "name": "Kubernetes catalog acceptance", "credential": string(raw), "settings": map[string]string{"namespace": admin.Config.Namespace, "storage_class": admin.Config.Details["storage_class"], "architecture": "arm64"}}, &connection)
	if connection.Status != "ready" || connection.Details["postgres_recovery_permissions"] != "ready" {
		t.Fatalf("database target prerequisites failed: %+v", connection)
	}
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Kubernetes catalog fixture", "access": "write"}, &key)
	lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	previous := lpclient.Every
	lpclient.Every = 200 * time.Millisecond
	defer func() { lpclient.Every = previous }()
	var plan lpclient.Plan
	if err = json.Unmarshal([]byte(lc.must("services", "create", "Cluster database", "--provider", "kubernetes", "--target", connection.ID, "--json")), &plan); err != nil || !plan.Executable {
		t.Fatalf("cluster catalog plan: %+v %v", plan, err)
	}
	lc.must("approve", plan.ID, "--yes")
	base := "/api/v1/projects/" + plan.ProjectID
	var bindings struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 {
		t.Fatalf("cluster binding missing: %+v", bindings)
	}
	source, err := id.Parse(bindings.Bindings[0].ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewed planning.ResourceConfig
	if err = json.Unmarshal(bindings.Bindings[0].Config, &reviewed); err != nil || reviewed.Catalog == nil || reviewed.Catalog.VolumeSizeMB != 10240 {
		t.Fatalf("lost reviewed volume capacity: %+v %v", reviewed, err)
	}
	ns := admin.Config.Namespace
	sql := func(iid id.ID, statement string) string {
		return cluster.Command(t, "kubectl", "--kubeconfig", path, "-n", ns, "exec", kubernetes.WorkloadName(iid)+"-0", "--", "psql", "-U", "postgres", "-d", "database", "-At", "-v", "ON_ERROR_STOP=1", "-c", statement)
	}
	sql(source, "SET ROLE app; CREATE TABLE catalog_cluster_probe(value text); INSERT INTO catalog_cluster_probe VALUES ('cluster-recovery-row')")
	claim, err := admin.Kube.CoreV1().PersistentVolumeClaims(ns).Get(t.Context(), owned.PersistentVolumeName(source, "data"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Resources.Requests.Storage().Value() != 10<<30 {
		t.Fatalf("PVC capacity differs from approved recipe: %s", claim.Spec.Resources.Requests.Storage())
	}
	pod, err := admin.Kube.CoreV1().Pods(ns).Get(t.Context(), kubernetes.WorkloadName(source)+"-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.Kube.CoreV1().Pods(ns).Delete(t.Context(), pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		next, getErr := admin.Kube.CoreV1().Pods(ns).Get(t.Context(), pod.Name, metav1.GetOptions{})
		if getErr == nil && next.UID != pod.UID && len(next.Status.ContainerStatuses) == 1 && next.Status.ContainerStatuses[0].Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("source replacement did not settle: %v", getErr)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
	if got := sql(source, "SELECT value FROM catalog_cluster_probe"); got != "cluster-recovery-row" {
		t.Fatalf("pod replacement lost row: %s", got)
	}
	var backup lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(lc.must("backups", "create", plan.ProjectID, "--request-id", "cluster-backup", "--wait", "--json")), &backup); err != nil || backup.Status != "succeeded" || backup.Bytes == 0 {
		t.Fatalf("cluster backup: %+v %v", backup, err)
	}
	lc.must("backups", "policy", plan.ProjectID, "--every-hours", "24")
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if _, err = db.Exec(t.Context(), "UPDATE loomplane_backup_policies SET next_at=now()-interval '1 second' WHERE project_id=$1", plan.ProjectID); err != nil {
		t.Fatal(err)
	}
	running.stop()
	running = start(t, cfg)
	c.base, lc.url = running.srv.URL, running.srv.URL
	var persisted lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(lc.must("backups", "show", backup.ID, "--json")), &persisted); err != nil || persisted.SHA256 != backup.SHA256 {
		t.Fatalf("restart lost backup evidence: %+v %v", persisted, err)
	}
	sql(source, "SET ROLE app; DELETE FROM catalog_cluster_probe")
	var restored lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(lc.must("restores", "create", backup.ID, "--request-id", "cluster-restore", "--wait", "--json")), &restored); err != nil || restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("cluster isolated restore: %+v %v", restored, err)
	}
	copyID, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sql(copyID, "SET ROLE app; SELECT value FROM catalog_cluster_probe"); !strings.HasSuffix(got, "cluster-recovery-row") {
		t.Fatalf("recovered row missing: %s", got)
	}
	if got := sql(source, "SELECT count(*) FROM catalog_cluster_probe"); got != "0" {
		t.Fatalf("restore changed source: %s", got)
	}
	copyClaim, err := admin.Kube.CoreV1().PersistentVolumeClaims(ns).Get(t.Context(), owned.PersistentVolumeName(copyID, "data"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc.must("restores", "delete", restored.ID, "--yes")
	lc.must("restores", "delete", restored.ID, "--yes")
	if _, err = admin.Kube.CoreV1().PersistentVolumes().Get(t.Context(), copyClaim.Spec.VolumeName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("temporary PV survived API cleanup: %v", err)
	}
	kept, err := admin.Kube.CoreV1().PersistentVolumeClaims(ns).Get(t.Context(), claim.Name, metav1.GetOptions{})
	if err != nil || kept.UID != claim.UID {
		t.Fatalf("cleanup changed source PVC: %v", err)
	}
	deadline = time.Now().Add(90 * time.Second)
	for {
		var scheduled []lpclient.ServiceBackup
		if err = json.Unmarshal([]byte(lc.must("backups", plan.ProjectID, "--json")), &scheduled); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, point := range scheduled {
			if point.ID != backup.ID && point.Status == "succeeded" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("persisted schedule failed after restart")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	lc.must("backups", "policy", plan.ProjectID, "--every-hours", "24", "--disable")
	var readerKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Cluster recovery reader", "access": "read"}, &readerKey)
	reader := loomctl{t: t, url: running.srv.URL, key: readerKey.Secret, config: filepath.Join(t.TempDir(), "reader.json")}
	reader.must("backups", "show", backup.ID, "--json")
	if _, denied := reader.run("", "restores", "create", backup.ID, "--request-id", "reader-denied", "--json"); denied == nil {
		t.Fatal("read-only key requested recovery")
	}
	t.Logf("Kubernetes catalog plan %s deployed PostgreSQL, preserved a row across pod replacement, persisted Trove backup after restart, restored an isolated copy and deleted its exact PV; scheduled backup and read-only denial passed", plan.ID)
	holdCatalogBrowser(t, running.srv.URL, plan.ProjectID, connection.ID)
}
