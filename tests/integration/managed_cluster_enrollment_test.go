//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func TestManagedClusterEnrollmentRBACRealAPI(t *testing.T) {
	path := os.Getenv("LOOMPLANE_TEST_MANAGED_KUBECONFIG")
	if path == "" {
		t.Skip("set LOOMPLANE_TEST_MANAGED_KUBECONFIG to the disposable kind kubeconfig")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "managed-" + uuid.NewString()[:8]
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := kubernetes.EnrollmentRequest{AttachmentID: uuid.NewString(), ProjectID: "project", Namespace: namespace, ServiceAccountName: "runtime"}
	state := &kubernetes.EnrollmentState{}
	defer func() {
		_ = kubernetes.RetireRuntimeRBAC(context.Background(), bootstrap.Kube, request, *state)
		_ = bootstrap.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
	}()
	checkpoint := func(context.Context, *kubernetes.EnrollmentState) error { return nil }
	if err = kubernetes.EnrollRuntime(t.Context(), bootstrap.Kube, request, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err = kubernetes.EnrollRuntime(t.Context(), bootstrap.Kube, request, state, checkpoint); err != nil {
		t.Fatalf("resume: %v", err)
	}
	token, expiry, err := kubernetes.RequestRuntimeToken(t.Context(), bootstrap.Kube, namespace, "runtime", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !expiry.After(time.Now().Add(50 * time.Minute)) {
		t.Fatal("short token")
	}
	embedded, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	ca := embedded.Clusters[embedded.Contexts[embedded.CurrentContext].Cluster].CertificateAuthorityData
	runtimeRaw, err := kubernetes.RuntimeKubeconfig(cfg.REST.Host, ca, namespace, token)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg, err := kubernetes.ParseConfig(runtimeRaw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	runtimeClient, err := kubernetes.New(runtimeCfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtimeClient.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("runtime permissions missing: %v", result.Missing)
	}
	if err = runtimeClient.CheckRuntimeBoundaries(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = runtimeClient.Kube.CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("list namespaces should be forbidden: %v", err)
	}
	if _, err = runtimeClient.Kube.CoreV1().Namespaces().Get(t.Context(), "kube-system", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("read another namespace should be forbidden: %v", err)
	}
	replicas := int32(1)
	deployment := &apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: namespace}, Spec: apps.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}}, Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "worker"}}, Spec: core.PodSpec{Containers: []core.Container{{Name: "worker", Image: "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", ImagePullPolicy: core.PullIfNotPresent}}}}}}
	if _, err = runtimeClient.Kube.AppsV1().Deployments(namespace).Create(t.Context(), deployment, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		d, getErr := runtimeClient.Kube.AppsV1().Deployments(namespace).Get(t.Context(), "worker", metav1.GetOptions{})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if d.Status.AvailableReplicas == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never became available")
		}
		time.Sleep(time.Second)
	}
	if err = bootstrap.Kube.CoreV1().Namespaces().Delete(t.Context(), namespace, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Minute)
	for {
		_, getErr := bootstrap.Kube.CoreV1().Namespaces().Get(t.Context(), namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("namespace deletion timed out")
		}
		time.Sleep(time.Second)
	}
	if _, err = bootstrap.Kube.CoreV1().Namespaces().Create(t.Context(), &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = kubernetes.VerifyRuntimeIdentity(t.Context(), bootstrap.Kube, namespace, state.NamespaceUID, "runtime", state.ServiceAccountUID); err == nil {
		t.Fatal("recreated namespace accepted")
	}
	if err = kubernetes.EnrollRuntime(t.Context(), bootstrap.Kube, request, state, checkpoint); err == nil {
		t.Fatal("resume crossed namespace incarnation")
	}
}
