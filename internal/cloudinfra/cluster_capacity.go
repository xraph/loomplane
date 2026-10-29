package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	mathbig "math/big"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/provider"
)

// reserveClusterCapacity uses the same reservation rows counted by server admission.
// The whole vector stays reserved until a later verified inventory handover.
func (s *PgStore) reserveClusterCapacity(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if err := checkAutomaticEnvelopePrices(o); err != nil {
		return err
	}
	p, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	if p.Budget != nil {
		review, e := scopedBudgetReviewTx(ctx, tx, o.OrgID, o.CloudID, o.Input.Provider, o.Input.AccountID, o.ID, o.MoneyEstimate, p, o.Input.EnvelopeReview)
		if e != nil {
			return e
		}
		if e := checkBudgetReview(p, o.Input.BudgetReview, review, o.Input.AcknowledgeUnknownCosts); e != nil {
			return e
		}
	}
	d := CapacityDemand{OrgID: o.OrgID, CloudID: o.CloudID, PolicyRevision: o.Input.PolicyRevision}
	blocked := func(reason, resource string) error {
		return &CapacityAdmissionError{Block: *capacityBlock(p, d, reason, resource)}
	}
	if p.Revision == 0 || p.Revision != o.Input.PolicyRevision {
		return blocked("review_required", "policy")
	}
	v := o.CapacityTarget
	if !nonnegativeCapacity(v) || v.Servers <= 0 || v.CPUMillis <= 0 || v.MemoryBytes <= 0 || v.StorageBytes <= 0 {
		return ErrInvalid
	}
	if p.Limits.Servers <= 0 || p.Limits.CPUMillis <= 0 || p.Limits.MemoryBytes <= 0 || p.Limits.StorageBytes <= 0 {
		return blocked("review_required", "ceilings")
	}
	records, err := capacityRecordsTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	used := capacityResources(ResourceVector{})
	for _, r := range records {
		if !freshCapacityRecord(r, time.Now()) {
			return blocked("stale_inventory", "inventory")
		}
		used, err = sumResources(used, r.Resources)
		if err != nil {
			return err
		}
	}
	missing, err := capacityInventoryIncomplete(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	if missing {
		return blocked("stale_inventory", "inventory")
	}
	reservations, err := capacityReservationsTx(ctx, tx, o.OrgID)
	if err != nil {
		return err
	}
	existing := false
	for _, r := range reservations {
		if r.reservation.OperationID == o.ID {
			if r.cloud != o.CloudID || r.reservation.ClusterOperationID != o.ID || r.reservation.Delta != v || r.reservation.Provider != o.Input.Provider || r.reservation.AccountID != o.Input.AccountID {
				return ErrConflict
			}
			existing = true
			continue
		}
		if r.cloud == o.CloudID {
			used, err = sumResources(used, capacityResources(r.reservation.Delta))
			if err != nil {
				return err
			}
		}
	}
	localEnvelope, accountEnvelope, err := envelopeUsage(ctx, tx, o.OrgID, o.CloudID, o.Input.Provider, o.Input.AccountID, o.ID)
	if err != nil {
		return err
	}
	used, err = sumResources(used, capacityResources(localEnvelope))
	if err != nil {
		return err
	}
	admitted, err := clusterEnvelopeAdmissionDelta(ctx, tx, o, v)
	if err != nil {
		return err
	}
	if b := checkCapacityLimit(p, d, used, admitted, capacityResources(p.Limits), "hard_limit"); b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3 FOR UPDATE`, o.OrgID, o.Input.Provider, o.Input.AccountID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return blocked("stale_inventory", "account")
	}
	if err != nil {
		return err
	}
	var account CapacityAccountSnapshot
	if checkErr := json.Unmarshal(raw, &account); checkErr != nil {
		return checkErr
	}
	if !freshCapacity(account.CheckedAt, time.Now()) {
		return blocked("stale_inventory", "account")
	}
	if account.Quota.Servers == nil {
		return blocked("stale_inventory", "provider_quota")
	}
	accountUsed := account.Provisioned
	for _, r := range reservations {
		if r.reservation.OperationID != o.ID && r.reservation.Provider == o.Input.Provider && r.reservation.AccountID == o.Input.AccountID {
			accountUsed, err = sumResources(accountUsed, capacityResources(r.reservation.Delta))
			if err != nil {
				return err
			}
		}
	}
	accountUsed, err = sumResources(accountUsed, capacityResources(accountEnvelope))
	if err != nil {
		return err
	}
	quota := CapacityResources{Servers: account.Quota.Servers, CPUMillis: account.Quota.CPUMillis, MemoryBytes: account.Quota.MemoryBytes, StorageBytes: account.Quota.StorageBytes}
	if b := checkCapacityLimit(p, d, accountUsed, admitted, quota, "provider_quota"); b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	if existing {
		return s.reserveReviewedEnvelopes(ctx, tx, o)
	}
	r := infrastructureReservation{OperationID: o.ID, ClusterOperationID: o.ID, ManagedResourceID: o.Input.ResourceID, Provider: o.Input.Provider, AccountID: o.Input.AccountID, Delta: v, Target: v, CreatedAt: time.Now().UTC()}
	raw, err = json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'reserved',$10)`, o.OrgID, o.CloudID, o.ID, p.Revision, o.Digest, v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes, raw)
	if err != nil {
		return storeError(err)
	}
	return s.reserveReviewedEnvelopes(ctx, tx, o)
}

// AttemptClusterCreate fences the worker and rechecks admission with the marker.
func (s *PgStore) AttemptClusterCreate(ctx context.Context, o *ClusterOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	saved, err := clusterOperationTx(ctx, tx, o.OrgID, o.CloudID, o.ID)
	if err != nil {
		return err
	}
	if saved.Revision != o.Revision || saved.FencingEpoch != o.FencingEpoch || saved.CancelRequested || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now()) || saved.Steps[0].State != "pending" {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if retirementKind(o.Kind) {
		if err := retirementPinsTx(ctx, tx, o); err != nil {
			return err
		}
	}
	if maintenanceKind(o.Kind) {
		if err := maintenancePinsTx(ctx, tx, o); err != nil {
			return err
		}
	}
	if o.Kind == "create" || o.CapacityTarget.Servers > 0 {
		if checkErr := s.reserveClusterCapacity(ctx, tx, o); checkErr != nil {
			return checkErr
		}
	}
	now := time.Now().UTC()
	o.Steps[0].State = "attempted"
	o.Steps[0].AttemptedAt = &now
	o.UpdatedAt = now
	o.Revision++
	if checkErr := checkpointClusterTx(ctx, tx, o, saved.Revision); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *Service) refreshClusterAccount(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) error {
	if !clusterObservationOnly(o) {
		if err := s.refreshBudgetPrices(ctx, o.Principal, o.CloudID); err != nil {
			return err
		}
	}
	inventory, ok := reader.(provider.CapacityInventoryAdapter)
	if !ok {
		return provider.ErrInfrastructureUnsupported
	}
	snapshot, err := inventory.CapacityInventory(ctx, provider.InfrastructureCredential{Provider: access.Provider, AccountID: access.AccountID, Token: string(access.Credential)})
	if err != nil {
		return err
	}
	if snapshot.Provider != access.Provider || snapshot.AccountID != access.AccountID {
		return provider.ErrInfrastructureIdentity
	}
	account, err := summarizeAccount(snapshot)
	if err != nil {
		return err
	}
	st, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return ErrInvalid
	}
	return st.SaveClusterAccount(ctx, o.OrgID, o.CloudID, account)
}
func (s *PgStore) SaveClusterAccount(ctx context.Context, org, cloud string, account CapacityAccountSnapshot) error {
	if !freshCapacity(account.CheckedAt, time.Now()) {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(account)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_accounts(org_id,provider,account_id,servers,cpu_millis,memory_bytes,storage_bytes,quota_servers,quota_cpu_millis,quota_memory_bytes,quota_storage_bytes,checked_at,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(org_id,provider,account_id) DO UPDATE SET servers=excluded.servers,cpu_millis=excluded.cpu_millis,memory_bytes=excluded.memory_bytes,storage_bytes=excluded.storage_bytes,quota_servers=excluded.quota_servers,quota_cpu_millis=excluded.quota_cpu_millis,quota_memory_bytes=excluded.quota_memory_bytes,quota_storage_bytes=excluded.quota_storage_bytes,checked_at=excluded.checked_at,body=excluded.body WHERE loomplane_cloud_capacity_accounts.checked_at<=excluded.checked_at`, org, account.Provider, account.AccountID, account.Provisioned.Servers, account.Provisioned.CPUMillis, account.Provisioned.MemoryBytes, account.Provisioned.StorageBytes, account.Quota.Servers, account.Quota.CPUMillis, account.Quota.MemoryBytes, account.Quota.StorageBytes, account.CheckedAt, raw)
	if err != nil {
		return err
	}
	if checkErr := saveAccountMembership(ctx, tx, org, account); checkErr != nil {
		return checkErr
	}
	if checkErr := settleDeletedClusterEnvelopes(ctx, tx, org, account); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

const ClusterCapacityFreshness = 2 * time.Minute

// KubernetesClusterCapacity is read through pinned management access.
type KubernetesClusterCapacity struct {
	Provider  string
	Ref       provider.ClusterRef
	Inventory kubernetes.CapacityInventory
}

type ClusterMetricProvenance struct {
	Source    string    `json:"source"`
	Identity  string    `json:"identity"`
	CheckedAt time.Time `json:"checked_at"`
}

type ClusterNodeCapacity struct {
	ExternalVMID  string                             `json:"external_vm_id"`
	NodeUID       string                             `json:"node_uid"`
	PoolID        string                             `json:"pool_id"`
	Zone          string                             `json:"zone"`
	Architecture  string                             `json:"architecture"`
	Taints        []provider.NodePoolTaint           `json:"taints"`
	Ready         bool                               `json:"ready"`
	Unschedulable bool                               `json:"unschedulable"`
	Provisioned   CapacityResources                  `json:"provisioned"`
	Allocatable   ClusterResourceMetrics             `json:"allocatable"`
	Requests      ClusterResourceMetrics             `json:"requests"`
	GPUKinds      map[string]int64                   `json:"gpu_kinds"`
	Provenance    map[string]ClusterMetricProvenance `json:"provenance"`
}

type ClusterCapacitySnapshot struct {
	Workloads           []ClusterObservedWorkload   `json:"workloads,omitempty"`
	WorkloadsComplete   bool                        `json:"workloads_complete,omitempty"`
	ResourceFingerprint string                      `json:"resource_fingerprint"`
	StorageProvenance   ClusterMetricProvenance     `json:"storage_provenance"`
	ResourceID          string                      `json:"resource_id"`
	Provider            string                      `json:"provider"`
	AccountID           string                      `json:"account_id"`
	Ref                 provider.ClusterRef         `json:"ref"`
	ObservationRevision int64                       `json:"observation_revision"`
	CheckedAt           time.Time                   `json:"checked_at"`
	Nodes               []ClusterNodeCapacity       `json:"nodes"`
	Volumes             []ClusterVolumeCapacity     `json:"volumes"`
	StorageClasses      []ClusterStorageClass       `json:"storage_classes"`
	Complete            bool                        `json:"complete"`
	Conditions          []provider.ClusterCondition `json:"conditions"`
}

func (s ClusterCapacitySnapshot) Fresh(now time.Time) bool {
	return s.Complete && !s.CheckedAt.IsZero() && !s.CheckedAt.After(now) && now.Sub(s.CheckedAt) <= ClusterCapacityFreshness
}

// MergeClusterCapacity joins immutable VM IDs. Names only associate pods within this observation.
func MergeClusterCapacity(p provider.ProviderClusterCapacity, k KubernetesClusterCapacity) (ClusterCapacitySnapshot, error) {
	out := ClusterCapacitySnapshot{Provider: p.Provider, AccountID: p.Ref.AccountID, Ref: p.Ref, CheckedAt: p.CheckedAt, Complete: p.Complete && k.Inventory.Complete, Nodes: []ClusterNodeCapacity{}, Conditions: p.Conditions}
	out.StorageProvenance = ClusterMetricProvenance{Source: "kubernetes", Identity: k.Ref.Incarnation, CheckedAt: k.Inventory.CheckedAt}
	if p.Provider == "" || p.Provider != k.Provider || p.Ref != k.Ref || p.Ref.ID == "" || p.Ref.AccountID == "" || p.Ref.Incarnation == "" {
		return out, provider.ErrInfrastructureIdentity
	}
	if k.Inventory.CheckedAt.Before(out.CheckedAt) {
		out.CheckedAt = k.Inventory.CheckedAt
	}
	missing := func(reason string) {
		out.Complete = false
		out.Conditions = append(out.Conditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: reason})
	}
	if !out.Complete {
		missing("inventory_incomplete")
	}
	var workloadErr error
	out.Workloads, out.WorkloadsComplete, workloadErr = clusterWorkloadObservations(k.Inventory)
	if workloadErr != nil {
		return out, workloadErr
	}
	if err := clusterStorageFacts(&out, k.Inventory); err != nil {
		return out, err
	}
	runtime := map[string]corev1.Node{}
	uids := map[string]bool{}
	names := map[string]bool{}
	for _, n := range k.Inventory.Nodes {
		prefix := p.Provider + "://"
		if n.UID == "" || n.Spec.ProviderID == "" || !strings.HasPrefix(n.Spec.ProviderID, prefix) || uids[string(n.UID)] || names[n.Name] {
			missing("node_identity_unknown")
			continue
		}
		id := strings.TrimPrefix(n.Spec.ProviderID, prefix)
		if id == "" || strings.Contains(id, "/") {
			missing("node_mapping_unknown")
			continue
		}
		if _, exists := runtime[id]; exists {
			return out, provider.ErrInfrastructureIdentity
		}
		runtime[id] = n
		uids[string(n.UID)] = true
		names[n.Name] = true
	}
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		if n.ExternalVMID == "" || n.PoolID == "" || seen[n.ExternalVMID] {
			return out, provider.ErrInfrastructureIdentity
		}
		seen[n.ExternalVMID] = true
		if _, err := sumResources(capacityResources(ResourceVector{}), CapacityResources{CPUMillis: n.CPUMillis, MemoryBytes: n.MemoryBytes, StorageBytes: n.EphemeralStorageBytes}); err != nil {
			return out, err
		}
		node, ok := runtime[n.ExternalVMID]
		if !ok {
			missing("node_mapping_unknown")
		}
		for _, metric := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
			if quantity, exists := node.Status.Allocatable[metric]; !exists || quantity.Sign() < 0 {
				missing("allocatable_unknown")
			}
		}
		entry := ClusterNodeCapacity{ExternalVMID: n.ExternalVMID, PoolID: n.PoolID, Zone: node.Labels[corev1.LabelTopologyZone], NodeUID: string(node.UID), Architecture: node.Status.NodeInfo.Architecture, Unschedulable: node.Spec.Unschedulable, Provisioned: CapacityResources{Servers: number(1), CPUMillis: n.CPUMillis, MemoryBytes: n.MemoryBytes, StorageBytes: n.EphemeralStorageBytes}, GPUKinds: map[string]int64{}, Provenance: map[string]ClusterMetricProvenance{}}
		requestsTotal := corev1.ResourceList{}
		var podCount int64
		for _, taint := range node.Spec.Taints {
			entry.Taints = append(entry.Taints, provider.NodePoolTaint{Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect)})
		}
		var conversionErr error
		entry.Allocatable, conversionErr = clusterResourceMetrics(node.Status.Allocatable)
		if conversionErr != nil {
			return out, conversionErr
		}
		entry.Provenance["zone"] = ClusterMetricProvenance{Source: "kubernetes", Identity: entry.NodeUID, CheckedAt: k.Inventory.CheckedAt}
		for _, c := range node.Status.Conditions {
			if c.Type == corev1.NodeReady {
				entry.Ready = c.Status == corev1.ConditionTrue
			}
		}
		for metric := range node.Status.Allocatable {
			if strings.Contains(string(metric), "gpu") {
				entry.GPUKinds[string(metric)] = entry.Allocatable.Extended[string(metric)]
			}
			entry.Provenance["allocatable/"+string(metric)] = ClusterMetricProvenance{Source: "kubernetes", Identity: entry.NodeUID, CheckedAt: k.Inventory.CheckedAt}
		}
		for _, metric := range []string{"cpu_millis", "memory_bytes", "ephemeral_storage_bytes"} {
			entry.Provenance["provisioned/"+metric] = ClusterMetricProvenance{Source: p.Provider, Identity: n.ExternalVMID, CheckedAt: p.CheckedAt}
		}
		for _, pod := range k.Inventory.Pods {
			if pod.Spec.NodeName != node.Name || !ok || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			podCount++
			if pod.Spec.Resources != nil {
				missing("pod_level_accounting_unknown")
			}
			// Resizing and restartable init containers need scheduler accounting we do not infer.
			if len(pod.Status.ContainerStatuses) > 0 {
				for _, status := range pod.Status.ContainerStatuses {
					if status.AllocatedResources != nil {
						missing("pod_resize_accounting_unknown")
					}
				}
			}
			requests := corev1.ResourceList{}
			for _, container := range pod.Spec.Containers {
				addClusterRequests(requests, container.Resources.Requests)
			}
			for _, container := range pod.Spec.InitContainers {
				if container.RestartPolicy != nil {
					missing("restartable_init_accounting_unknown")
				}
				for name, q := range container.Resources.Requests {
					current := requests[name]
					if q.Cmp(current) > 0 {
						requests[name] = q.DeepCopy()
					}
				}
			}
			addClusterRequests(requests, pod.Spec.Overhead)
			addClusterRequests(requestsTotal, requests)
		}
		requestsTotal[corev1.ResourcePods] = *resource.NewQuantity(podCount, resource.DecimalSI)
		for _, metric := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
			if _, ok := requestsTotal[metric]; !ok {
				requestsTotal[metric] = resource.Quantity{}
			}
		}
		entry.Requests, conversionErr = clusterResourceMetrics(requestsTotal)
		if conversionErr != nil {
			return out, conversionErr
		}
		for metric := range requestsTotal {
			entry.Provenance["requests/"+string(metric)] = ClusterMetricProvenance{Source: "kubernetes", Identity: entry.NodeUID, CheckedAt: k.Inventory.CheckedAt}
		}
		out.Nodes = append(out.Nodes, entry)
	}
	for id := range runtime {
		if !seen[id] {
			missing("provider_mapping_unknown")
		}
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ExternalVMID < out.Nodes[j].ExternalVMID })
	return out, nil
}
func addClusterRequests(out, values corev1.ResourceList) {
	for name, q := range values {
		current := out[name]
		current.Add(q)
		out[name] = current
	}
}

func freshCapacityRecord(r CapacityRecord, now time.Time) bool {
	if r.ClusterResourceID != "" || r.Kind == "cluster_node" || r.Kind == "cluster_inventory" {
		checked := r.CheckedAt
		if r.ClusterCheckedAt != nil {
			checked = *r.ClusterCheckedAt
		}
		if !r.ClusterComplete || checked.IsZero() || checked.After(now) || now.Sub(checked) > ClusterCapacityFreshness {
			return false
		}
	}
	return freshCapacity(r.CheckedAt, now)
}
func deduplicateClusterCapacity(servers, clusters []CapacityRecord) []CapacityRecord {
	index := map[string]int{}
	out := append([]CapacityRecord(nil), servers...)
	for i, r := range out {
		index[recordKey(r)] = i
	}
	for _, r := range clusters {
		if i, ok := index[recordKey(r)]; ok {
			previous := out[i]
			if previous.ClusterResourceID != "" && previous.ClusterResourceID != r.ClusterResourceID {
				r.ClusterComplete = false
				r.Resources = CapacityResources{}
				r.ClusterConditions = append(r.ClusterConditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: "multiple_cluster_mapping"})
			}
			if previous.Kind == "server" {
				// Keep the daemon's independent measurements, pool accounting and physical GPU facts.

				previous.ClusterResourceID = r.ClusterResourceID
				previous.ClusterNode = r.ClusterNode
				previous.ClusterComplete = r.ClusterComplete
				previous.ClusterCheckedAt = &r.CheckedAt
				previous.ClusterConditions = r.ClusterConditions
				out[i] = previous
			} else {
				out[i] = r
			}
		} else {
			index[recordKey(r)] = len(out)
			out = append(out, r)
		}
	}
	return out
}

// ClusterResourceMetrics uses integer units. Nil means the source omitted that dimension.
type ClusterResourceMetrics struct {
	CPUMillis             *int64           `json:"cpu_millis"`
	MemoryBytes           *int64           `json:"memory_bytes"`
	EphemeralStorageBytes *int64           `json:"ephemeral_storage_bytes"`
	Pods                  *int64           `json:"pods"`
	Extended              map[string]int64 `json:"extended"`
}
type ClusterTopologyRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}
type ClusterTopologyTerm struct {
	Expressions []ClusterTopologyRequirement `json:"expressions"`
	Fields      []ClusterTopologyRequirement `json:"fields"`
}
type ClusterVolumeCapacity struct {
	Name          string                `json:"name"`
	UID           string                `json:"uid"`
	Class         string                `json:"class"`
	CapacityBytes *int64                `json:"capacity_bytes"`
	ClaimUID      string                `json:"claim_uid"`
	ReclaimPolicy string                `json:"reclaim_policy"`
	Phase         string                `json:"phase"`
	VolumeMode    string                `json:"volume_mode"`
	Driver        string                `json:"driver"`
	VolumeHandle  string                `json:"volume_handle"`
	Topology      []ClusterTopologyTerm `json:"topology"`
}
type ClusterStorageClass struct {
	Name           string                `json:"name"`
	UID            string                `json:"uid"`
	Provisioner    string                `json:"provisioner"`
	BindingMode    string                `json:"binding_mode"`
	ReclaimPolicy  string                `json:"reclaim_policy"`
	AllowExpansion *bool                 `json:"allow_expansion"`
	Topology       []ClusterTopologyTerm `json:"topology"`
}

func checkedClusterQuantity(q resource.Quantity, scale int64, whole bool) (*int64, error) {
	value, ok := new(mathbig.Rat).SetString(q.AsDec().String())
	if !ok || value.Sign() < 0 {
		return nil, ErrInvalid
	}
	value.Mul(value, new(mathbig.Rat).SetInt64(scale))
	integer, remainder := new(mathbig.Int), new(mathbig.Int)
	integer.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() != 0 {
		if whole {
			return nil, ErrInvalid
		}
		integer.Add(integer, mathbig.NewInt(1))
	}
	if !integer.IsInt64() {
		return nil, ErrInvalid
	}
	return number(integer.Int64()), nil
}
func clusterResourceMetrics(resources corev1.ResourceList) (ClusterResourceMetrics, error) {
	out := ClusterResourceMetrics{Extended: map[string]int64{}}
	for name, q := range resources {
		scale := int64(1)
		if name == corev1.ResourceCPU {
			scale = 1000
		}
		count := name == corev1.ResourcePods || strings.Contains(string(name), "/")
		value, err := checkedClusterQuantity(q, scale, count)
		if err != nil {
			return out, err
		}
		switch name {
		case corev1.ResourceCPU:
			out.CPUMillis = value
		case corev1.ResourceMemory:
			out.MemoryBytes = value
		case corev1.ResourceEphemeralStorage:
			out.EphemeralStorageBytes = value
		case corev1.ResourcePods:
			out.Pods = value
		default:
			out.Extended[string(name)] = *value
		}
	}
	return out, nil
}
func clusterStorageFacts(out *ClusterCapacitySnapshot, inventory kubernetes.CapacityInventory) error {
	for _, pv := range inventory.Volumes {
		v := ClusterVolumeCapacity{Name: pv.Name, UID: string(pv.UID), Class: pv.Spec.StorageClassName, ReclaimPolicy: string(pv.Spec.PersistentVolumeReclaimPolicy), Phase: string(pv.Status.Phase)}
		if q, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			var err error
			v.CapacityBytes, err = checkedClusterQuantity(q, 1, false)
			if err != nil {
				return err
			}
		}
		if pv.Spec.ClaimRef != nil {
			v.ClaimUID = string(pv.Spec.ClaimRef.UID)
		}
		if pv.Spec.VolumeMode != nil {
			v.VolumeMode = string(*pv.Spec.VolumeMode)
		}
		if pv.Spec.CSI != nil {
			v.Driver = pv.Spec.CSI.Driver
			v.VolumeHandle = pv.Spec.CSI.VolumeHandle
		}
		if pv.Spec.NodeAffinity != nil && pv.Spec.NodeAffinity.Required != nil {
			for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
				t := ClusterTopologyTerm{}
				for _, r := range term.MatchExpressions {
					t.Expressions = append(t.Expressions, ClusterTopologyRequirement{Key: r.Key, Operator: string(r.Operator), Values: append([]string(nil), r.Values...)})
				}
				for _, r := range term.MatchFields {
					t.Fields = append(t.Fields, ClusterTopologyRequirement{Key: r.Key, Operator: string(r.Operator), Values: append([]string(nil), r.Values...)})
				}
				v.Topology = append(v.Topology, t)
			}
		}
		out.Volumes = append(out.Volumes, v)
	}
	for _, class := range inventory.StorageClasses {
		c := ClusterStorageClass{Name: class.Name, UID: string(class.UID), Provisioner: class.Provisioner, AllowExpansion: class.AllowVolumeExpansion}
		if class.VolumeBindingMode != nil {
			c.BindingMode = string(*class.VolumeBindingMode)
		}
		if class.ReclaimPolicy != nil {
			c.ReclaimPolicy = string(*class.ReclaimPolicy)
		}
		for _, term := range class.AllowedTopologies {
			t := ClusterTopologyTerm{}
			for _, r := range term.MatchLabelExpressions {
				t.Expressions = append(t.Expressions, ClusterTopologyRequirement{Key: r.Key, Operator: "In", Values: append([]string(nil), r.Values...)})
			}
			c.Topology = append(c.Topology, t)
		}
		out.StorageClasses = append(out.StorageClasses, c)
	}
	return nil
}

func clusterCapacityIdentityInvalid(r *ManagedResource) bool {
	return r == nil || r.DeletedAt != nil || r.Status == "missing" || r.Status == "identity_changed"
}
