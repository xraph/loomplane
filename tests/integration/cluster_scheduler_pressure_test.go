//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclient "k8s.io/client-go/kubernetes"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func reserveKindCPU(ctx context.Context, t *testing.T, client kubeclient.Interface, ns, image string) func() {
	t.Helper()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 1 || nodes.Items[0].Name != "loomplane-managed-clusters-02-control-plane" {
		t.Fatal("expected dedicated single-node kind fixture", err)
	}
	node := nodes.Items[0]
	free := node.Status.Allocatable.Cpu().MilliValue()
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		var cpu int64
		for _, c := range p.Spec.Containers {
			cpu += c.Resources.Requests.Cpu().MilliValue()
		}
		for _, c := range p.Spec.InitContainers {
			if c.RestartPolicy != nil {
				t.Fatal("unsupported kind fixture sidecar")
			}
			cpu = max(cpu, c.Resources.Requests.Cpu().MilliValue())
		}
		free -= cpu + p.Spec.Overhead.Cpu().MilliValue()
	}
	if free < 500 {
		t.Fatal("insufficient disposable fixture headroom", free)
	}
	fixture := client.CoreV1().Pods(ns)
	zero := int64(0)
	pod, err := fixture.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "scheduler-pressure-"}, Spec: corev1.PodSpec{TerminationGracePeriodSeconds: &zero, Containers: []corev1.Container{{Name: "pause", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(free-100, resource.DecimalSI)}}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	remove := func() {
		uid := pod.UID
		if delErr := fixture.Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &uid}}); delErr != nil {
			t.Error(delErr)
		}
	}
	removed := false
	cleanup := func() {
		if !removed {
			remove()
			removed = true
		}
	}
	t.Cleanup(cleanup)
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
		p, e := fixture.Get(call, pod.Name, metav1.GetOptions{})
		return e == nil && p.Spec.NodeName == node.Name, e
	}); err != nil {
		t.Fatal("schedule pressure", err)
	}
	t.Logf("dedicated node allocatable CPU %dm; free before pressure %dm; pressure request %dm", node.Status.Allocatable.Cpu().MilliValue(), free, free-100)
	return cleanup
}

func requireSchedulerShortage(ctx context.Context, t *testing.T, client kubeclient.Interface, dep *appsv1.Deployment, release string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(call context.Context) (bool, error) {
		sets, e := client.AppsV1().ReplicaSets(dep.Namespace).List(call, metav1.ListOptions{})
		if e != nil {
			return false, e
		}
		pods, e := client.CoreV1().Pods(dep.Namespace).List(call, metav1.ListOptions{})
		if e != nil {
			return false, e
		}
		for _, rs := range sets.Items {
			if !metav1.IsControlledBy(&rs, dep) || rs.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] != release {
				continue
			}
			for _, p := range pods.Items {
				if !metav1.IsControlledBy(&p, &rs) || p.Annotations[kube.WorkloadReleaseAnnotation] != release || p.Spec.NodeName != "" || p.Status.Phase != corev1.PodPending {
					continue
				}
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable && strings.Contains(c.Message, "Insufficient cpu") {
						t.Logf("pod %s (%s), PodScheduled=False/%s: %s", p.Name, p.UID, c.Reason, c.Message)
						return true, nil
					}
				}
			}
		}
		return false, nil
	}); err != nil {
		t.Fatal("no scheduler CPU shortage for the accepted release", err)
	}
}
