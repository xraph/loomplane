package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/grove/driver"
)

// ReleaseInfrastructureCapacity requires a durable provider absence receipt.
// Changing an operation or reservation status never supplies that evidence.
func (s *PgStore) ReleaseInfrastructureCapacity(ctx context.Context, tx driver.Tx, org, operationID string) error {
	var cloud string
	if err := tx.QueryRow(ctx, `SELECT cloud_id FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND id=$2`, org, operationID).Scan(&cloud); err != nil {
		return storeError(err)
	}
	if err := lockCapacityCloud(ctx, tx, org, cloud); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET status='released' WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND cleanup_confirmed_at IS NOT NULL`, org, cloud, operationID)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Transfer only after the immutable receipt is present in a sufficiently recent
// inventory sample. Keep the row as an idempotency record for operation retries.
func reconcileCapacityReservations(ctx context.Context, tx driver.Tx, org, cloud string, records []CapacityRecord) error {
	reservations, err := capacityReservationsTx(ctx, tx, org)
	if err != nil {
		return err
	}
	for _, scoped := range reservations {
		if scoped.cloud != cloud {
			continue
		}
		r := scoped.reservation
		var resource string
		if err = tx.QueryRow(ctx, `SELECT body->>'resource_id' FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, r.OperationID).Scan(&resource); err != nil {
			return storeError(err)
		}
		if resource == "" {
			continue
		}
		for _, record := range records {
			if record.Provider != r.Provider || record.AccountID != r.AccountID || record.ResourceID != "server:"+resource || record.ServerID != r.ServerID || record.CheckedAt.Before(r.CreatedAt) {
				continue
			}
			complete := true
			facts, target := factValues(record.Resources), vectorValues(r.Target)
			for i, value := range facts {
				if value == nil || *value < target[i] {
					complete = false
				}
			}
			// GPUs the reservation holds stay held until inventory shows them.
			if r.Target.GPUs > 0 && (record.GPUs == nil || record.GPUKind != r.Target.GPUKind || *record.GPUs < r.Target.GPUs) {
				complete = false
			}
			if complete {
				if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET status='materialized' WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='reserved'`, org, cloud, r.OperationID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func recordCapacityCleanup(ctx context.Context, tx driver.Tx, o *Operation) error {
	if o.Input.Kind != "delete" || o.AbsenceConfirmedAt.IsZero() || o.ResourceID == "" {
		return nil
	}
	// Invalidate any collection that started before the absence was recorded.
	if _, err := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_refreshes SET issued_generation=issued_generation+1 WHERE org_id=$1 AND cloud_id=$2`, o.OrgID, o.CloudID); err != nil {
		return err
	}
	// A provider GetServer absence receipt names one immutable server. Retained
	// volumes have distinct identities and remain attributed until their own scan.
	if _, err := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations r SET cleanup_confirmed_at=$6,status='released' FROM loomplane_infrastructure_operations original WHERE r.org_id=$1 AND r.cloud_id=$2 AND original.org_id=r.org_id AND original.cloud_id=r.cloud_id AND original.id=r.id AND r.body->>'provider'=$3 AND r.body->>'account_id'=$4 AND original.body->>'resource_id'=$5`, o.OrgID, o.CloudID, o.Provider, o.AccountID, o.ResourceID, o.AbsenceConfirmedAt); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM loomplane_cloud_capacity_inventory WHERE org_id=$1 AND cloud_id=$2 AND provider=$3 AND account_id=$4 AND resource_id=$5`, o.OrgID, o.CloudID, o.Provider, o.AccountID, "server:"+o.ResourceID)
	return err
}

func retainDisconnectedCapacity(ctx context.Context, tx driver.Tx, org, cloud string, records []CapacityRecord) ([]CapacityRecord, error) {
	rows, err := tx.Query(ctx, `SELECT i.body,coalesce((s.body->>'state_revision')::bigint,0),s.body FROM loomplane_cloud_capacity_inventory i JOIN loomplane_cloud_servers s ON s.org_id=i.org_id AND s.cloud_id=i.cloud_id AND s.id=i.server_id WHERE i.org_id=$1 AND i.cloud_id=$2 AND s.status IN ('disconnected','deleted') ORDER BY s.id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for _, r := range records {
		seen[recordKey(r)] = true
	}
	for rows.Next() {
		var raw, host []byte
		var revision int64
		if err := rows.Scan(&raw, &revision, &host); err != nil {
			return nil, err
		}
		var r CapacityRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if seen[recordKey(r)] {
			continue
		}
		// A record saved before GPU counts reads them from the host's last check.
		if r.Kind == "server" && r.GPUs == nil {
			var v Server
			if err := json.Unmarshal(host, &v); err != nil {
				return nil, err
			}
			r.GPUKind, r.GPUs = serverGPUFacts(&v, nil)
		}
		r.ServerStateRevision = revision
		r.Observation = nil
		records = append(records, r)
	}
	return records, rows.Err()
}
