package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/provider"
)

// reserveClusterCapacity uses the same reservation rows counted by server admission.
// The whole vector stays reserved until a later verified inventory handover.
func (s *PgStore) reserveClusterCapacity(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	p, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
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
		if !freshCapacity(r.CheckedAt, time.Now()) {
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
	if b := checkCapacityLimit(p, d, used, v, capacityResources(p.Limits), "hard_limit"); b != nil {
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
	accountUsed := account.Provisioned
	for _, r := range reservations {
		if r.reservation.OperationID != o.ID && r.reservation.Provider == o.Input.Provider && r.reservation.AccountID == o.Input.AccountID {
			accountUsed, err = sumResources(accountUsed, capacityResources(r.reservation.Delta))
			if err != nil {
				return err
			}
		}
	}
	quota := CapacityResources{Servers: account.Quota.Servers, CPUMillis: account.Quota.CPUMillis, MemoryBytes: account.Quota.MemoryBytes, StorageBytes: account.Quota.StorageBytes}
	if b := checkCapacityLimit(p, d, accountUsed, v, quota, "provider_quota"); b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	if existing {
		return nil
	}
	r := infrastructureReservation{OperationID: o.ID, ClusterOperationID: o.ID, Provider: o.Input.Provider, AccountID: o.Input.AccountID, Delta: v, Target: v, CreatedAt: time.Now().UTC()}
	raw, err = json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'reserved',$10)`, o.OrgID, o.CloudID, o.ID, p.Revision, o.Digest, v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes, raw)
	return storeError(err)
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
	return tx.Commit()
}
