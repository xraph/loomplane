package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func managedRegistryFixture(t *testing.T) *Client {
	t.Helper()
	c := fakeRouteClient(t, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "retained", UID: "storage-uid"}, Provisioner: "test.csi", ReclaimPolicy: ptr.To(corev1.PersistentVolumeReclaimRetain), VolumeBindingMode: ptr.To(storagev1.VolumeBindingWaitForFirstConsumer)})
	c.Config.Details = map[string]string{"build_workers": "dedicated", "registry_hostname": "images.example.com", "namespace_uid": "namespace-uid", "storage_class": "retained", "storage_class_uid": "storage-uid", "architecture": "amd64", "managed_builds": "ready", "ingress_class": "nginx"}
	return c
}

func TestManagedRegistryProvisionRetryAndOwnership(t *testing.T) {
	c := managedRegistryFixture(t)
	first, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil || string(first.DockerConfig) != string(second.DockerConfig) || first.Password != second.Password {
		t.Fatalf("retry rotated credentials: %v", err)
	}
	ingress := getIngress(t, c, RouteObjectName(first.Name))
	if ingress.Spec.TLS[0].Hosts[0] != first.Host || ingress.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] != "0" {
		t.Fatalf("registry TLS or upload configuration absent: %+v", ingress)
	}
	cert := getCertificate(t, c, RouteObjectName(first.Name))
	if cert.issuer != HTTPIssuerName || cert.secret != ingress.Spec.TLS[0].SecretName {
		t.Fatal("registry certificate is not connected to ingress")
	}
	claim, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), first.Name, metav1.GetOptions{})
	if err != nil || claim.Spec.Resources.Requests.Storage().String() != "20Gi" || len(claim.OwnerReferences) != 0 {
		t.Fatalf("registry storage: %+v %v", claim, err)
	}
	deployment, err := c.Kube.AppsV1().Deployments("selected").Get(t.Context(), first.Name, metav1.GetOptions{})
	if err != nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken || !strings.Contains(deployment.Spec.Template.Spec.Containers[0].Image, "@sha256:") {
		t.Fatalf("registry pod: %+v %v", deployment, err)
	}
	// A changed destination cannot reuse the shared credentials.
	c.Config.Details["registry_hostname"] = "other.example.com"
	if _, err = c.EnsureManagedRegistry(t.Context(), "org", "conn"); !errors.Is(err, ErrOwned) {
		t.Fatalf("changed hostname accepted: %v", err)
	}
	c.Config.Details["registry_hostname"] = first.Host
	claim.Labels[owned.LabelOrg] = "another-org"
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.EnsureManagedRegistry(t.Context(), "org", "conn"); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign volume adopted: %v", err)
	}
}

func TestManagedRegistryPullSecretRolloutAndRemoval(t *testing.T) {
	c := managedRegistryFixture(t)
	registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelConnection: "conn", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelReplicas: "1"}
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }, WaitReady: func(context.Context, *Client, string) error { return nil }}
	spec := cpprovider.ServiceSpec{Name: "main", Image: registry.Host + "/loomplane/api@sha256:" + strings.Repeat("a", 64), Ports: []cpprovider.PortSpec{{Container: 8080}}}
	if _, err = r.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	name := WorkloadName(iid)
	pull, err := c.Kube.CoreV1().Secrets("selected").Get(t.Context(), name+"-pull", metav1.GetOptions{})
	if err != nil || pull.Type != corev1.SecretTypeDockerConfigJson || string(pull.Data[corev1.DockerConfigJsonKey]) != string(registry.DockerConfig) {
		t.Fatalf("managed pull secret missing: %v", err)
	}
	deployment, err := c.Kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || len(deployment.Spec.Template.Spec.ImagePullSecrets) != 1 || deployment.Spec.Template.Spec.ImagePullSecrets[0].Name != pull.Name {
		t.Fatalf("pod cannot pull managed image: %v", err)
	}
	r.WaitReady = func(context.Context, *Client, string) error { return errors.New("readiness failed") }
	spec.Image = "public.example.com/api@sha256:" + strings.Repeat("b", 64)
	if _, err = r.Apply(t.Context(), iid, spec, labels); err == nil {
		t.Fatal("failed rollout succeeded")
	}
	if _, err = c.Kube.CoreV1().Secrets("selected").Get(t.Context(), name+"-pull", metav1.GetOptions{}); err != nil {
		t.Fatalf("rollback lost pull credentials: %v", err)
	}
	if err = r.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Kube.CoreV1().Secrets("selected").Get(t.Context(), name+"-pull", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("workload credentials retained: %v", err)
	}
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), registry.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("service removal deleted image storage: %v", err)
	}
	if _, err = c.registrySecret(t.Context(), "org", "conn", false); err != nil {
		t.Fatalf("service removal deleted registry credentials: %v", err)
	}
}

func TestManagedRegistryDoesNotReplaceLostStorage(t *testing.T) {
	c := managedRegistryFixture(t)
	registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Delete(t.Context(), registry.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.EnsureManagedRegistry(t.Context(), "org", "conn"); err == nil || !strings.Contains(err.Error(), "lost its image volume") {
		t.Fatalf("lost registry was silently reinitialized: %v", err)
	}
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), registry.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("created an empty replacement claim")
	}
}

func TestManagedPullSecretSupportsPersistentRetries(t *testing.T) {
	r, c, labels, spec := persistentFixture()
	c.Config.Details["namespace_uid"], c.Config.Details["registry_hostname"] = "namespace-uid", "images.example.com"
	if _, err := c.registrySecret(t.Context(), labels[owned.LabelOrg], labels[owned.LabelConnection], true); err != nil {
		t.Fatal(err)
	}
	spec.Image = "images.example.com/loomplane/database@sha256:" + strings.Repeat("a", 64)
	iid := id.New(id.PrefixInstance)
	for range 2 {
		if _, err := r.Apply(t.Context(), iid, spec, labels); err != nil {
			t.Fatal(err)
		}
	}
	pull, err := c.Kube.CoreV1().Secrets("selected").Get(t.Context(), WorkloadName(iid)+"-pull", metav1.GetOptions{})
	if err != nil || pull.Labels[owned.LabelConnection] != labels[owned.LabelConnection] {
		t.Fatalf("persistent pull secret lost its connection: %v", err)
	}
}

func TestManagedPullSecretSupportsJobsAndCleanup(t *testing.T) {
	cluster, r := newJobCluster(t, 0, func(job *batchv1.Job) { job.Status.Succeeded = 1 })
	c, err := r.Session(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Details["namespace_uid"], c.Config.Details["registry_hostname"] = "namespace-uid", "images.example.com"
	spec, labels := kubernetesJobFixture()
	if _, err = c.registrySecret(t.Context(), labels[owned.LabelOrg], labels[owned.LabelConnection], true); err != nil {
		t.Fatal(err)
	}
	spec.Image = "images.example.com/loomplane/migrate@sha256:" + strings.Repeat("a", 64)
	if _, err = r.RunJob(t.Context(), jobFixtureName, spec, labels, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(cluster.created.Spec.Template.Spec.ImagePullSecrets) != 1 {
		t.Fatal("job did not reference its managed pull secret")
	}
	if _, err = c.Kube.CoreV1().Secrets("selected").Get(t.Context(), jobFixtureName+"-pull", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("job leaked pull credentials: %v", err)
	}
	if _, err = c.registrySecret(t.Context(), labels[owned.LabelOrg], labels[owned.LabelConnection], false); err != nil {
		t.Fatal("job removed shared registry authentication")
	}
}
