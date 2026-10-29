package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ClusterManagementFacts are complete bounded management reads, never runtime credentials.
type ClusterManagementFacts struct {
	Nodes      []corev1.Node
	Pods       []corev1.Pod
	Volumes    []corev1.PersistentVolume
	PDBs       []policyv1.PodDisruptionBudget
	ObservedAt time.Time
	Complete   bool
}

type ClusterDrainReview struct {
	ResourceID             string         `json:"resource_id"`
	PoolID                 string         `json:"pool_id"`
	Workloads              []string       `json:"workloads"`
	Volumes                []string       `json:"volumes"`
	PDBs                   []string       `json:"pdbs"`
	RequiredSpareCapacity  ResourceVector `json:"required_spare_capacity"`
	AvailableSpareCapacity ResourceVector `json:"available_spare_capacity"`
	Blockers               []string       `json:"blockers"`
	Digest                 string         `json:"digest"`
	ObservedAt             time.Time      `json:"observed_at"`
}

func drainReview(r *ManagedResource, poolID string, facts ClusterManagementFacts, now time.Time) *ClusterDrainReview {
	out := &ClusterDrainReview{ResourceID: r.ID, PoolID: poolID, ObservedAt: facts.ObservedAt, Workloads: []string{}, Volumes: []string{}, PDBs: []string{}, Blockers: []string{}}
	block := map[string]bool{}
	if !facts.Complete || facts.ObservedAt.IsZero() || facts.ObservedAt.After(now) || now.Sub(facts.ObservedAt) >= 2*time.Minute {
		block["management_observation_incomplete"] = true
	}
	affected := map[string]bool{}
	remaining := map[string]bool{}
	for _, node := range facts.Nodes {
		id := node.Labels["doks.digitalocean.com/node-pool-id"]
		if node.UID == "" || node.Spec.ProviderID == "" || id == "" {
			block["node_identity_incomplete"] = true
		}
		if poolID == "" || id == poolID {
			affected[node.Name] = true
			continue
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if ready && !node.Spec.Unschedulable && len(node.Spec.Taints) == 0 {
			remaining[node.Name] = true
			out.AvailableSpareCapacity.CPUMillis += node.Status.Allocatable.Cpu().MilliValue()
			out.AvailableSpareCapacity.MemoryBytes += node.Status.Allocatable.Memory().Value()
		}
	}
	if len(affected) == 0 {
		block["pool_nodes_unobserved"] = true
	}
	for _, volume := range facts.Volumes {
		if volume.Spec.Local != nil || volume.Spec.HostPath != nil {
			out.Volumes = append(out.Volumes, volume.Name+"@"+string(volume.UID))
			block["retained_local_volume"] = true
		}
	}
	for _, pod := range facts.Pods {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		cpu, memory := int64(0), int64(0)
		for _, container := range pod.Spec.Containers {
			cpu += container.Resources.Requests.Cpu().MilliValue()
			memory += container.Resources.Requests.Memory().Value()
		}
		for _, container := range pod.Spec.InitContainers {
			if c := container.Resources.Requests.Cpu().MilliValue(); c > cpu {
				cpu = c
			}
			if m := container.Resources.Requests.Memory().Value(); m > memory {
				memory = m
			}
		}
		cpu += pod.Spec.Overhead.Cpu().MilliValue()
		memory += pod.Spec.Overhead.Memory().Value()
		if remaining[pod.Spec.NodeName] {
			out.AvailableSpareCapacity.CPUMillis -= cpu
			out.AvailableSpareCapacity.MemoryBytes -= memory
		}
		if !affected[pod.Spec.NodeName] {
			continue
		}
		ref := pod.Namespace + "/" + pod.Name + "@" + string(pod.UID)
		out.Workloads = append(out.Workloads, ref)
		out.RequiredSpareCapacity.CPUMillis += cpu
		out.RequiredSpareCapacity.MemoryBytes += memory
		// Scheduling feasibility, unmanaged writers and replacement readiness need a
		// prepared drain protocol. An aggregate spare vector alone cannot prove them.
		block["workload_drain_preparation_required"] = true
		for _, volume := range pod.Spec.Volumes {
			if volume.EmptyDir != nil || volume.HostPath != nil || volume.Ephemeral != nil {
				block["local_data"] = true
			}
			if volume.PersistentVolumeClaim != nil {
				out.Volumes = append(out.Volumes, pod.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName)
				block["volume_relocation_unproven"] = true
			}
		}
		for _, pdb := range facts.PDBs {
			if pdb.Namespace != pod.Namespace {
				continue
			}
			selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil {
				block["pdb_observation_invalid"] = true
				continue
			}
			if !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			out.PDBs = append(out.PDBs, pdb.Namespace+"/"+pdb.Name+"@"+string(pdb.UID))
			if pdb.Status.ObservedGeneration < pdb.Generation || pdb.Status.DisruptionsAllowed < 1 {
				block["pdb_blocked"] = true
			}
		}
	}
	if out.AvailableSpareCapacity.CPUMillis < 0 {
		out.AvailableSpareCapacity.CPUMillis = 0
	}
	if out.AvailableSpareCapacity.MemoryBytes < 0 {
		out.AvailableSpareCapacity.MemoryBytes = 0
	}
	if out.RequiredSpareCapacity.CPUMillis > out.AvailableSpareCapacity.CPUMillis || out.RequiredSpareCapacity.MemoryBytes > out.AvailableSpareCapacity.MemoryBytes {
		block["missing_spare_capacity"] = true
	}
	for reason := range block {
		out.Blockers = append(out.Blockers, reason)
	}
	sort.Strings(out.Workloads)
	sort.Strings(out.Volumes)
	sort.Strings(out.PDBs)
	sort.Strings(out.Blockers)
	material := *out
	material.ObservedAt = time.Time{}
	out.Digest = maintenanceHash(struct {
		Review   ClusterDrainReview
		Revision int64
		Ref      provider.ClusterRef
	}{material, r.ObservedRevision, r.Observation.Ref})
	return out
}
func (s *Service) PreviewClusterDrain(ctx context.Context, p security.Principal, cloud, resource, pool string) (*ClusterDrainReview, error) {
	if checkErr := s.clusterEnabled(); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.Authz.Require(ctx, p, security.ClusterManage, ""); checkErr != nil {
		return nil, checkErr
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, resource)
	if err != nil {
		return nil, err
	}
	if r.Observation == nil || r.DeletedAt != nil || r.CloudID != cloud || r.Ownership != "managed" {
		return nil, ErrConflict
	}
	profile, err := s.authorize(ctx, p, cloud)
	if err != nil {
		return nil, err
	}
	_, access, err := s.clusterAccount(ctx, p, r.ConnectionID, provider.ClusterDiscovery)
	if err != nil {
		return nil, err
	}
	in := ClusterInput{Kind: "management-observation", Provider: r.Provider, AccountID: r.AccountID, ConnectionID: r.ConnectionID, ConnectionGeneration: access.Generation, CloudRevision: profile.Revision, ResourceID: r.ID, ResourceRevision: r.Revision, Ref: r.Observation.Ref}
	reader, access, err := s.clusterAuthority(ctx, p, cloud, in)
	if err != nil {
		return nil, err
	}
	facts, err := s.observeClusterManagement(ctx, p, cloud, in, r, reader, access)
	if err != nil {
		return nil, err
	}
	return drainReview(r, pool, facts, s.now()), nil
}
func (s *Service) observeClusterManagement(ctx context.Context, p security.Principal, cloud string, in ClusterInput, r *ManagedResource, reader provider.ClusterReader, access provider.AccountAccess) (ClusterManagementFacts, error) {
	var facts ClusterManagementFacts
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	observation, err := reader.GetCluster(ctx, access, in.Ref)
	if err != nil {
		return facts, err
	}
	if observation.Ref != r.Observation.Ref || observation.Endpoint == "" || observation.Endpoint != r.Observation.Endpoint || r.Observation.CAHash == "" {
		return facts, ErrConflict
	}
	if checkErr := s.Adapters.Require(in.Provider, provider.ClusterBootstrapAccess); checkErr != nil {
		return facts, checkErr
	}
	bootstrap, ok := reader.(provider.ClusterBootstrap)
	if !ok {
		return facts, provider.ErrInfrastructureUnsupported
	}
	now := s.now().UTC()
	digest := clusterDigest(in)
	record := &ClusterOperation{ID: uuid.NewString(), OrgID: p.OrgID, CloudID: cloud, Kind: in.Kind, SchemaVersion: 1, Input: in, Digest: digest, Revision: 1, Status: "preview", Principal: p, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Steps: []ClusterStep{{Name: "management-access", State: "pending", RequestDigest: digest}}}
	if checkErr := s.Clusters.CreateClusterPreview(ctx, record); checkErr != nil {
		return facts, checkErr
	}
	access.BeforeEffect = func(effectCtx context.Context) error {
		if checkErr := s.Adapters.Require(in.Provider, provider.ClusterBootstrapAccess); checkErr != nil {
			return checkErr
		}
		if _, _, checkErr := s.clusterAuthority(effectCtx, p, cloud, in); checkErr != nil {
			return checkErr
		}
		if checkErr := s.Authz.Require(effectCtx, p, security.ClusterManage, ""); checkErr != nil {
			return checkErr
		}
		current, getErr := s.Clusters.GetManagedResource(effectCtx, p.OrgID, r.ID)
		if getErr != nil {
			return getErr
		}
		if current.Revision != r.Revision || current.DeletedAt != nil {
			return ErrConflict
		}
		at := s.now().UTC()
		record.Steps[0].State = "attempted"
		record.Steps[0].AttemptedAt = &at
		if checkErr := s.saveCluster(effectCtx, record); checkErr != nil {
			return checkErr
		}
		return checkClusterMaintenanceFence(effectCtx)
	}
	material, err := bootstrap.BootstrapCluster(ctx, access, in.Ref, 5*time.Minute)
	if err != nil {
		return facts, err
	}
	hash := sha256.Sum256(material.CAData)
	if material.Endpoint != observation.Endpoint || hex.EncodeToString(hash[:]) != r.Observation.CAHash || !material.ExpiresAt.After(s.now().Add(time.Minute)) {
		return facts, ErrConflict
	}
	cfg := &rest.Config{Host: material.Endpoint, BearerToken: material.Token, Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: material.CAData, CertData: material.ClientCertificate, KeyData: material.ClientKey}}
	client, err := kubeclient.NewForConfig(cfg)
	if err != nil {
		return facts, err
	}
	facts, err = readClusterManagement(ctx, client)
	if err != nil {
		return facts, &provider.ClusterLifecycleBlockedError{Reason: "management_read_failed"}
	}
	record.Status = "succeeded"
	record.Steps[0].State = "completed"
	if checkErr := s.saveCluster(ctx, record); checkErr != nil {
		return facts, checkErr
	}
	return facts, nil
}
func readClusterManagement(ctx context.Context, client kubeclient.Interface) (ClusterManagementFacts, error) {
	var facts ClusterManagementFacts
	opts := metav1.ListOptions{Limit: 5000}
	nodes, err := client.CoreV1().Nodes().List(ctx, opts)
	if err != nil {
		return facts, err
	}
	pods, err := client.CoreV1().Pods("").List(ctx, opts)
	if err != nil {
		return facts, err
	}
	pdbs, err := client.PolicyV1().PodDisruptionBudgets("").List(ctx, opts)
	if err != nil {
		return facts, err
	}
	volumes, err := client.CoreV1().PersistentVolumes().List(ctx, opts)
	if err != nil {
		return facts, err
	}
	facts = ClusterManagementFacts{Nodes: nodes.Items, Pods: pods.Items, PDBs: pdbs.Items, Volumes: volumes.Items, ObservedAt: time.Now().UTC(), Complete: nodes.Continue == "" && pods.Continue == "" && pdbs.Continue == "" && volumes.Continue == ""}
	return facts, nil
}
