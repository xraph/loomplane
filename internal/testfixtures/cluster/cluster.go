//go:build integration

// Package cluster provides disposable, explicitly selected Kubernetes test targets.
package cluster

import (
	"context"
	"os"
	"os/exec"
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

func New(t *testing.T) (kubeconfig string, runtime *kubernetes.Runtime, client *kubernetes.Client, labels map[string]string, spec cpprovider.ServiceSpec) {
	t.Helper()
	path := os.Getenv("LOOMPLANE_PERSISTENCE_KUBECONFIG")
	if path == "" {
		t.Skip("set LOOMPLANE_PERSISTENCE_KUBECONFIG for the disposable kind persistence target")
	}
	image := os.Getenv("LOOMPLANE_PERSISTENCE_POSTGRES_IMAGE")
	if image == "" {
		image = "loomplane-persistence-test:17.11"
	}
	raw, err := os.ReadFile(path) //nolint:gosec // explicitly selected disposable test kubeconfig
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
	client, err = kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
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
	client.Config.Details["storage_provisioner"] = sc.Provisioner
	checkedNamespace, err := client.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Details["namespace_uid"] = string(checkedNamespace.UID)
	runtime = &kubernetes.Runtime{Session: func(context.Context, map[string]string) (*kubernetes.Client, error) { return client, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"POSTGRES_PASSWORD": "fixture-private-password"}}, nil
	}, Wait: 2 * time.Minute}
	labels = map[string]string{owned.LabelOrg: "fixture-org", owned.LabelProject: suffix, owned.LabelEnvironment: "production", owned.LabelConnection: "fixture-kind", owned.LabelStateful: "true", owned.LabelArchitecture: "arm64", owned.LabelRevision: "first", owned.LabelPublic: "false"}
	spec = cpprovider.ServiceSpec{Name: "database", Image: image, Env: map[string]string{"POSTGRES_DB": "database"}, Ports: []cpprovider.PortSpec{{Container: 5432, Protocol: "tcp"}}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/var/lib/postgresql/data", SizeMB: 1024}}}

	return path, runtime, client, labels, spec
}

func Command(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), args[0], args[1:]...) //nolint:gosec // fixed fixture commands and arguments supplied by test callers
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture command %s failed: %v %s", args[0], err, output)
	}
	return strings.TrimSpace(string(output))
}
