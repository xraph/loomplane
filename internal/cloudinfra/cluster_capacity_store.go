package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
	kubeclient "k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterCapacityStore interface {
	BeginClusterCapacityObservation(context.Context, string, string, string) (int64, error)
	SaveClusterCapacity(context.Context, *ManagedResource, ClusterCapacitySnapshot) error
}

func (s *Service) ObserveClusterCapacity(ctx context.Context, p security.Principal, cloud, resource string) (*ClusterCapacitySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	if err := s.Authz.Require(ctx, p, security.ClusterManage, ""); err != nil {
		return nil, err
	}
	profile, err := s.authorize(ctx, p, cloud)
	if err != nil {
		return nil, err
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, resource)
	if err != nil {
		return nil, err
	}
	if r.Kind != "cluster" || r.CloudID != cloud || clusterCapacityIdentityInvalid(r) || r.Observation == nil {
		return nil, ErrConflict
	}
	reader, access, err := s.clusterAccount(ctx, p, r.ConnectionID, provider.ClusterCapacityObservation)
	if err != nil {
		return nil, err
	}
	capacity, ok := reader.(provider.ClusterCapacityReader)
	if !ok {
		return nil, provider.ErrInfrastructureUnsupported
	}
	st, ok := s.Clusters.(ClusterCapacityStore)
	if !ok {
		return nil, ErrInvalid
	}
	revision, err := st.BeginClusterCapacityObservation(ctx, p.OrgID, cloud, r.ID)
	if err != nil {
		return nil, err
	}
	facts, providerErr := capacity.ObserveClusterCapacity(ctx, access, r.Observation.Ref)
	if providerErr != nil {
		facts = provider.ProviderClusterCapacity{Provider: r.Provider, Ref: r.Observation.Ref, CheckedAt: s.now().UTC(), Conditions: []provider.ClusterCondition{{Type: "capacity", Status: "unknown", Reason: "provider_read_failed"}}}
	}
	in := ClusterInput{Kind: "management-observation", Provider: r.Provider, AccountID: r.AccountID, ConnectionID: r.ConnectionID, ConnectionGeneration: access.Generation, CloudRevision: profile.Revision, ResourceID: r.ID, ResourceRevision: r.Revision, Ref: r.Observation.Ref}
	if authority, ok := ctx.Value(clusterScalingObservationKey{}).(ClusterScalingObservation); ok {
		in.ScalingObservation = &authority
	}
	var inventory kubernetes.CapacityInventory
	readErr := providerErr
	if providerErr == nil {
		_, readErr = s.observeClusterManagementWith(ctx, p, cloud, in, r, reader, access, func(readCtx context.Context, client kubeclient.Interface) (ClusterManagementFacts, error) {
			var e error
			inventory, e = kubernetes.ReadCapacityInventory(readCtx, client)
			return ClusterManagementFacts{Complete: inventory.Complete, ObservedAt: inventory.CheckedAt}, e
		}, provider.ClusterCapacityObservation)
	}
	if readErr != nil {
		inventory.Complete = false
		if inventory.CheckedAt.IsZero() {
			inventory.CheckedAt = s.now().UTC()
		}
	}
	snapshot, err := MergeClusterCapacity(facts, KubernetesClusterCapacity{Provider: r.Provider, Ref: r.Observation.Ref, Inventory: inventory})
	if err != nil {
		return nil, err
	}
	snapshot.ResourceID = r.ID
	snapshot.ObservationRevision = revision
	if readErr != nil {
		snapshot.Conditions = append(snapshot.Conditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: "management_read_failed"})
	}
	if checkErr := s.Adapters.Require(r.Provider, provider.ClusterCapacityObservation); checkErr != nil {
		return nil, checkErr
	}
	if _, _, err = s.clusterAuthority(ctx, p, cloud, in); err != nil {
		return nil, err
	}
	if checkErr := s.Authz.Require(ctx, p, security.ClusterManage, ""); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := st.SaveClusterCapacity(ctx, r, snapshot); checkErr != nil {
		return nil, checkErr
	}
	return &snapshot, nil
}

func (s *PgStore) BeginClusterCapacityObservation(ctx context.Context, org, cloud, resource string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return 0, checkErr
	}
	var revision int64
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cluster_capacity_inventory(org_id,cloud_id,resource_id,issued_revision) SELECT org_id,cloud_id,id,1 FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND kind='cluster' AND deleted_at IS NULL AND coalesce(body->>'status','') NOT IN ('missing','identity_changed') ON CONFLICT(org_id,resource_id) DO UPDATE SET issued_revision=loomplane_cluster_capacity_inventory.issued_revision+1 WHERE loomplane_cluster_capacity_inventory.issued_revision<9223372036854775807 RETURNING issued_revision`, org, cloud, resource).Scan(&revision)
	if err != nil {
		return 0, storeError(err)
	}
	return revision, tx.Commit()
}
func (s *PgStore) SaveClusterCapacity(ctx context.Context, r *ManagedResource, snapshot ClusterCapacitySnapshot) error {
	if snapshot.CheckedAt.IsZero() || snapshot.CheckedAt.After(time.Now()) {
		return ErrInvalid
	}
	workloads := map[string]bool{}
	for _, w := range snapshot.Workloads {
		if w.PodUID == "" || workloads[w.PodUID] {
			return ErrInvalid
		}
		workloads[w.PodUID] = true
		if _, err := clusterWorkloadUsage(w.Requests, nil); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.ExternalVMID == "" || node.PoolID == "" || seen[node.ExternalVMID] || (snapshot.Complete && node.NodeUID == "") {
			return ErrInvalid
		}
		seen[node.ExternalVMID] = true
		if _, err := sumResources(capacityResources(ResourceVector{}), node.Provisioned); err != nil {
			return err
		}
		for _, metrics := range []ClusterResourceMetrics{node.Allocatable, node.Requests} {
			for _, value := range []*int64{metrics.CPUMillis, metrics.MemoryBytes, metrics.EphemeralStorageBytes, metrics.Pods} {
				if value != nil && *value < 0 {
					return ErrInvalid
				}
			}
			for _, value := range metrics.Extended {
				if value < 0 {
					return ErrInvalid
				}
			}
		}
	}

	if r == nil || r.Kind != "cluster" || r.Observation == nil || snapshot.ResourceID != r.ID || snapshot.Provider != r.Provider || snapshot.AccountID != r.AccountID || snapshot.Ref != r.Observation.Ref || snapshot.ObservationRevision <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, r.OrgID, r.CloudID); checkErr != nil {
		return checkErr
	}
	current, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 FOR UPDATE`, r.OrgID, r.ID))
	if err != nil {
		return err
	}
	if clusterCapacityIdentityInvalid(r) || clusterCapacityIdentityInvalid(current) || current.Revision != r.Revision || retirementResourceFingerprint(current) != retirementResourceFingerprint(r) {
		return ErrConflict
	}
	snapshot.ResourceFingerprint = retirementResourceFingerprint(r)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cluster_capacity_inventory SET saved_revision=$4,body=$5,checked_at=$6,complete=$7 WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 AND issued_revision=$4 AND saved_revision<$4 AND (checked_at IS NULL OR checked_at<=$6)`, r.OrgID, r.CloudID, r.ID, snapshot.ObservationRevision, raw, snapshot.CheckedAt, snapshot.Complete)
	if err != nil {
		return err
	}
	count, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	if err := settleClusterEnvelopes(ctx, tx, current, snapshot, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// clusterCapacityRecordsTx reads separately stored observations, so a server refresh cannot remove them.
func clusterCapacityRecordsTx(ctx context.Context, tx driver.Tx, org, cloud string) ([]CapacityRecord, error) {
	rows, err := tx.Query(ctx, `SELECT c.body,r.body FROM loomplane_cluster_capacity_inventory c JOIN loomplane_managed_resources r ON r.org_id=c.org_id AND r.id=c.resource_id WHERE c.org_id=$1 AND c.cloud_id=$2 AND r.deleted_at IS NULL AND c.body IS NOT NULL ORDER BY c.resource_id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []CapacityRecord
	for rows.Next() {
		var raw, resourceRaw []byte
		var current ManagedResource
		var snapshot ClusterCapacitySnapshot
		if checkErr := rows.Scan(&raw, &resourceRaw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &snapshot); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(resourceRaw, &current); checkErr != nil {
			return nil, checkErr
		}
		if snapshot.ResourceFingerprint != retirementResourceFingerprint(&current) {
			snapshot.Complete = false
			snapshot.Conditions = append(snapshot.Conditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: "resource_trust_changed"})
		}
		if clusterCapacityIdentityInvalid(&current) {
			snapshot.Complete = false
			snapshot.Conditions = append(snapshot.Conditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: "resource_" + current.Status})
		}
		records = append(records, clusterCapacityRecords(snapshot)...)
	}
	return records, rows.Err()
}
func clusterCapacityRecords(snapshot ClusterCapacitySnapshot) []CapacityRecord {
	records := make([]CapacityRecord, 0, len(snapshot.Nodes)+1)
	for _, node := range snapshot.Nodes {
		records = append(records, CapacityRecord{Kind: "cluster_node", Provider: snapshot.Provider, AccountID: snapshot.AccountID, ResourceID: "server:" + node.ExternalVMID, Resources: node.Provisioned, CheckedAt: snapshot.CheckedAt, ClusterResourceID: snapshot.ResourceID, ClusterNode: &node, ClusterCheckedAt: &snapshot.CheckedAt, ClusterConditions: snapshot.Conditions, ClusterComplete: snapshot.Complete})
	}
	if !snapshot.Complete || len(snapshot.Nodes) == 0 {
		records = append(records, CapacityRecord{Kind: "cluster_inventory", Provider: snapshot.Provider, AccountID: snapshot.AccountID, ResourceID: "cluster:" + snapshot.ResourceID, CheckedAt: snapshot.CheckedAt, ClusterResourceID: snapshot.ResourceID, ClusterCheckedAt: &snapshot.CheckedAt, ClusterConditions: snapshot.Conditions, ClusterComplete: snapshot.Complete, Resources: capacityResources(ResourceVector{})})
	}
	return records
}
