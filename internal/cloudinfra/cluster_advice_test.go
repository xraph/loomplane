package cloudinfra

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingLowDemandRecommendation(t *testing.T) {
	vacant := int64(0)
	occupied := int64(1)
	p := ClusterScalingPolicy{PoolID: "p", MinNodes: 1}
	s := ClusterCapacitySnapshot{Nodes: []ClusterNodeCapacity{{PoolID: "p", Ready: true, NodeUID: "one", Requests: ClusterResourceMetrics{Pods: &vacant}}, {PoolID: "p", Ready: true, NodeUID: "two", Requests: ClusterResourceMetrics{Pods: &occupied}}}, SchedulingProfiles: []ClusterSchedulingProfile{{PoolID: "p", Available: ClusterWorkloadVector{CPUMillis: 1000, Pods: 3}}}, Workloads: []ClusterObservedWorkload{{Requests: ClusterWorkloadVector{CPUMillis: 100, Pods: 1}}}}
	now := time.Now().UTC()
	s.StorageProvenance.CheckedAt = now
	s.Nodes[0].Provenance = map[string]ClusterMetricProvenance{"requests/pods": {Source: "kubernetes", Identity: "one", CheckedAt: now}}
	if !poolCouldShrink(s, ClusterWorkloadVector{CPUMillis: 100, Pods: 1}, p) {
		t.Fatal("low requests didn't qualify")
	}
	charged := ClusterWorkloadVector{CPUMillis: 1050, Pods: 1}
	if poolCouldShrink(s, charged, p) {
		t.Fatal("ignored guaranteed maximum")
	}
	s.Nodes[0].Requests.Pods = &occupied
	if poolCouldShrink(s, ClusterWorkloadVector{CPUMillis: 100, Pods: 1}, p) {
		t.Fatal("all nodes need drain")
	}
	s.Nodes[0].Requests.Pods = &vacant
	s.Volumes = []ClusterVolumeCapacity{{UID: "retained"}}
	if poolCouldShrink(s, ClusterWorkloadVector{CPUMillis: 100, Pods: 1}, p) {
		t.Fatal("retained volume")
	}
	s.Volumes = nil
	p.MinNodes = 2
	if poolCouldShrink(s, ClusterWorkloadVector{CPUMillis: 100, Pods: 1}, p) {
		t.Fatal("minimum floor")
	}
	p.MinNodes = 1
	s.Workloads[0].Pending = true
	if poolCouldShrink(s, ClusterWorkloadVector{CPUMillis: 100, Pods: 1}, p) {
		t.Fatal("pending demand allowed recommendation")
	}
	for _, v := range []struct {
		e     error
		delay time.Duration
	}{{&provider.InfrastructureAPIError{Status: 429, RetryAfter: 13 * time.Minute}, 13 * time.Minute}, {&provider.InfrastructureAPIError{Status: 429}, time.Minute}, {&provider.InfrastructureAPIError{Status: 401}, 0}} {
		if providerObservationDelay(v.e) != v.delay {
			t.Fatal(v)
		}
	}
}

func TestClusterScalingPendingIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "image", "request", "custom-scheduler", "affinity", "quota", "readiness", "image-pull", "missing-owner", "mixed-scheduling", "both-custom", "both-selector"} {
		t.Run(mode, func(t *testing.T) {
			in := workloadObservationFixture()
			p := in.Pods[0].DeepCopy()
			p.Status.Phase = corev1.PodPending
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: "0/1 nodes are available: 1 Insufficient memory. preemption: 0/1 nodes are available."}}
			switch mode {
			case "image":
				p.Spec.Containers[0].Image = "drift"
			case "request":
				p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1")
			case "custom-scheduler":
				p.Spec.SchedulerName = "other"
			case "affinity":
				p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}}
			case "both-custom":
				p.Spec.SchedulerName = "other"
				in.Deployments[0].Spec.Template.Spec.SchedulerName = "other"
				in.ReplicaSets[0].Spec.Template.Spec.SchedulerName = "other"
			case "both-selector":
				p.Spec.NodeSelector = map[string]string{"tenant": "other"}
				in.Deployments[0].Spec.Template.Spec.NodeSelector = p.Spec.NodeSelector
				in.ReplicaSets[0].Spec.Template.Spec.NodeSelector = p.Spec.NodeSelector
			case "quota":
				p.Status.Conditions[0].Message = "exceeded quota"
			case "readiness", "image-pull":
				p.Spec.NodeName = "worker"
				p.Status.Conditions[0].Status = corev1.ConditionTrue
			case "missing-owner":
				p.OwnerReferences = nil
			case "mixed-scheduling":
				p.Status.Conditions[0].Message = "0/2 nodes are available: 1 Insufficient cpu, 1 node(s) had untolerated taint."
			}
			in.Pods = []corev1.Pod{*p}
			ws, complete, err := clusterWorkloadObservations(in)
			if err != nil || !complete || len(ws) != 1 || (ws[0].SchedulerShortage && ws[0].ReleaseID != "") != (mode == "valid") {
				t.Fatal("scheduler demand", ws, complete, err)
			}
		})
	}
}

func TestClusterScalingPendingMatchesMaximum(t *testing.T) {
	actual := ClusterWorkloadVector{CPUMillis: 500, MemoryBytes: 128 << 20, Pods: 1}
	maximum := ClusterWorkloadVector{CPUMillis: 1500, MemoryBytes: 384 << 20, Pods: 3}
	if !matchesReviewedPod(actual, maximum) {
		t.Fatal("reviewed pod rejected")
	}
	maximum.CPUMillis++
	if matchesReviewedPod(actual, maximum) {
		t.Fatal("fractional per-pod review accepted")
	}
	actual.CPUMillis = 1 << 62
	maximum.CPUMillis = 1500
	if matchesReviewedPod(actual, maximum) {
		t.Fatal("oversized pod accepted")
	}
}
