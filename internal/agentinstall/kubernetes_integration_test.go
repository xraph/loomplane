//go:build integration

package agentinstall

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// This fixture checks real API defaulting, scheduling and immutable delivery.
// It deliberately does not install SPIRE or qualify workload attestation.
func TestKubernetesLaunchAPI(t *testing.T) {
	path := os.Getenv("LOOMPLANE_AGENT_INSTALL_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("requires a disposable Kubernetes cluster")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	namespace := "loomplane-launch-" + uuid.NewString()[:8]
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	client, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ns, err := client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := client.Kube.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); err != nil {
			t.Error(err)
		}
	})
	for _, publisher := range []bool{false, true} {
		t.Run(map[bool]string{false: "consumer", true: "publisher"}[publisher], func(t *testing.T) {
			template, _, record, settings := launchFixture(t, publisher)
			plan, err := NewKubernetesPlan(namespace, uuid.NewString(), template.image, publisher)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Kube.CoreV1().ServiceAccounts(namespace).Create(ctx, plan.ServiceAccount(), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			role, binding := plan.ObserverRBAC()
			if role != nil {
				if _, err = client.Kube.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err = client.Kube.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			pod, err := client.Kube.CoreV1().Pods(namespace).Create(ctx, plan.Pod(), metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for pod.Spec.NodeName == "" {
				select {
				case <-ctx.Done():
					t.Fatal("pod did not schedule", ctx.Err())
				case <-time.After(200 * time.Millisecond):
				}
				pod, err = client.Kube.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			record.Binding.Namespace = namespace
			record.Binding.NamespaceUID = string(ns.UID)
			record.Binding.Destination.Identity.NamespaceUID = string(ns.UID)
			record.Binding.ServiceAccount = pod.Spec.ServiceAccountName
			record.Binding.PodUID = string(pod.UID)
			cm, err := plan.Deliver(ctx, client.Kube, record, settings, func(context.Context) error { return nil })
			if err != nil {
				body, _ := json.MarshalIndent(pod.Spec, "", "  ")
				t.Fatalf("deliver to defaulted pod: %v\n%s", err, body)
			}
			cm.Data[ConfigKey] = "{}"
			if _, err := client.Kube.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{}); err == nil {
				t.Fatal("Kubernetes allowed changing immutable launch data")
			}
			for _, check := range []struct {
				resource, group, verb, namespace string
				allowed                          bool
			}{
				{"endpointslices", "discovery.k8s.io", "list", namespace, publisher},
				{"endpointslices", "discovery.k8s.io", "watch", namespace, publisher},
				{"endpointslices", "discovery.k8s.io", "list", "default", false},
				{"endpointslices", "discovery.k8s.io", "update", namespace, false},
				{"secrets", "", "get", namespace, false},
				{"configmaps", "", "update", namespace, false},
				{"pods", "", "create", namespace, false},
			} {
				review, err := client.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{User: "system:serviceaccount:" + namespace + ":" + pod.Spec.ServiceAccountName, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}, ResourceAttributes: &authv1.ResourceAttributes{Group: check.group, Resource: check.resource, Verb: check.verb, Namespace: check.namespace}}}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if review.Status.Allowed != check.allowed {
					t.Fatalf("unexpected %s %s permission in %s: %t", check.verb, check.resource, check.namespace, review.Status.Allowed)
				}
			}
		})
	}
}
