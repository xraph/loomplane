package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

type ClusterStore interface {
	CreateClusterPreview(context.Context, *ClusterOperation) error
	GetClusterOperation(context.Context, string, string, string) (*ClusterOperation, error)
	SaveClusterCheckpoint(context.Context, *ClusterOperation, int64) error
	ListPendingClusterOperations(context.Context) ([]*ClusterOperation, error)
	GetManagedResource(context.Context, string, string) (*ManagedResource, error)
	ClaimManagedResource(context.Context, *ManagedResource, int64) (*ManagedResource, error)
	ListManagedResources(context.Context, string, string, string, int) (ResourcePage, error)
	SaveRuntimeAttachment(context.Context, *RuntimeAttachment, int64) error
	GetRuntimeAttachment(context.Context, string, string) (*RuntimeAttachment, error)
}

var _ ClusterStore = (*PgStore)(nil)

func scanClusterOperation(row pgx.Row) (*ClusterOperation, error) {
	var raw, principal []byte
	if err := row.Scan(&raw, &principal); err != nil {
		return nil, storeError(err)
	}
	var o ClusterOperation
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(principal, &o.Principal); err != nil {
		return nil, err
	}
	return &o, nil
}
func (s *PgStore) GetClusterOperation(ctx context.Context, org, cloud, id string) (*ClusterOperation, error) {
	return scanClusterOperation(s.db.QueryRow(ctx, `SELECT body,principal FROM loomplane_cluster_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id))
}
func (s *PgStore) ListPendingClusterOperations(ctx context.Context) ([]*ClusterOperation, error) {
	rows, err := s.db.Query(ctx, `SELECT body,principal FROM loomplane_cluster_operations WHERE status IN ('accepted','running','waiting_provider') ORDER BY updated_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ClusterOperation{}
	for rows.Next() {
		o, e := scanClusterOperation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *PgStore) CreateClusterPreview(ctx context.Context, o *ClusterOperation) error {
	if o == nil || o.Status != "preview" || o.Revision != 1 || o.SchemaVersion != 1 || o.Digest == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	if checkErr := prepareTemporaryHoldTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	policy, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	if o.CapacityTarget.Servers > 0 {
		o.Input.BudgetReview, err = scopedBudgetReviewTx(ctx, tx, o.OrgID, o.CloudID, o.Input.Provider, o.Input.AccountID, o.ID, o.MoneyEstimate, policy, o.Input.EnvelopeReview)
		if err != nil {
			return err
		}
		if o.Input.BudgetReview != nil || o.Input.EnvelopeReview != nil {
			o.Digest = clusterDigest(o.Input)
			for i := range o.Steps {
				o.Steps[i].RequestDigest = o.Digest
			}
		}
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	principal, err := json.Marshal(o.Principal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_operations(id,org_id,cloud_id,resource_id,status,revision,digest,fencing_epoch,body,principal) VALUES($1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9,$10)`, o.ID, o.OrgID, o.CloudID, o.ResourceID, o.Status, o.Revision, o.Digest, o.FencingEpoch, raw, principal)
	if err != nil {
		return storeError(err)
	}
	if err := saveClusterSteps(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PgStore) SaveClusterCheckpoint(ctx context.Context, o *ClusterOperation, expectedRevision int64) error {
	if o == nil || expectedRevision < 1 || o.Revision != expectedRevision+1 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	if err := checkpointClusterTx(ctx, tx, o, expectedRevision); err != nil {
		return err
	}
	return tx.Commit()
}
func checkpointClusterTx(ctx context.Context, tx driver.Tx, o *ClusterOperation, expectedRevision int64) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	principal, err := json.Marshal(o.Principal)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET status=$4,revision=$5,body=$6,fencing_epoch=$7,updated_at=now(),resource_id=nullif($11,'') WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND revision=$8 AND digest=$9 AND fencing_epoch=$7 AND (fencing_epoch=0 OR (body->>'lease_owner'=$6::jsonb->>'lease_owner' AND (body->>'lease_expires_at')::timestamptz>clock_timestamp())) AND body->'money_estimate' IS NOT DISTINCT FROM $6::jsonb->'money_estimate' AND body->'input'=$6::jsonb->'input' AND principal=$10::jsonb AND body->'capacity_target'=$6::jsonb->'capacity_target' AND body->'kind'=$6::jsonb->'kind' AND body->'schema_version'=$6::jsonb->'schema_version' AND body->'expires_at'=$6::jsonb->'expires_at' AND body->'created_at'=$6::jsonb->'created_at' AND (resource_id IS NULL OR resource_id=nullif($11,''))`, o.OrgID, o.CloudID, o.ID, o.Status, o.Revision, raw, o.FencingEpoch, expectedRevision, o.Digest, principal, o.ResourceID)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return saveClusterSteps(ctx, tx, o)
}
func saveClusterSteps(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	seen := make(map[string]struct{}, len(o.Steps))
	for _, step := range o.Steps {
		if _, exists := seen[step.Name]; exists {
			return ErrInvalid
		}
		seen[step.Name] = struct{}{}
	}

	for _, step := range o.Steps {
		raw, err := json.Marshal(step)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO loomplane_cluster_operation_steps(org_id,operation_id,name,state,request_digest,body) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(org_id,operation_id,name) DO UPDATE SET state=excluded.state,body=excluded.body WHERE loomplane_cluster_operation_steps.request_digest=excluded.request_digest AND array_position(ARRAY['pending','attempted','observed','completed'],loomplane_cluster_operation_steps.state)<=array_position(ARRAY['pending','attempted','observed','completed'],excluded.state) AND (loomplane_cluster_operation_steps.body->'attempted_at' IS NULL OR loomplane_cluster_operation_steps.body->'attempted_at'=excluded.body->'attempted_at') AND (coalesce(loomplane_cluster_operation_steps.body->>'remote_operation_id','')='' OR loomplane_cluster_operation_steps.body->'remote_operation_id'=excluded.body->'remote_operation_id') AND (coalesce(loomplane_cluster_operation_steps.body->>'remote_resource_id','')='' OR loomplane_cluster_operation_steps.body->'remote_resource_id'=excluded.body->'remote_resource_id')`, o.OrgID, o.ID, step.Name, step.State, step.RequestDigest, raw)
		if err != nil {
			return storeError(err)
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_operation_steps WHERE org_id=$1 AND operation_id=$2`, o.OrgID, o.ID).Scan(&count); err != nil {
		return err
	}
	if count != len(o.Steps) {
		return ErrConflict
	}
	if retirementKind(o.Kind) {
		raw, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_cluster_retirement_receipts(org_id,cloud_id,operation_id,resource_id,revision,body) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(org_id,operation_id) DO UPDATE SET revision=excluded.revision,body=excluded.body,updated_at=now() WHERE loomplane_cluster_retirement_receipts.revision<excluded.revision`, o.OrgID, o.CloudID, o.ID, o.Input.ResourceID, o.Revision, raw); err != nil {
			return err
		}
	}
	return appendClusterEvent(ctx, tx, o)
}
