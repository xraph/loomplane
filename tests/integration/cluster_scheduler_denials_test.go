//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingScheduledFailuresAreNotDemand(t *testing.T) {
	e := newEnrollmentServiceFixture(t)
	api := e.bootstrap.Kube.CoreV1()
	nodes, err := api.Nodes().List(t.Context(), metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 1 || nodes.Items[0].Name != "loomplane-managed-clusters-02-control-plane" {
		t.Fatal("dedicated kind node", err)
	}
	ns, err := api.Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "scheduler-failures-"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if cleanupErr := api.Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	pods := api.Pods(ns.Name)
	zero := int64(0)
	for _, name := range []string{"unready", "image"} {
		c := corev1.Container{Name: "app", Image: "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", ImagePullPolicy: corev1.PullIfNotPresent, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(1)}}, PeriodSeconds: 1}}
		if name == "image" {
			c.Image = "127.0.0.1:1/not-present:fixture"
			c.ImagePullPolicy = corev1.PullAlways
			c.ReadinessProbe = nil
		}
		_, err = pods.Create(t.Context(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.PodSpec{TerminationGracePeriodSeconds: &zero, Containers: []corev1.Container{c}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]string{}
	if err = wait.PollUntilContextTimeout(t.Context(), 200*time.Millisecond, 45*time.Second, true, func(ctx context.Context) (bool, error) {
		items, listErr := pods.List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return false, listErr
		}
		for _, p := range items.Items {
			var scheduled, unready bool
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue {
					scheduled = true
				}
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionFalse {
					unready = true
				}
			}
			if !scheduled || !unready || len(p.Status.ContainerStatuses) != 1 {
				continue
			}
			status := p.Status.ContainerStatuses[0]
			if p.Name == "unready" && p.Status.Phase == corev1.PodRunning && status.State.Running != nil {
				refs[p.Name] = string(p.UID)
			}
			if p.Name == "image" && status.State.Waiting != nil && (status.State.Waiting.Reason == "ErrImagePull" || status.State.Waiting.Reason == "ImagePullBackOff") {
				refs[p.Name] = string(p.UID)
			}
		}
		return len(refs) == 2, nil
	}); err != nil {
		t.Fatal(err)
	}
	in, err := kube.ReadCapacityInventory(t.Context(), e.bootstrap.Kube)
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.ClusterRef{ID: "kind", AccountID: "fixture", Incarnation: "fixture"}
	s, err := cloudinfra.MergeClusterCapacity(provider.ProviderClusterCapacity{Provider: "fixture", Ref: ref, CheckedAt: in.CheckedAt}, cloudinfra.KubernetesClusterCapacity{Provider: "fixture", Ref: ref, Inventory: in})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range s.Workloads {
		for name, uid := range refs {
			if w.PodUID == uid {
				if w.SchedulerShortage {
					t.Fatal("scheduled workload classified as cluster demand", name)
				}
				t.Log("scheduled failure", name, uid)
				delete(refs, name)
			}
		}
	}
	if len(refs) != 0 {
		t.Fatal("missing observed pods")
	}
}
