//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/testfixtures/cluster"
)

func TestAcceptanceKubernetesPersistentPostgres(t *testing.T) {
	path, runtime, client, labels, spec := cluster.New(t)
	namespace := client.Config.Namespace
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	iid := id.New(id.PrefixInstance)
	var err error
	checkInventory := func(status string) {
		t.Helper()
		volumes, readErr := client.StorageVolumes(ctx, labels[owned.LabelOrg], labels[owned.LabelConnection])
		if readErr != nil || len(volumes) != 1 || volumes[0].InstanceID != iid.String() || volumes[0].Status != status || volumes[0].CapacityBytes != 1<<30 || volumes[0].StorageClass != client.Config.Details["storage_class"] {
			t.Fatalf("persistent storage inventory: %+v %v", volumes, readErr)
		}
	}

	if _, err = runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	pod := kubernetes.WorkloadName(iid) + "-0"
	query := func(sql string) string {
		return fixtureCommand(t, "kubectl", "--kubeconfig", path, "-n", namespace, "exec", pod, "--", "psql", "-U", "postgres", "-d", "database", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	if got := query("CREATE TABLE persistence_probe(value text); INSERT INTO persistence_probe VALUES ('survived-kind'); SELECT value FROM persistence_probe"); !strings.Contains(got, "survived-kind") {
		t.Fatal(got)
	}
	claimName := owned.PersistentVolumeName(iid, "data")
	claim, err := client.Kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claim.UID == "" || claim.Status.Phase != corev1.ClaimBound {
		t.Fatalf("claim not bound: %+v", claim)
	}
	checkInventory("in-use")
	pv, err := client.Kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatal("provisioner ignored retention")
	}
	before, err := client.Kube.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Kube.CoreV1().Pods(namespace).Delete(ctx, pod, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		current, getErr := client.Kube.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
		ready := false
		if getErr == nil && current.UID != before.UID {
			for _, condition := range current.Status.Conditions {
				ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement pod did not become ready")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if query("SELECT value FROM persistence_probe") != "survived-kind" {
		t.Fatal("pod replacement lost data")
	}
	labels[owned.LabelRevision] = "replacement"
	if _, err = runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if query("SELECT value FROM persistence_probe") != "survived-kind" {
		t.Fatal("runtime replacement lost data")
	}
	if err = runtime.Remove(ctx, iid, labels); err != nil {
		t.Fatal(err)
	}
	kept, err := client.Kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil || kept.UID != claim.UID {
		t.Fatalf("removal lost retained claim: %v", err)
	}
	checkInventory("retained")
	if _, err = runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if query("SELECT value FROM persistence_probe") != "survived-kind" {
		t.Fatal("reattachment lost data")
	}
	checkInventory("in-use")
	kept, err = client.Kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil || kept.UID != claim.UID {
		t.Fatalf("reattachment changed claim: %v", err)
	}
	t.Logf("PostgreSQL row survived pod replacement, rollout, removal and reattachment; PVC %s, PV %s, provisioner %s", kept.UID, kept.Spec.VolumeName, client.Config.Details["storage_provisioner"])
}
