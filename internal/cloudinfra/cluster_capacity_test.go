package cloudinfra

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterCapacityUnknownIsNotZero(t *testing.T) {
	now := time.Now().UTC()
	usage, err := summarizeCapacity([]CapacityRecord{{Kind: "cluster_node", Provider: "digitalocean", AccountID: "a", ResourceID: "server:42", CheckedAt: now.Add(-3 * time.Minute), Resources: capacityResources(ResourceVector{Servers: 1, CPUMillis: 2000})}}, nil, CapacityPolicy{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Fresh {
		t.Fatal("cluster inventory older than two minutes was accepted as fresh")
	}
}

func clusterCapacityFixture() (provider.ProviderClusterCapacity, KubernetesClusterCapacity) {
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "c", AccountID: "a", Region: "nyc1", Incarnation: "birth"}
	p := provider.ProviderClusterCapacity{Provider: "digitalocean", Ref: ref, CheckedAt: now, Complete: true, Nodes: []provider.ClusterCapacityNode{{ExternalVMID: "42", PoolID: "pool", CPUMillis: number(2000), MemoryBytes: number(4096), EphemeralStorageBytes: number(8192)}}}
	k := KubernetesClusterCapacity{Provider: p.Provider, Ref: ref, Inventory: kubernetes.CapacityInventory{CheckedAt: now, Complete: true, Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-1"}, Spec: corev1.NodeSpec{ProviderID: "digitalocean://42"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64"}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("8Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}}}}
	return p, k
}
func TestClusterCapacityDeduplicatesDroplets(t *testing.T) {
	p, k := clusterCapacityFixture()
	for _, namespace := range []string{"one", "two"} {
		k.Inventory.Pods = append(k.Inventory.Pods, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "web"}, Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}})
	}
	snapshot, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ResourceID = "resource"
	records := deduplicateClusterCapacity([]CapacityRecord{{Kind: "server", Provider: "digitalocean", AccountID: "a", ResourceID: "server:42", Resources: capacityResources(ResourceVector{Servers: 1, CPUMillis: 2000})}}, clusterCapacityRecords(snapshot))
	if len(records) != 1 {
		t.Fatalf("VM counted %d times", len(records))
	}
	usage, err := summarizeCapacity(records, nil, CapacityPolicy{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if *usage.Provisioned.Servers != 1 || *usage.Records[0].ClusterNode.Allocatable.CPUMillis != 2000 {
		t.Fatalf("namespace duplication: %+v", usage)
	}
	if *snapshot.Nodes[0].Requests.CPUMillis != 200 {
		t.Fatal("cross namespace requests not counted")
	}
}
func TestClusterCapacityNodeReincarnation(t *testing.T) {
	p, k := clusterCapacityFixture()
	first, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	k.Inventory.Nodes[0].UID = "uid-2"
	second, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	if first.Nodes[0].NodeUID == second.Nodes[0].NodeUID || second.Nodes[0].Provenance["allocatable/cpu"].Identity != "uid-2" {
		t.Fatal("reused name merged old UID")
	}
	k.Inventory.Nodes = append(k.Inventory.Nodes, k.Inventory.Nodes[0])
	snapshot, err := MergeClusterCapacity(p, k)
	if err == nil && snapshot.Complete {
		t.Fatal("duplicate runtime node accepted")
	}
}
func TestClusterCapacityNoCrossAccountJoin(t *testing.T) {
	p, k := clusterCapacityFixture()
	k.Ref.AccountID = "other"
	if _, err := MergeClusterCapacity(p, k); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
}
func TestClusterCapacityIncompleteAndConstraints(t *testing.T) {
	p, k := clusterCapacityFixture()
	k.Inventory.Complete = false
	snapshot, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Fresh(time.Now()) {
		t.Fatal("partial list permitted automatic decisions")
	}
	k.Inventory.Complete = true
	k.Inventory.Nodes[0].Spec.ProviderID = "digitalocean://43"
	snapshot, err = MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete {
		t.Fatal("unmapped VM accepted")
	}
	p, k = clusterCapacityFixture()
	k.Inventory.Nodes[0].Labels = map[string]string{corev1.LabelTopologyZone: "zone-a"}
	k.Inventory.Nodes[0].Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}
	k.Inventory.Nodes[0].Status.Allocatable["nvidia.com/gpu"] = resource.MustParse("1")
	k.Inventory.StorageClasses = []storagev1.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: "zoned"}, AllowedTopologies: []corev1.TopologySelectorTerm{{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{Key: "topology.kubernetes.io/zone", Values: []string{"zone-a"}}}}}}}
	snapshot, err = MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Nodes[0].Zone != "zone-a" || snapshot.Nodes[0].Architecture != "arm64" || len(snapshot.Nodes[0].Taints) != 1 || snapshot.Nodes[0].GPUKinds["nvidia.com/gpu"] != 1 || snapshot.StorageClasses[0].Topology[0].Expressions[0].Values[0] != "zone-a" {
		t.Fatal("scheduling constraints lost")
	}
	if snapshot.Fresh(p.CheckedAt.Add(2*time.Minute + time.Nanosecond)) {
		t.Fatal("stale facts accepted")
	}
}

func TestClusterCapacityMetricUnitsAndUnknown(t *testing.T) {
	metrics, err := clusterResourceMetrics(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("1G"), corev1.ResourcePods: resource.MustParse("110"), "nvidia.com/gpu": resource.MustParse("2")})
	if err != nil {
		t.Fatal(err)
	}
	if *metrics.CPUMillis != 250 || *metrics.MemoryBytes != 1<<30 || *metrics.EphemeralStorageBytes != 1_000_000_000 || *metrics.Pods != 110 || metrics.Extended["nvidia.com/gpu"] != 2 {
		t.Fatalf("wrong units: %+v", metrics)
	}
	unknown, err := clusterResourceMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cpu_millis":null`) || unknown.MemoryBytes != nil {
		t.Fatal("unknown converted to zero", string(raw))
	}
	for _, input := range []corev1.ResourceList{{corev1.ResourceCPU: resource.MustParse("9223372036854775807")}, {corev1.ResourceMemory: resource.MustParse("-1")}, {"nvidia.com/gpu": resource.MustParse("500m")}} {
		if _, err = clusterResourceMetrics(input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid quantity accepted", input, err)
		}
	}
}
func TestClusterCapacitySerializationOmitsRawSecrets(t *testing.T) {
	p, k := clusterCapacityFixture()
	k.Inventory.Volumes = []corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "data", UID: "volume", Annotations: map[string]string{"private": "CANARY_ANNOTATION"}}, Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "disk.csi", VolumeHandle: "volume-id", VolumeAttributes: map[string]string{"password": "CANARY_ATTRIBUTE"}, NodePublishSecretRef: &corev1.SecretReference{Name: "CANARY_SECRET"}}}}}}
	k.Inventory.StorageClasses = []storagev1.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: "disk", UID: "class", Annotations: map[string]string{"private": "CANARY_CLASS"}}, Provisioner: "disk.csi", Parameters: map[string]string{"token": "CANARY_PARAMETER"}}}
	snapshot, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CANARY") || strings.Contains(string(raw), "parameters") || strings.Contains(string(raw), "annotations") {
		t.Fatal("raw private fields persisted")
	}
	if *snapshot.Volumes[0].CapacityBytes != 10<<30 || snapshot.Volumes[0].VolumeHandle != "volume-id" || snapshot.StorageClasses[0].UID != "class" {
		t.Fatal("safe storage identity or units lost")
	}
}
func TestClusterCapacityUnmeasuredPodFormsAreIncomplete(t *testing.T) {
	for _, form := range []string{"pod-level", "sidecar", "resize"} {
		t.Run(form, func(t *testing.T) {
			p, k := clusterCapacityFixture()
			pod := corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}}
			switch form {
			case "pod-level":
				pod.Spec.Resources = &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}
			case "sidecar":
				always := corev1.ContainerRestartPolicyAlways
				pod.Spec.InitContainers = []corev1.Container{{RestartPolicy: &always}}
			case "resize":
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}}
			}
			k.Inventory.Pods = []corev1.Pod{pod}
			snapshot, err := MergeClusterCapacity(p, k)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Complete || len(snapshot.Conditions) == 0 {
				t.Fatal("unsupported request form silently treated as measured")
			}
		})
	}
}

func TestClusterCapacityPodRequestsAccounting(t *testing.T) {
	p, k := clusterCapacityFixture()
	k.Inventory.Pods = []corev1.Pod{{Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}, {Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")}}}}, InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}}, Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("1Mi")}}}}
	snapshot, err := MergeClusterCapacity(p, k)
	if err != nil {
		t.Fatal(err)
	}
	requests := snapshot.Nodes[0].Requests
	if !snapshot.Complete || *requests.CPUMillis != 550 || *requests.MemoryBytes != 65<<20 || *requests.Pods != 1 {
		t.Fatalf("init or overhead requests lost: %+v", requests)
	}
}

func TestClusterCapacityIndependentServerFreshness(t *testing.T) {
	now := time.Now().UTC()
	server := CapacityRecord{Kind: "server", Provider: "digitalocean", AccountID: "a", ResourceID: "server:42", ServerID: "daemon", CheckedAt: now, Observation: &CapacityObservation{CheckedAt: now, CPUMillis: number(1000)}, GPUKind: "nvidia-l4", GPUs: number(1)}
	cluster := CapacityRecord{Kind: "cluster_node", Provider: "digitalocean", AccountID: "a", ResourceID: "server:42", ClusterResourceID: "cluster", ClusterComplete: true, CheckedAt: now.Add(-3 * time.Minute)}
	joined := deduplicateClusterCapacity([]CapacityRecord{server}, []CapacityRecord{cluster})[0]
	if freshCapacityRecord(joined, now) {
		t.Fatal("new server fact renewed stale cluster inventory")
	}
	server.CheckedAt = now.Add(-6 * time.Minute)
	cluster.CheckedAt = now
	joined = deduplicateClusterCapacity([]CapacityRecord{server}, []CapacityRecord{cluster})[0]
	if freshCapacityRecord(joined, now) {
		t.Fatal("new cluster fact renewed stale server/GPU inventory")
	}
	if joined.Observation != server.Observation || joined.GPUKind != server.GPUKind || joined.GPUs != server.GPUs {
		t.Fatal("independent server facts replaced")
	}
	server.CheckedAt = now
	server.Observation.CheckedAt = now.Add(-6 * time.Minute)
	joined = deduplicateClusterCapacity([]CapacityRecord{server}, []CapacityRecord{cluster})[0]
	usage, err := summarizeCapacity([]CapacityRecord{joined}, nil, CapacityPolicy{}, now)
	if err != nil || usage.Observed.CPUMillis != nil || usage.Fresh {
		t.Fatal("cluster observation renewed a stale daemon measurement", err)
	}
}
