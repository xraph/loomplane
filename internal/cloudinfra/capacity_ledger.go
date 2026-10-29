package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// CapacityAdmissionError preserves the structured reason for a blocked mutation.
type CapacityAdmissionError struct{ Block CapacityBlock }

func (e *CapacityAdmissionError) Unwrap() error { return ErrConflict }

func (e *CapacityAdmissionError) Error() string {
	return fmt.Sprintf("%s: infrastructure %s capacity", e.Block.Reason, e.Block.Resource)
}

type infrastructureReservation struct {
	ClusterOperationID string         `json:"cluster_operation_id,omitempty"`
	ManagedResourceID  string         `json:"managed_resource_id,omitempty"`
	OperationID        string         `json:"operation_id"`
	Provider           string         `json:"provider"`
	AccountID          string         `json:"account_id"`
	ServerID           string         `json:"server_id"`
	PoolID             string         `json:"pool_id"`
	Delta              ResourceVector `json:"delta"`
	Target             ResourceVector `json:"target"`
	CreatedAt          time.Time      `json:"created_at"`
}

// Organization serialization also covers accounts shared by several Clouds.
// Every capacity writer takes this gate before the Cloud and downstream locks.
func lockCapacityCloud(ctx context.Context, tx driver.Tx, org, cloud string) error {
	if err := cloudprofiles.LockOrganization(ctx, tx, org); err != nil {
		return err
	}
	var id string
	return storeError(tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, cloud).Scan(&id))
}
func capacityPolicyTx(ctx context.Context, tx driver.Tx, org, cloud string) (*CapacityPolicy, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2 FOR SHARE`, org, cloud).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return &CapacityPolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	var p CapacityPolicy
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func capacityBlock(p *CapacityPolicy, d CapacityDemand, reason, resource string) *CapacityBlock {
	return &CapacityBlock{Reason: reason, Resource: resource, CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID, PolicyRevision: p.Revision, Retryable: reason == "stale_inventory"}
}
func vectorValues(v ResourceVector) [4]int64 {
	return [4]int64{v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes}
}
func factValues(v CapacityResources) [4]*int64 {
	return [4]*int64{v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes}
}

var capacityDimensions = [4]string{"servers", "cpu_millis", "memory_bytes", "storage_bytes"}

func checkCapacityLimit(p *CapacityPolicy, d CapacityDemand, used CapacityResources, delta ResourceVector, limits CapacityResources, reason string) *CapacityBlock {
	counts, requested, ceilings := factValues(used), vectorValues(delta), factValues(limits)
	for i, name := range capacityDimensions {
		if requested[i] == 0 || ceilings[i] == nil {
			continue
		}
		if counts[i] == nil {
			return capacityBlock(p, d, "stale_inventory", name)
		}
		if *counts[i] > *ceilings[i] || requested[i] > *ceilings[i]-*counts[i] {
			b := capacityBlock(p, d, reason, name)
			b.Limit = *ceilings[i]
			b.Counted = *counts[i]
			b.Requested = requested[i]
			return b
		}
	}
	return nil
}

// ReserveInfrastructureCapacity runs before the provider effect in the caller's
// transaction. The operation pins immutable account identity and reviewed size.
func (s *PgStore) ReserveInfrastructureCapacity(ctx context.Context, tx driver.Tx, operationID string, d CapacityDemand, delta ResourceVector) (*CapacityBlock, error) {
	return s.reserveInfrastructureCapacity(ctx, tx, operationID, d, delta, false)
}

func (s *PgStore) reserveInfrastructureCapacity(ctx context.Context, tx driver.Tx, operationID string, d CapacityDemand, delta ResourceVector, revalidate bool) (*CapacityBlock, error) {
	if operationID == "" || !nonnegativeCapacity(delta) {
		return nil, ErrInvalid
	}
	if err := lockCapacityCloud(ctx, tx, d.OrgID, d.CloudID); err != nil {
		return nil, err
	}
	p, err := capacityPolicyTx(ctx, tx, d.OrgID, d.CloudID)
	if err != nil {
		return nil, err
	}
	if p.Revision == 0 {
		return nil, nil
	}
	if d.PolicyRevision != 0 && d.PolicyRevision != p.Revision {
		return capacityBlock(p, d, "review_required", "policy"), nil
	}
	o, err := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, d.OrgID, d.CloudID, operationID))
	if err != nil {
		return nil, err
	}
	var existing []byte
	var reservationStatus string
	err = tx.QueryRow(ctx, `SELECT body,status FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, d.OrgID, d.CloudID, operationID).Scan(&existing, &reservationStatus)
	switch {
	case err == nil:
		if reservationStatus != "reserved" && reservationStatus != "materialized" {
			return nil, ErrConflict
		}
		var r infrastructureReservation
		if checkErr := json.Unmarshal(existing, &r); checkErr != nil {
			return nil, checkErr
		}
		if r.Delta != delta || r.PoolID != d.PoolID {
			return nil, ErrConflict
		}
		if !revalidate {
			return nil, nil
		}
		if o.Attempted || reservationStatus != "reserved" || o.Input.Kind != "create" || o.CapacityTarget == nil || *o.CapacityTarget != r.Target || o.Provider != r.Provider || o.AccountID != r.AccountID || o.ServerID != r.ServerID {
			return nil, ErrConflict
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	case revalidate:
		return nil, ErrConflict
	}
	if o.CapacityTarget == nil || !nonnegativeCapacity(*o.CapacityTarget) || o.CapacityTarget.Servers != 1 || o.CapacityTarget.CPUMillis == 0 || o.CapacityTarget.MemoryBytes == 0 || (!revalidate && !freshCapacity(o.CapacityCheckedAt, time.Now())) {
		return capacityBlock(p, d, "stale_inventory", "request"), nil
	}
	if o.Input.Kind != "create" && o.Input.Kind != "adopt" && o.Input.Kind != "resize" {
		return nil, ErrInvalid
	}
	expected, deltaErr := operationCapacityDelta(ctx, tx, o, p, d)
	if deltaErr != nil {
		var blocked *CapacityAdmissionError
		if errors.As(deltaErr, &blocked) {
			return &blocked.Block, nil
		}
		return nil, deltaErr
	}
	if expected != delta {
		return nil, ErrInvalid
	}
	if delta == (ResourceVector{}) {
		return nil, nil
	}
	records, err := capacityRecordsTx(ctx, tx, d.OrgID, d.CloudID)
	if err != nil {
		return nil, err
	}
	used := capacityResources(ResourceVector{})
	for _, r := range records {
		if !freshCapacityRecord(r, time.Now()) {
			return capacityBlock(p, d, "stale_inventory", "inventory"), nil
		}
		if r.Observation != nil && r.ServerID != "" {
			var revision int64
			if err = tx.QueryRow(ctx, `SELECT coalesce((body->>'state_revision')::bigint,0) FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR SHARE`, d.OrgID, d.CloudID, r.ServerID).Scan(&revision); err != nil {
				return nil, storeError(err)
			}
			if revision != r.ServerStateRevision {
				return capacityBlock(p, d, "stale_inventory", "host"), nil
			}
		}
		used, err = sumResources(used, r.Resources)
		if err != nil {
			return nil, err
		}
	}
	// Registered resources absent from inventory are unknown, except for hosts
	// whose entire allocation is still held by a create or adoption reservation.
	missing, err := capacityInventoryIncomplete(ctx, tx, d.OrgID, d.CloudID)
	if err != nil {
		return nil, err
	}
	if missing {
		return capacityBlock(p, d, "stale_inventory", "inventory"), nil
	}
	reservations, err := capacityReservationsTx(ctx, tx, d.OrgID)
	if err != nil {
		return nil, err
	}
	// Revalidation compares current usage without counting the same reservation
	// twice. The durable row remains reserved throughout this transaction.
	if revalidate {
		filtered := make([]scopedCapacityReservation, 0, len(reservations))
		for _, r := range reservations {
			if r.cloud != d.CloudID || r.reservation.OperationID != operationID {
				filtered = append(filtered, r)
			}
		}
		reservations = filtered
	}
	for _, r := range reservations {
		if r.cloud == d.CloudID {
			used, err = sumResources(used, capacityResources(r.reservation.Delta))
			if err != nil {
				return nil, err
			}
		}
	}
	if b := checkCapacityLimit(p, d, used, delta, capacityResources(p.Limits), "hard_limit"); b != nil {
		return b, nil
	}
	// GPU ceilings are per kind, counted from inventory plus pending growth.
	if delta.GPUs > 0 {
		counted, countErr := cloudGPUs(records, reservations, d.CloudID, delta.GPUKind)
		if countErr != nil {
			return nil, countErr
		}
		if b := checkGPULimit(p, d, delta.GPUKind, counted, delta.GPUs); b != nil {
			return b, nil
		}
	}
	for _, pool := range p.Pools {
		var member bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE org_id=$1 AND pool_id=$2 AND server_id=$3)`, d.OrgID, pool.PoolID, o.ServerID).Scan(&member); checkErr != nil {
			return nil, checkErr
		}
		if pool.PoolID != d.PoolID && !member {
			continue
		}
		poolUsed, e := capacityPoolUsage(ctx, tx, d.OrgID, d.CloudID, pool.PoolID, records)
		if e != nil {
			return nil, e
		}
		// The pool's GPUs of the delta's kind, from its inventoried members plus
		// the reservations counted to it below.
		var poolGPUs *int64
		if delta.GPUs > 0 {
			members, memberErr := poolMemberSet(ctx, tx, d.OrgID, pool.PoolID)
			if memberErr != nil {
				return nil, memberErr
			}
			poolGPUs = poolInventoryGPUs(records, members, delta.GPUKind)
		}
		for _, r := range reservations {
			if r.cloud != d.CloudID {
				continue
			}
			var reservedMember bool
			if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE org_id=$1 AND pool_id=$2 AND server_id=$3)`, d.OrgID, pool.PoolID, r.reservation.ServerID).Scan(&reservedMember); checkErr != nil {
				return nil, checkErr
			}
			if r.reservation.PoolID == pool.PoolID || reservedMember {
				poolUsed, e = sumResources(poolUsed, capacityResources(r.reservation.Delta))
				if e != nil {
					return nil, e
				}
				if r.reservation.Delta.GPUKind == delta.GPUKind {
					if poolGPUs, e = addGPUCount(poolGPUs, r.reservation.Delta.GPUs); e != nil {
						return nil, e
					}
				}
			}
		}
		limits := CapacityResources{Servers: number(pool.MaxServers)}
		if pool.Limits != nil {
			limits = capacityResources(*pool.Limits)
			limits.Servers = number(min(pool.MaxServers, *limits.Servers))
		}
		poolDemand := d
		poolDemand.PoolID = pool.PoolID
		if b := checkCapacityLimit(p, poolDemand, poolUsed, delta, limits, "hard_limit"); b != nil {
			return b, nil
		}
		// capacityResources keeps four dimensions, so the pool's GPU ceiling is checked here.
		if b := checkPoolGPULimit(p, poolDemand, pool, delta.GPUKind, poolGPUs, delta.GPUs); b != nil {
			return b, nil
		}
	}

	if o.Provider != "server" {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3 FOR UPDATE`, d.OrgID, o.Provider, o.AccountID).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
			return capacityBlock(p, d, "stale_inventory", "account"), nil
		} else if err != nil {
			return nil, err
		}
		var account CapacityAccountSnapshot
		if checkErr := json.Unmarshal(raw, &account); checkErr != nil {
			return nil, checkErr
		}
		if !freshCapacity(account.CheckedAt, time.Now()) {
			return capacityBlock(p, d, "stale_inventory", "account"), nil
		}
		accountUsed := account.Provisioned
		for _, r := range reservations {
			if r.reservation.Provider == o.Provider && r.reservation.AccountID == o.AccountID {
				accountUsed, err = sumResources(accountUsed, capacityResources(r.reservation.Delta))
				if err != nil {
					return nil, err
				}
			}
		}
		// Adoption changes Cloud attribution, but does not consume provider quota.
		if o.Input.Kind != "adopt" {
			quota := CapacityResources{Servers: account.Quota.Servers, CPUMillis: account.Quota.CPUMillis, MemoryBytes: account.Quota.MemoryBytes, StorageBytes: account.Quota.StorageBytes}
			if b := checkCapacityLimit(p, d, accountUsed, delta, quota, "provider_quota"); b != nil {
				return b, nil
			}
		}
	}
	if revalidate {
		return nil, nil
	}
	r := infrastructureReservation{OperationID: operationID, Provider: o.Provider, AccountID: o.AccountID, ServerID: o.ServerID, PoolID: d.PoolID, Delta: delta, Target: *o.CapacityTarget, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,pool_id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES($1,$2,$3,nullif($4,''),$5,$3,$6,$7,$8,$9,'reserved',$10)`, d.OrgID, d.CloudID, operationID, d.PoolID, p.Revision, delta.Servers, delta.CPUMillis, delta.MemoryBytes, delta.StorageBytes, raw)
	return nil, storeError(err)
}

type scopedCapacityReservation struct {
	cloud       string
	reservation infrastructureReservation
}

func capacityReservationsTx(ctx context.Context, tx driver.Tx, org string) ([]scopedCapacityReservation, error) {
	rows, err := tx.Query(ctx, `SELECT cloud_id,body FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND status='reserved' ORDER BY cloud_id,id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scopedCapacityReservation
	for rows.Next() {
		var r scopedCapacityReservation
		var raw []byte
		if err := rows.Scan(&r.cloud, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r.reservation); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func capacityRecordsTx(ctx context.Context, tx driver.Tx, org, cloud string) ([]CapacityRecord, error) {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cloud_capacity_inventory WHERE org_id=$1 AND cloud_id=$2 ORDER BY server_id,id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapacityRecord
	for rows.Next() {
		var r CapacityRecord
		var raw []byte
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &r); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, r)
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	clusters, err := clusterCapacityRecordsTx(ctx, tx, org, cloud)
	if err != nil {
		return nil, err
	}
	return deduplicateClusterCapacity(out, clusters), nil
}

// poolMemberSet lists the servers in a pool.
func poolMemberSet(ctx context.Context, tx driver.Tx, org, pool string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT server_id FROM loomplane_server_pool_members WHERE org_id=$1 AND pool_id=$2 ORDER BY server_id`, org, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
func capacityPoolUsage(ctx context.Context, tx driver.Tx, org, _, pool string, records []CapacityRecord) (CapacityResources, error) {
	used := capacityResources(ResourceVector{})
	ids, err := poolMemberSet(ctx, tx, org, pool)
	if err != nil {
		return used, err
	}
	for _, r := range records {
		if ids[r.ServerID] {
			used, err = sumResources(used, r.Resources)
			if err != nil {
				return used, err
			}
			delete(ids, r.ServerID)
		}
	}
	if len(ids) > 0 {
		return CapacityResources{}, nil
	}
	return used, nil
}

func positiveCapacityDelta(target, old ResourceVector) ResourceVector {
	return ResourceVector{Servers: max(0, target.Servers-old.Servers), CPUMillis: max(0, target.CPUMillis-old.CPUMillis), MemoryBytes: max(0, target.MemoryBytes-old.MemoryBytes), StorageBytes: max(0, target.StorageBytes-old.StorageBytes)}
}
func (s *PgStore) reserveOperationCapacity(ctx context.Context, tx driver.Tx, o *Operation) error {
	if o.Input.Kind != "create" && o.Input.Kind != "adopt" && o.Input.Kind != "resize" {
		return nil
	}
	p, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	if p.Revision == 0 {
		return nil
	}
	d := CapacityDemand{OrgID: o.OrgID, CloudID: o.CloudID}
	var admitted []byte
	var reservationStatus string
	if readErr := tx.QueryRow(ctx, `SELECT body,status FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, o.OrgID, o.CloudID, o.ID).Scan(&admitted, &reservationStatus); readErr == nil {
		if reservationStatus != "reserved" && reservationStatus != "materialized" {
			return ErrConflict
		}
		var r infrastructureReservation
		if checkErr := json.Unmarshal(admitted, &r); checkErr != nil {
			return checkErr
		}
		if o.CapacityTarget == nil || *o.CapacityTarget != r.Target {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(readErr, pgx.ErrNoRows) {
		return readErr
	}
	if o.CapacityTarget == nil {
		resource := "request"
		if o.Input.Kind == "adopt" {
			resource = "storage_bytes"
		}
		return &CapacityAdmissionError{Block: *capacityBlock(p, d, "stale_inventory", resource)}
	}
	delta, err := operationCapacityDelta(ctx, tx, o, p, d)
	if err != nil {
		return err
	}
	b, err := s.ReserveInfrastructureCapacity(ctx, tx, o.ID, d, delta)
	if err != nil {
		return err
	}
	if b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	return nil
}

func checkPoolCapacityEdit(ctx context.Context, tx driver.Tx, pool *Pool) error {
	policy, err := capacityPolicyTx(ctx, tx, pool.OrgID, pool.CloudID)
	if err != nil {
		return err
	}
	var limits *PoolExpansionPolicy
	for i := range policy.Pools {
		if policy.Pools[i].PoolID == pool.ID {
			limits = &policy.Pools[i]
			break
		}
	}
	if limits == nil {
		return nil
	}
	records, err := capacityRecordsTx(ctx, tx, pool.OrgID, pool.CloudID)
	if err != nil {
		return err
	}
	old, err := capacityPoolUsage(ctx, tx, pool.OrgID, pool.CloudID, pool.ID, records)
	if err != nil {
		return err
	}
	// Removing members and retaining existing members remain possible after a
	// ceiling reduction. Only newly attributed hosts consume additional budget.
	oldIDs := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT server_id FROM loomplane_server_pool_members WHERE org_id=$1 AND pool_id=$2`, pool.OrgID, pool.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		oldIDs[id] = true
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return checkErr
	}
	delta := ResourceVector{}
	// The pool's GPU ceiling covers its own kind: GPUs already in the pool, and
	// the GPUs newly added members bring.
	gpuKind := limits.AcceleratorKind
	oldGPUs := poolInventoryGPUs(records, oldIDs, gpuKind)
	var addedGPUs int64
	gpusUnknown := false
	newIDs := map[string]bool{}
	for _, member := range pool.Members {
		newIDs[member.ServerID] = true
		if oldIDs[member.ServerID] {
			continue
		}
		found := false
		for _, record := range records {
			if record.ServerID != member.ServerID || record.Kind != "server" {
				continue
			}
			found = true
			facts := factValues(record.Resources)
			for _, value := range facts {
				if value == nil {
					return &CapacityAdmissionError{Block: CapacityBlock{Reason: "stale_inventory", Resource: "pool", CloudID: pool.CloudID, PoolID: pool.ID}}
				}
			}
			if !freshCapacity(record.CheckedAt, time.Now()) {
				return &CapacityAdmissionError{Block: CapacityBlock{Reason: "stale_inventory", Resource: "pool", CloudID: pool.CloudID, PoolID: pool.ID}}
			}
			delta, err = addCapacity(delta, ResourceVector{Servers: *facts[0], CPUMillis: *facts[1], MemoryBytes: *facts[2], StorageBytes: *facts[3]})
			if err != nil {
				return err
			}
			switch {
			case record.GPUs == nil:
				gpusUnknown = true
			case record.GPUKind == gpuKind:
				if *record.GPUs > math.MaxInt64-addedGPUs {
					return ErrInvalid
				}
				addedGPUs += *record.GPUs
			}
		}
		if !found {
			return &CapacityAdmissionError{Block: CapacityBlock{Reason: "stale_inventory", Resource: "pool", CloudID: pool.CloudID, PoolID: pool.ID}}
		}
	}
	reservations, err := capacityReservationsTx(ctx, tx, pool.OrgID)
	if err != nil {
		return err
	}
	counted := map[string]bool{}
	for _, scoped := range reservations {
		r := scoped.reservation
		if scoped.cloud != pool.CloudID || counted[r.OperationID] {
			continue
		}
		// Manual resize reservations carry the host identity without a pool ID.
		// Count their delta once, including when the edit adds that host.
		switch {
		case r.PoolID == pool.ID || oldIDs[r.ServerID]:
			old, err = sumResources(old, capacityResources(r.Delta))
			if err == nil && r.Delta.GPUKind == gpuKind {
				oldGPUs, err = addGPUCount(oldGPUs, r.Delta.GPUs)
			}
		case newIDs[r.ServerID]:
			// The four counted dimensions take any GPU kind; the pool's GPU
			// ceiling below counts only its own.
			delta, err = addCapacity(delta, withoutGPUs(r.Delta))
			if err == nil && r.Delta.GPUKind == gpuKind {
				if r.Delta.GPUs > math.MaxInt64-addedGPUs {
					return ErrInvalid
				}
				addedGPUs += r.Delta.GPUs
			}
		default:
			continue
		}
		if err != nil {
			return err
		}
		counted[r.OperationID] = true
	}
	ceilings := CapacityResources{Servers: number(limits.MaxServers)}
	if limits.Limits != nil {
		ceilings = capacityResources(*limits.Limits)
		if *ceilings.Servers > limits.MaxServers {
			ceilings.Servers = number(limits.MaxServers)
		}
	}
	demand := CapacityDemand{OrgID: pool.OrgID, CloudID: pool.CloudID, PoolID: pool.ID}
	if b := checkCapacityLimit(policy, demand, old, delta, ceilings, "hard_limit"); b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	// A new member whose GPUs Loomplane couldn't read can't join a GPU-capped pool.
	if gpusUnknown && limits.Limits != nil && limits.Limits.GPUs > 0 {
		return &CapacityAdmissionError{Block: *capacityBlock(policy, demand, "stale_inventory", "gpus:"+gpuKind)}
	}
	if b := checkPoolGPULimit(policy, demand, *limits, gpuKind, oldGPUs, addedGPUs); b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	return nil
}

// EnsureInfrastructureCapacity fences previously accepted, unattempted work when
// a policy was configured after its review. Provider calls happen after commit.
func (s *PgStore) EnsureInfrastructureCapacity(ctx context.Context, o *Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); err != nil {
		return err
	}
	if err := s.reserveOperationCapacity(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit()
}

func operationCapacityDelta(ctx context.Context, tx driver.Tx, o *Operation, p *CapacityPolicy, d CapacityDemand) (ResourceVector, error) {
	var err error
	delta := *o.CapacityTarget
	if o.Input.Kind == "resize" {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_inventory WHERE org_id=$1 AND cloud_id=$2 AND server_id=$3 AND resource_id=$4`, o.OrgID, o.CloudID, o.ServerID, "server:"+o.ResourceID).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
			return ResourceVector{}, &CapacityAdmissionError{Block: *capacityBlock(p, d, "stale_inventory", "server")}
		} else if err != nil {
			return ResourceVector{}, err
		}
		var r CapacityRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return ResourceVector{}, err
		}
		if !freshCapacityRecord(r, time.Now()) {
			return ResourceVector{}, &CapacityAdmissionError{Block: *capacityBlock(p, d, "stale_inventory", "server")}
		}
		facts := factValues(r.Resources)
		for _, v := range facts {
			if v == nil {
				return ResourceVector{}, &CapacityAdmissionError{Block: *capacityBlock(p, d, "stale_inventory", "server")}
			}
		}
		target := delta
		delta = positiveCapacityDelta(delta, ResourceVector{Servers: *facts[0], CPUMillis: *facts[1], MemoryBytes: *facts[2], StorageBytes: *facts[3]})
		// A resize onto GPUs adds the devices the server doesn't have yet.
		if target.GPUs > 0 {
			if r.GPUs == nil {
				return ResourceVector{}, &CapacityAdmissionError{Block: *capacityBlock(p, d, "stale_inventory", "gpus:"+target.GPUKind)}
			}
			have := int64(0)
			if r.GPUKind == target.GPUKind {
				have = *r.GPUs
			}
			if target.GPUs > have {
				delta.GPUKind, delta.GPUs = target.GPUKind, target.GPUs-have
			}
		}
	}
	return delta, nil
}

func capacityInventoryIncomplete(ctx context.Context, tx driver.Tx, org, cloud string) (bool, error) {
	var missing bool
	// Retirement status does not establish provider absence, even when this host
	// never had an inventory sample. Only an exact durable delete receipt does.
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM loomplane_cloud_servers s WHERE s.org_id=$1 AND s.cloud_id=$2
 AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_capacity_inventory i
 WHERE i.org_id=s.org_id AND i.cloud_id=s.cloud_id AND i.server_id=s.id)
 AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations r
 WHERE r.org_id=s.org_id AND r.cloud_id=s.cloud_id AND r.body->>'server_id'=s.id AND r.servers=1 AND r.status='reserved')
 AND NOT EXISTS(SELECT 1 FROM loomplane_infrastructure_operations o
 WHERE o.org_id=s.org_id AND o.cloud_id=s.cloud_id AND o.server_id=s.id
 AND o.body->'input'->>'kind'='delete' AND s.resource_id<>''
 AND o.body->>'provider'=s.provider AND o.body->>'account_id'=s.account_id AND o.body->>'resource_id'=s.resource_id
 AND coalesce(o.body->>'absence_confirmed_at','') NOT IN ('','0001-01-01T00:00:00Z'))
 ) OR EXISTS(SELECT 1 FROM loomplane_managed_resources r LEFT JOIN loomplane_cluster_capacity_inventory c ON c.org_id=r.org_id AND c.resource_id=r.id
 WHERE r.org_id=$1 AND r.cloud_id=$2 AND r.deleted_at IS NULL AND r.kind='cluster'
 AND (r.body->>'status' IN ('missing','identity_changed') OR c.body IS NULL OR NOT c.complete OR c.checked_at<now()-interval '2 minutes' OR c.checked_at>now()))`, org, cloud).Scan(&missing)
	return missing, err
}
