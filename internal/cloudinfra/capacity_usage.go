package cloudinfra

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// GetCapacityUsage reads inventory and reservations under the admission gate.
// Pending is the attempted subset of Reserved, not an additional allocation.
func (s *PgStore) GetCapacityUsage(ctx context.Context, org, cloud string) (*CapacityUsage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return nil, checkErr
	}
	p, err := capacityPolicyTx(ctx, tx, org, cloud)
	if err != nil {
		return nil, err
	}
	records, err := capacityRecordsTx(ctx, tx, org, cloud)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_server_pools WHERE org_id=$1 AND cloud_id=$2 ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	var pools []*Pool
	for rows.Next() {
		var raw []byte
		var pool Pool
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &pool); err != nil {
			rows.Close()
			return nil, err
		}
		pools = append(pools, &pool)
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	usage, err := summarizeCapacity(records, pools, *p, time.Now())
	if err != nil {
		return nil, err
	}
	missing, err := capacityInventoryIncomplete(ctx, tx, org, cloud)
	if err != nil {
		return nil, err
	}
	if missing {
		usage.Allocatable = CapacityResources{}
		usage.Observed = CapacityResources{}
		usage.Provisioned = CapacityResources{}
		usage.Fresh = false
		for i := range usage.GPUs {
			usage.GPUs[i].Provisioned = nil
		}
	}
	usage.Reserved = capacityResources(ResourceVector{})
	usage.Pending = capacityResources(ResourceVector{})
	reservations, err := capacityReservationsTx(ctx, tx, org)
	if err != nil {
		return nil, err
	}
	for _, r := range reservations {
		if r.cloud != cloud {
			continue
		}
		usage.Reserved, err = sumResources(usage.Reserved, capacityResources(r.reservation.Delta))
		if err != nil {
			return nil, err
		}
		gpus := r.reservation.Delta
		if gpus.GPUs > 0 {
			usage.GPUs = withGPUKind(usage.GPUs, gpus.GPUKind)
			gpuUsageFor(usage.GPUs, gpus.GPUKind).Reserved += gpus.GPUs
		}
		var attempted bool
		query := `SELECT coalesce((body->>'attempted')::boolean,false) FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`
		if r.reservation.ClusterOperationID != "" {
			query = `SELECT EXISTS(SELECT 1 FROM jsonb_array_elements(body->'steps') step WHERE step->>'state' IN ('attempted','observed','completed')) FROM loomplane_cluster_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`
		}
		if checkErr := tx.QueryRow(ctx, query, org, cloud, r.reservation.OperationID).Scan(&attempted); checkErr != nil {
			return nil, checkErr
		}
		if attempted {
			usage.Pending, err = sumResources(usage.Pending, capacityResources(r.reservation.Delta))
			if err != nil {
				return nil, err
			}
			if gpus.GPUs > 0 {
				gpuUsageFor(usage.GPUs, gpus.GPUKind).Pending += gpus.GPUs
			}
		}
	}
	// A kind only reservations hold has none in a fully known inventory.
	if _, known, gpuErr := inventoryGPUs(records); gpuErr == nil && known && !missing {
		for i := range usage.GPUs {
			if usage.GPUs[i].Provisioned == nil {
				usage.GPUs[i].Provisioned = number(0)
			}
		}
	}
	slices.SortFunc(usage.GPUs, func(a, b GPUUsage) int { return strings.Compare(a.Kind, b.Kind) })
	counted, err := sumResources(usage.Provisioned, usage.Reserved)
	if err != nil {
		return nil, err
	}
	if p.Revision > 0 {
		facts, limits := factValues(counted), vectorValues(p.Limits)
		var headroom [4]*int64
		for i, v := range facts {
			if v != nil {
				headroom[i] = number(max(0, limits[i]-*v))
			}
		}
		usage.Headroom = CapacityResources{Servers: headroom[0], CPUMillis: headroom[1], MemoryBytes: headroom[2], StorageBytes: headroom[3]}
	}
	rows, err = tx.Query(ctx, `SELECT a.body FROM loomplane_cloud_capacity_accounts a WHERE a.org_id=$1 AND (EXISTS(SELECT 1 FROM loomplane_cloud_capacity_inventory i WHERE i.org_id=a.org_id AND i.cloud_id=$2 AND i.provider=a.provider AND i.account_id=a.account_id) OR EXISTS(SELECT 1 FROM loomplane_managed_resources m WHERE m.org_id=a.org_id AND m.cloud_id=$2 AND m.kind='cluster' AND m.deleted_at IS NULL AND m.provider=a.provider AND m.account_id=a.account_id) OR EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations r WHERE r.org_id=a.org_id AND r.cloud_id=$2 AND r.body->>'provider'=a.provider AND r.body->>'account_id'=a.account_id)) ORDER BY a.provider,a.account_id`, org, cloud)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var account CapacityAccountSnapshot
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &account); err != nil {
			rows.Close()
			return nil, err
		}
		usage.Accounts = append(usage.Accounts, account)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return usage, nil
}
