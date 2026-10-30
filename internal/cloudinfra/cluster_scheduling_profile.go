package cloudinfra

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

// ClusterSchedulingProfile contains normalized observation facts, not SKU capacity.
// Overhead includes every DaemonSet and verified node-static mirror workloads.
type ClusterSchedulingProfile struct {
	PoolID         string                `json:"pool_id"`
	TemplateDigest string                `json:"template_digest"`
	Digest         string                `json:"digest"`
	Architecture   string                `json:"architecture"`
	Zone           string                `json:"zone"`
	Allocatable    ClusterWorkloadVector `json:"allocatable"`
	Overhead       ClusterWorkloadVector `json:"overhead"`
	Available      ClusterWorkloadVector `json:"available"`
}

type ClusterNativeWorkload struct {
	PoolID         string `json:"pool_id"`
	PolicyID       string `json:"policy_id"`
	PolicyRevision int64  `json:"policy_revision"`
	ProfileDigest  string `json:"profile_digest"`
	MaximumNodes   int    `json:"maximum_nodes"`
}

func schedulingDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func clusterPoolTemplateDigest(o provider.ClusterObservation, p provider.NodePoolSpec) string {
	p.Count, p.Min, p.Max = 0, 0, 0
	p.OperationID, p.RequestDigest = "", ""
	return schedulingDigest(struct {
		Version string
		Pool    provider.NodePoolSpec
	}{o.Version, p})
}

// BuildClusterSchedulingProfiles refuses incomplete, heterogeneous or constrained
// templates. Missing requests cannot stand in for a zero overhead guarantee.
func BuildClusterSchedulingProfiles(s ClusterCapacitySnapshot, o provider.ClusterObservation, in kubernetes.CapacityInventory, now time.Time) []ClusterSchedulingProfile {
	if !s.Fresh(now) || !s.WorkloadsComplete || !in.Complete || !freshClusterEnvelopeEvidence(o.ObservedAt, now) || o.Version == "" || o.Ref != s.Ref {
		return nil
	}
	var overhead ClusterWorkloadVector
	var proofs []string
	add := func(p corev1.Pod) bool {
		for _, c := range append(append([]corev1.Container{}, p.Spec.Containers...), p.Spec.InitContainers...) {
			cpu, cpuOK := c.Resources.Requests[corev1.ResourceCPU]
			mem, memOK := c.Resources.Requests[corev1.ResourceMemory]
			if !cpuOK || !memOK || cpu.Sign() <= 0 || mem.Sign() <= 0 {
				return false
			}
		}
		facts, complete, err := clusterWorkloadObservations(kubernetes.CapacityInventory{Pods: []corev1.Pod{p}})
		if err != nil || !complete || len(facts) != 1 {
			return false
		}
		overhead, err = clusterWorkloadUsage(overhead, []clusterWorkloadCoverage{{Maximum: facts[0].Requests}})
		return err == nil
	}
	daemons := map[string]bool{}
	for _, ds := range in.DaemonSets {
		if ds.UID == "" || ds.DeletionTimestamp != nil || ds.Status.ObservedGeneration != ds.Generation || ds.Status.DesiredNumberScheduled < 1 || ds.Status.UpdatedNumberScheduled != ds.Status.DesiredNumberScheduled || ds.Spec.Template.Spec.RuntimeClassName != nil || len(ds.Spec.Template.Spec.Overhead) > 0 {
			return nil
		}
		daemons[string(ds.UID)] = true
		p := corev1.Pod{ObjectMeta: ds.Spec.Template.ObjectMeta, Spec: ds.Spec.Template.Spec}
		p.UID = ds.UID
		saved := overhead
		overhead = ClusterWorkloadVector{}
		if !add(p) {
			return nil
		}
		templateRequests := overhead
		overhead = saved
		observed := 0
		for _, actual := range in.Pods {
			if workloadController(actual.OwnerReferences, "DaemonSet") != string(ds.UID) || actual.Namespace != ds.Namespace || actual.Status.Phase == corev1.PodSucceeded || actual.Status.Phase == corev1.PodFailed {
				continue
			}
			if actual.Spec.RuntimeClassName != nil || len(actual.Spec.Overhead) > 0 || len(actual.Spec.Containers) != len(p.Spec.Containers) || len(actual.Spec.InitContainers) != len(p.Spec.InitContainers) {
				return nil
			}
			for i, c := range actual.Spec.Containers {
				if c.Name != p.Spec.Containers[i].Name || c.Image != p.Spec.Containers[i].Image {
					return nil
				}
			}
			saved = overhead
			overhead = ClusterWorkloadVector{}
			if !add(actual) {
				return nil
			}
			actualRequests := overhead
			overhead = saved
			if actualRequests != templateRequests {
				return nil
			}
			observed++
		}
		if observed != int(ds.Status.DesiredNumberScheduled) {
			return nil
		}
		var err error
		overhead, err = clusterWorkloadUsage(overhead, []clusterWorkloadCoverage{{Maximum: templateRequests}})
		if err != nil {
			return nil
		}
		proofs = append(proofs, schedulingDigest(struct {
			UID        string
			Generation int64
		}{string(ds.UID), ds.Generation}))
	}
	mirrors := map[string]ClusterWorkloadVector{}
	for _, p := range in.Pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if uid := workloadController(p.OwnerReferences, "DaemonSet"); uid != "" {
			if !daemons[uid] {
				return nil
			}
			continue
		}
		if p.Annotations["kubernetes.io/config.mirror"] == "" {
			if p.Annotations["kubernetes.io/config.source"] != "" && p.Annotations["kubernetes.io/config.source"] != "api" {
				return nil
			}
			continue
		}
		matched := false
		for _, n := range in.Nodes {
			if n.Name == p.Spec.NodeName {
				for _, owner := range p.OwnerReferences {
					if owner.Kind == "Node" && owner.APIVersion == "v1" && owner.UID == n.UID {
						matched = true
					}
				}
			}
		}
		if !matched {
			return nil
		}
		saved := overhead
		overhead = mirrors[p.Spec.NodeName]
		if !add(p) {
			return nil
		}
		mirrors[p.Spec.NodeName] = overhead
		overhead = saved
	}
	sort.Strings(proofs)
	var out []ClusterSchedulingProfile
	for _, pool := range o.Pools {
		if len(pool.Taints) > 0 {
			continue
		}
		profile := ClusterSchedulingProfile{PoolID: pool.ID, TemplateDigest: clusterPoolTemplateDigest(o, pool), Overhead: overhead}
		count := 0
		valid := true
		var mirrorBound ClusterWorkloadVector
		for _, node := range s.Nodes {
			if node.PoolID != pool.ID {
				continue
			}
			m := node.Allocatable
			if !node.Ready || node.Unschedulable || node.Architecture == "" || node.Zone == "" || !node.WorkloadCompatible(node.Architecture) || m.CPUMillis == nil || m.MemoryBytes == nil || m.EphemeralStorageBytes == nil || m.Pods == nil {
				valid = false
				break
			}
			v := ClusterWorkloadVector{CPUMillis: *m.CPUMillis, MemoryBytes: *m.MemoryBytes, EphemeralStorageBytes: *m.EphemeralStorageBytes, Pods: *m.Pods}
			if count == 0 {
				profile.Architecture, profile.Zone, profile.Allocatable = node.Architecture, node.Zone, v
			} else {
				if profile.Architecture != node.Architecture || profile.Zone != node.Zone {
					valid = false
					break
				}
				profile.Allocatable.CPUMillis = min(profile.Allocatable.CPUMillis, v.CPUMillis)
				profile.Allocatable.MemoryBytes = min(profile.Allocatable.MemoryBytes, v.MemoryBytes)
				profile.Allocatable.EphemeralStorageBytes = min(profile.Allocatable.EphemeralStorageBytes, v.EphemeralStorageBytes)
				profile.Allocatable.Pods = min(profile.Allocatable.Pods, v.Pods)
			}
			foundNode := false
			for _, n := range in.Nodes {
				if string(n.UID) != node.NodeUID {
					continue
				}
				if n.Labels[corev1.LabelInstanceTypeStable] != pool.Size || strings.TrimPrefix(n.Status.NodeInfo.KubeletVersion, "v") != strings.Split(o.Version, "-")[0] {
					valid = false
					break
				}
				foundNode = true
				m := mirrors[n.Name]
				mirrorBound.CPUMillis = max(mirrorBound.CPUMillis, m.CPUMillis)
				mirrorBound.MemoryBytes = max(mirrorBound.MemoryBytes, m.MemoryBytes)
				mirrorBound.EphemeralStorageBytes = max(mirrorBound.EphemeralStorageBytes, m.EphemeralStorageBytes)
				mirrorBound.Pods = max(mirrorBound.Pods, m.Pods)
			}
			if !foundNode {
				valid = false
				break
			}
			count++
		}
		if !valid || count == 0 || count != pool.Count {
			continue
		}
		var addErr error
		profile.Overhead, addErr = clusterWorkloadUsage(overhead, []clusterWorkloadCoverage{{Maximum: mirrorBound}})
		if addErr != nil {
			continue
		}
		a, b := workloadDimensions(profile.Allocatable), workloadDimensions(profile.Overhead)
		for i := range a {
			a[i] -= b[i]
			if a[i] < 0 {
				valid = false
			}
		}
		if !valid || a[0] <= 0 || a[1] <= 0 || a[3] <= 0 {
			continue
		}
		profile.Available = ClusterWorkloadVector{CPUMillis: a[0], MemoryBytes: a[1], EphemeralStorageBytes: a[2], Pods: a[3]}
		profile.Digest = schedulingDigest(struct {
			Profile ClusterSchedulingProfile
			Daemons []string
		}{profile, proofs})
		out = append(out, profile)
	}
	return out
}

// WorkloadCompatible covers the constraints supported by managed CPU admission.
func (n ClusterNodeCapacity) WorkloadCompatible(architecture string) bool {
	if n.Architecture != architecture || architecture == "" || len(n.Taints) > 0 {
		return false
	}
	for _, quantity := range n.Allocatable.Extended {
		if quantity != 0 {
			return false
		}
	}
	return true
}
