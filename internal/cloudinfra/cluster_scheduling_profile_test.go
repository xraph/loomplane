package cloudinfra

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

func schedulingFixture() (ClusterCapacitySnapshot, provider.ClusterObservation, kubernetes.CapacityInventory) {
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "cluster", AccountID: "account", Region: "region", Incarnation: "birth"}
	s := ClusterCapacitySnapshot{Ref: ref, CheckedAt: now, Complete: true, WorkloadsComplete: true, Nodes: []ClusterNodeCapacity{{NodeUID: "node", PoolID: "pool", Ready: true, Architecture: "amd64", Zone: "zone", Allocatable: ClusterResourceMetrics{CPUMillis: number(2000), MemoryBytes: number(2 << 30), EphemeralStorageBytes: number(10 << 30), Pods: number(20)}}}}
	o := provider.ClusterObservation{Ref: ref, Version: "1.36", ObservedAt: now, Pools: []provider.NodePoolSpec{{ID: "pool", Size: "size", Count: 1}}}
	request := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("100Mi")}
	c := corev1.Container{Name: "agent", Image: "agent:1", Resources: corev1.ResourceRequirements{Requests: request}}
	ds := appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{UID: "ds", Namespace: "system", Generation: 1}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1}}
	controller := true
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod", Namespace: "system", OwnerReferences: []metav1.OwnerReference{{UID: "ds", APIVersion: "apps/v1", Kind: "DaemonSet", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{c}}}
	in := kubernetes.CapacityInventory{Complete: true, CheckedAt: now, Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node", Labels: map[string]string{corev1.LabelInstanceTypeStable: "size"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.36"}}}}, DaemonSets: []appsv1.DaemonSet{ds}, Pods: []corev1.Pod{pod}}
	return s, o, in
}

func TestClusterScalingSchedulingProfileOverhead(t *testing.T) {
	s, o, in := schedulingFixture()
	s.Nodes[0].Allocatable.Extended = map[string]int64{"hugepages-2Mi": 0}
	busy := in.Pods[0].DeepCopy()
	busy.UID = "application"
	busy.OwnerReferences = nil
	busy.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1900m")
	in.Pods = append(in.Pods, *busy)
	profiles := BuildClusterSchedulingProfiles(s, o, in, time.Now())
	if len(profiles) != 1 || profiles[0].Available.CPUMillis != 1900 || profiles[0].Overhead.Pods != 1 {
		t.Fatalf("global application demand duplicated as future overhead: %+v", profiles)
	}
	for _, tc := range []struct {
		name string
		edit func(*ClusterCapacitySnapshot, *provider.ClusterObservation, *kubernetes.CapacityInventory)
	}{
		{"expired", func(s *ClusterCapacitySnapshot, _ *provider.ClusterObservation, _ *kubernetes.CapacityInventory) {
			s.CheckedAt = time.Now().Add(-3 * time.Minute)
		}},
		{"empty", func(s *ClusterCapacitySnapshot, o *provider.ClusterObservation, _ *kubernetes.CapacityInventory) {
			s.Nodes = nil
			o.Pools[0].Count = 0
		}},
		{"injected_sidecar", func(_ *ClusterCapacitySnapshot, _ *provider.ClusterObservation, in *kubernetes.CapacityInventory) {
			in.Pods[0].Spec.Containers = append(in.Pods[0].Spec.Containers, corev1.Container{Name: "injected"})
		}},
		{"missing_requests", func(_ *ClusterCapacitySnapshot, _ *provider.ClusterObservation, in *kubernetes.CapacityInventory) {
			in.DaemonSets[0].Spec.Template.Spec.Containers[0].Resources.Requests = nil
		}},
		{"missing_actual", func(_ *ClusterCapacitySnapshot, _ *provider.ClusterObservation, in *kubernetes.CapacityInventory) {
			in.Pods = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o, in := schedulingFixture()
			tc.edit(&s, &o, &in)
			if p := BuildClusterSchedulingProfiles(s, o, in, time.Now()); len(p) != 0 {
				t.Fatalf("unqualified profile: %+v", p)
			}
		})
	}
}
