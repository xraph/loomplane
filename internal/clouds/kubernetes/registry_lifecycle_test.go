package kubernetes

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func readyRegistry(t *testing.T, c *Client, name string) {
	t.Helper()
	api := c.Kube.AppsV1().Deployments(c.Config.Namespace)
	d, err := api.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.UID = "registry-uid"
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1}
	if _, err = api.Update(t.Context(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRotationResumesAndUpdatesPulls(t *testing.T) {
	c := managedRegistryFixture(t)
	registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	readyRegistry(t, c, registry.Name)
	api := c.Kube.CoreV1().Secrets(c.Config.Namespace)
	pull := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lp-work-pull", Namespace: c.Config.Namespace, Labels: map[string]string{owned.LabelOrg: "org", owned.LabelConnection: "conn"}}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: registry.DockerConfig}}
	if _, err = api.Create(t.Context(), pull, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = c.RotateManagedRegistry(t.Context(), "org", "conn", "operation", now); !errors.Is(err, ErrRegistryPending) {
		t.Fatal(err)
	}
	readyRegistry(t, c, registry.Name)
	if err = c.RotateManagedRegistry(t.Context(), "org", "conn", "operation", now); !errors.Is(err, ErrRegistryPending) {
		t.Fatal(err)
	}
	secret, err := api.Get(t.Context(), registry.Name+"-auth", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(secret.Data["config.json"], registry.DockerConfig) || strings.Count(string(secret.Data["htpasswd"]), "\n") != 2 {
		t.Fatal("credentials did not overlap")
	}
	pull, err = api.Get(t.Context(), pull.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pull.Data[corev1.DockerConfigJsonKey], secret.Data["config.json"]) {
		t.Fatal("pull secret was not refreshed")
	}
	if err = c.RotateManagedRegistry(t.Context(), "org", "conn", "other", now); !errors.Is(err, ErrOwned) {
		t.Fatal("concurrent rotation accepted", err)
	}
	if err = c.RotateManagedRegistry(t.Context(), "org", "conn", "operation", now.Add(11*time.Minute)); !errors.Is(err, ErrRegistryPending) {
		t.Fatal(err)
	}
	readyRegistry(t, c, registry.Name)
	for range 2 {
		if err = c.RotateManagedRegistry(t.Context(), "org", "conn", "operation", now.Add(12*time.Minute)); err != nil {
			t.Fatal("completion retry", err)
		}
	}
	secret, err = api.Get(t.Context(), secret.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(secret.Data["htpasswd"]), "\n") != 1 || secret.Annotations[registryRotation] != "" {
		t.Fatal("old credential not retired")
	}
}

func TestRegistryMaintenanceFencesHelperAndResumes(t *testing.T) {
	c := managedRegistryFixture(t)
	registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	readyRegistry(t, c, registry.Name)
	pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(c.Config.Namespace).Get(t.Context(), registry.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims(c.Config.Namespace).Update(t.Context(), pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = c.BeginRegistryMaintenance(t.Context(), "org", "conn", "operation"); !errors.Is(err, ErrRegistryPending) {
			t.Fatal(err)
		}
	}
	pods, err := c.Kube.CoreV1().Pods(c.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatal(err)
	}
	pod := pods.Items[0].DeepCopy()
	pod.UID = "helper-uid"
	pod.Status.Phase = corev1.PodRunning
	if _, err = c.Kube.CoreV1().Pods(c.Config.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.BeginRegistryMaintenance(t.Context(), "org", "conn", "operation"); err != nil {
		t.Fatal(err)
	}
	pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "foreign-data"
	if _, err = c.Kube.CoreV1().Pods(c.Config.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.BeginRegistryMaintenance(t.Context(), "org", "conn", "operation"); !errors.Is(err, ErrOwned) {
		t.Fatal("foreign volume helper adopted", err)
	}
	if err = c.EndRegistryMaintenance(t.Context(), "org", "conn", "other"); !errors.Is(err, ErrOwned) {
		t.Fatal("another operation resumed registry", err)
	}
	for range 2 {
		if err = c.EndRegistryMaintenance(t.Context(), "org", "conn", "operation"); !errors.Is(err, ErrRegistryPending) {
			t.Fatal(err)
		}
	}
	readyRegistry(t, c, registry.Name)
	if err = c.EndRegistryMaintenance(t.Context(), "org", "conn", "operation"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRemovalProtectsScheduledWorkAndRetainsStorage(t *testing.T) {
	for _, kind := range []string{"cron", "daemon"} {
		t.Run(kind, func(t *testing.T) {
			c := managedRegistryFixture(t)
			registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
			if err != nil {
				t.Fatal(err)
			}
			template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: registry.Host + "/app:old"}}}}
			if kind == "cron" {
				_, err = c.Kube.BatchV1().CronJobs(c.Config.Namespace).Create(t.Context(), &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "scheduled"}, Spec: batchv1.CronJobSpec{Suspend: ptr.To(true), JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: template}}}}, metav1.CreateOptions{})
			} else {
				_, err = c.Kube.AppsV1().DaemonSets(c.Config.Namespace).Create(t.Context(), &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "daemon"}, Spec: appsv1.DaemonSetSpec{Template: template}}, metav1.CreateOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = c.RemoveManagedRegistry(t.Context(), "org", "conn", false); err == nil || errors.Is(err, ErrRegistryPending) {
				t.Fatal("workload not protected", err)
			}
			if kind == "cron" {
				err = c.Kube.BatchV1().CronJobs(c.Config.Namespace).Delete(t.Context(), "scheduled", metav1.DeleteOptions{})
			} else {
				err = c.Kube.AppsV1().DaemonSets(c.Config.Namespace).Delete(t.Context(), "daemon", metav1.DeleteOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = c.RemoveManagedRegistry(t.Context(), "org", "conn", false); !errors.Is(err, ErrRegistryPending) {
				t.Fatal(err)
			}
			if err = c.RemoveManagedRegistry(t.Context(), "org", "conn", false); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Kube.CoreV1().PersistentVolumeClaims(c.Config.Namespace).Get(t.Context(), registry.Name, metav1.GetOptions{}); err != nil {
				t.Fatal("retained data removed", err)
			}
			if _, err = c.Kube.CoreV1().Secrets(c.Config.Namespace).Get(t.Context(), registry.Name+"-auth", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("auth remains", err)
			}
		})
	}
}

func TestRegistryArchiveRejectsEscapesAndLinks(t *testing.T) {
	for i, tc := range []struct {
		name  string
		kind  byte
		valid bool
	}{{"./docker/registry/v2/blobs/x/data", tar.TypeReg, true}, {"./", tar.TypeDir, true}, {"../data", tar.TypeReg, false}, {"/docker/data", tar.TypeReg, false}, {"docker/link", tar.TypeSymlink, false}, {"docker/hard", tar.TypeLink, false}, {"lost+found", tar.TypeDir, false}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Name: tc.name, Mode: 0600, Typeflag: tc.kind}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := validateRegistryArchive(tar.NewReader(&data)); (err == nil) != tc.valid {
				t.Fatal(tc.name, err)
			}
		})
	}
}

func TestBootstrapUpgradeCompatibilityAndRuntimeFields(t *testing.T) {
	if !compatibleBootstrapUpgrade("cert-manager-v1.21.2-r1", "cert-manager-v1.21.3-r1") || compatibleBootstrapUpgrade("cert-manager-v1.20.9-r1", "cert-manager-v1.21.3-r1") || compatibleBootstrapUpgrade("cert-manager-v1.21.4-r1", "cert-manager-v1.21.3-r1") {
		t.Fatal("unsafe migration accepted")
	}
	old := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "ingress", "uid": "uid", "resourceVersion": "7"}, "spec": map[string]any{"clusterIP": "10.0.0.1", "ports": []any{map[string]any{"name": "https", "nodePort": int64(32443)}}}}}
	want := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "ingress"}, "spec": map[string]any{"ports": []any{map[string]any{"name": "https", "port": int64(443)}}}}}
	if err := preserveBootstrapRuntime(want, old); err != nil {
		t.Fatal(err)
	}
	address, _, _ := unstructured.NestedString(want.Object, "spec", "clusterIP")
	ports, _, _ := unstructured.NestedSlice(want.Object, "spec", "ports")
	if address != "10.0.0.1" || ports[0].(map[string]any)["nodePort"] != int64(32443) || want.GetResourceVersion() != "7" {
		t.Fatal("runtime allocation lost")
	}
}

func TestRegistryRemovalWaitsForOwnedTerminatingDeployment(t *testing.T) {
	c := managedRegistryFixture(t)
	registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	api := c.Kube.AppsV1().Deployments(c.Config.Namespace)
	d, err := api.Get(t.Context(), registry.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	d.DeletionTimestamp = &now
	if _, err = api.Update(t.Context(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RemoveManagedRegistry(t.Context(), "org", "conn", false); !errors.Is(err, ErrRegistryPending) {
		t.Fatal("owned deletion did not wait", err)
	}
	d.Labels[owned.LabelOrg] = "foreign"
	if _, err = api.Update(t.Context(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RemoveManagedRegistry(t.Context(), "org", "conn", false); !errors.Is(err, ErrOwned) {
		t.Fatal("foreign deletion accepted", err)
	}
}
