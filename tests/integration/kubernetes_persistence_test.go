//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestAcceptanceKubernetesPersistentPostgres(t *testing.T) {
	path := os.Getenv("LOOMPLANE_PERSISTENCE_KUBECONFIG")
	if path == "" {
		t.Skip("set LOOMPLANE_PERSISTENCE_KUBECONFIG for the disposable kind persistence target")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(id.New(id.PrefixInstance).String(), "_", "-")
	namespace := "persistence-" + suffix
	className := namespace
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if _, err = client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"loomplane.acceptance": suffix}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if deleteErr := client.Kube.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); deleteErr != nil {
			t.Error(deleteErr)
		}
		if deleteErr := client.Kube.StorageV1().StorageClasses().Delete(cleanup, className, metav1.DeleteOptions{}); deleteErr != nil {
			t.Error(deleteErr)
		}
	})
	base, err := client.Kube.StorageV1().StorageClasses().Get(ctx, "standard", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	retain := corev1.PersistentVolumeReclaimRetain
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	sc, err := client.Kube.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: className, Labels: map[string]string{"loomplane.acceptance": suffix}}, Provisioner: base.Provisioner, Parameters: base.Parameters, ReclaimPolicy: &retain, VolumeBindingMode: &binding}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Details["storage_class"] = sc.Name
	client.Config.Details["storage_class_uid"] = string(sc.UID)
	runtime := &kubernetes.Runtime{Session: func(context.Context, map[string]string) (*kubernetes.Client, error) { return client, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"POSTGRES_PASSWORD": "fixture-private-password"}}, nil
	}, Wait: 2 * time.Minute}
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "fixture-org", owned.LabelProject: suffix, owned.LabelEnvironment: "production", owned.LabelConnection: "fixture-kind", owned.LabelStateful: "true", owned.LabelArchitecture: "arm64", owned.LabelRevision: "first"}
	spec := cpprovider.ServiceSpec{Name: "database", Image: "loomplane-persistence-test:17.11", Env: map[string]string{"POSTGRES_DB": "database"}, Ports: []cpprovider.PortSpec{{Container: 5432, Protocol: "tcp"}}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/var/lib/postgresql/data", SizeMB: 1024}}}
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
	if _, err = runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if query("SELECT value FROM persistence_probe") != "survived-kind" {
		t.Fatal("reattachment lost data")
	}
	kept, err = client.Kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil || kept.UID != claim.UID {
		t.Fatalf("reattachment changed claim: %v", err)
	}
	t.Logf("PostgreSQL row survived pod replacement, rollout, removal and reattachment; PVC %s, PV %s, provisioner %s", kept.UID, kept.Spec.VolumeName, sc.Provisioner)
}
