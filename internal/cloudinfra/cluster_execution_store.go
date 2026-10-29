package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

func clusterOperationTx(ctx context.Context, tx driver.Tx, org, cloud, id string) (*ClusterOperation, error) {
	return scanClusterOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_cluster_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, org, cloud, id))
}
func (s *PgStore) AcceptClusterOperation(ctx context.Context, o *ClusterOperation, limit int) error {
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
	if saved.Digest != o.Digest || saved.Revision != o.Revision || saved.Status != "preview" || !time.Now().Before(saved.ExpiresAt) {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if o.Kind == "create" {
		var active int
		if checkErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_operations WHERE org_id=$1 AND body->'input'->>'provider'=$2 AND body->'input'->>'account_id'=$3 AND body->>'kind'='create' AND status IN ('accepted','running','waiting_provider','needs_attention')`, o.OrgID, o.Input.Provider, o.Input.AccountID).Scan(&active); checkErr != nil {
			return checkErr
		}
		if limit < 1 || limit > 2 || active >= limit {
			return ErrBusy
		}
		if checkErr := s.reserveClusterCapacity(ctx, tx, o); checkErr != nil {
			return checkErr
		}
		o.ReservationIDs = []string{o.ID}
	} else {
		var active bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM loomplane_cluster_operations o
 LEFT JOIN loomplane_managed_resources r ON r.org_id=o.org_id AND r.id=o.resource_id
 WHERE o.org_id=$1 AND o.body->'input'->>'provider'=$2 AND o.body->'input'->>'account_id'=$3
 AND o.status IN ('accepted','running','waiting_provider','needs_attention')
 AND (o.body->'input'->'ref'->>'id'=$4
 OR (r.provider=$2 AND r.account_id=$3 AND r.external_id=$4)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(o.body->'receipts','[]'::jsonb)) receipt WHERE receipt->'ref'->>'account_id'=$3 AND receipt->'ref'->>'id'=$4)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_operation_steps step WHERE step.org_id=o.org_id AND step.operation_id=o.id AND step.body->>'remote_resource_id'=$4)))`, o.OrgID, o.Input.Provider, o.Input.AccountID, o.Input.Ref.ID).Scan(&active); checkErr != nil {
			return checkErr
		}
		if active {
			return ErrBusy
		}
	}
	o.Status = "accepted"
	o.Attempt = 1
	o.Revision++
	o.UpdatedAt = time.Now().UTC()
	if checkErr := checkpointClusterTx(ctx, tx, o, saved.Revision); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}
func (s *PgStore) LeaseClusterOperation(ctx context.Context, org, id, owner string, duration time.Duration) (*ClusterOperation, error) {
	if owner == "" || duration <= 0 || duration > 2*time.Minute {
		return nil, ErrInvalid
	}
	var cloud string
	if err := s.db.QueryRow(ctx, `SELECT cloud_id FROM loomplane_cluster_operations WHERE org_id=$1 AND id=$2`, org, id).Scan(&cloud); err != nil {
		return nil, storeError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return nil, checkErr
	}
	o, err := clusterOperationTx(ctx, tx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if o.Status != "accepted" && o.Status != "running" && o.Status != "waiting_provider" {
		return nil, ErrPending
	}
	if o.NextAttemptAt.After(now) || (o.LeaseExpiresAt != nil && o.LeaseExpiresAt.After(now)) {
		return nil, ErrBusy
	}
	o.FencingEpoch++
	o.Revision++
	o.LeaseOwner = owner
	expires := now.Add(duration)
	o.LeaseExpiresAt = &expires
	o.Status = "running"
	o.UpdatedAt = now
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET fencing_epoch=$4,revision=$5,status=$6,body=$7,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id, o.FencingEpoch, o.Revision, o.Status, raw)
	if err != nil {
		return nil, err
	}
	if checkErr := appendClusterEvent(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return o, nil
}
func (s *PgStore) ControlClusterOperation(ctx context.Context, org, cloud, id string, cancel bool) (*ClusterOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return nil, checkErr
	}
	o, err := clusterOperationTx(ctx, tx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	if o.Status == "succeeded" || o.Status == "cancelled" || o.Status == "failed" {
		return o, nil
	}
	if !cancel && (o.Status != "needs_attention" || o.CancelRequested) {
		return nil, ErrConflict
	}
	if cancel {
		o.CancelRequested = true
		if len(o.Steps) > 0 && o.Steps[0].State == "pending" {
			o.Status = "cancelled"
			if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET status='released' WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='reserved'`, org, cloud, id); err != nil {
				return nil, err
			}
		} else {
			o.Status = "waiting_provider"
		}
	} else {
		o.Status = "accepted"
		o.Error = ""
	}
	// Invalidating the epoch prevents an in-flight worker from checkpointing after cancellation.
	// Its attempted marker still forbids a second provider mutation.
	o.FencingEpoch++
	o.Revision++
	o.Attempt++
	o.UpdatedAt = time.Now().UTC()
	o.LeaseOwner = ""
	o.LeaseExpiresAt = nil
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET fencing_epoch=$4,revision=$5,status=$6,body=$7,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id, o.FencingEpoch, o.Revision, o.Status, raw)
	if err != nil {
		return nil, err
	}
	if checkErr := appendClusterEvent(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return o, nil
}
func (s *PgStore) CheckpointClusterResource(ctx context.Context, o *ClusterOperation, r *ManagedResource, expected int64) error {
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
	if saved.Revision != expected || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now()) {
		return ErrConflict
	}
	if r != nil {
		if r.OrgID != o.OrgID || r.CloudID != o.CloudID {
			return ErrInvalid
		}
		if _, err = claimManagedResourceTx(ctx, tx, r, r.Revision-1); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET body=jsonb_set(body,'{managed_resource_id}',to_jsonb($4::text)) WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, o.OrgID, o.CloudID, o.ID, r.ID); err != nil {
			return err
		}
	}
	if checkErr := checkpointClusterTx(ctx, tx, o, expected); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func clusterPinsTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2`, o.OrgID, o.CloudID).Scan(&revision); err != nil {
		return err
	}
	if revision != o.Input.CloudRevision {
		return ErrConflict
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND cloud=$3 AND generation=$4 AND status='ready')`, o.OrgID, o.Input.ConnectionID, o.Input.Provider, o.Input.ConnectionGeneration).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	if a := o.Input.Attachment; a != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2 AND project_id=$3 AND revision=$4 AND NOT revoked AND cardinality(target_ids)=0)`, o.OrgID, o.CloudID, a.ProjectID, a.GrantRevision).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrConflict
		}
	}
	return nil
}
