package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

func (s *PgStore) ClusterRetirementDependencies(ctx context.Context, org, resource, attachment, operation string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out, err := retirementDependenciesTx(ctx, tx, org, resource, attachment, operation)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func retirementDependenciesTx(ctx context.Context, tx driver.Tx, org, resource, attachment, operation string) ([]string, error) {
	rows, err := tx.Query(ctx, `WITH attachments AS (SELECT * FROM loomplane_runtime_attachments WHERE org_id=$1 AND cluster_id=$2 AND ($3='' OR id=$3))
 SELECT 'attachment:'||id FROM attachments WHERE $3='' AND deleted_at IS NULL
 UNION SELECT 'connection_use:'||u.kind||':'||u.owner_id FROM loomplane_connection_uses u JOIN attachments a ON a.org_id=u.org_id AND a.runtime_connection_id=u.connection_id WHERE u.kind NOT IN ('cloud_target')
 UNION SELECT 'binding:'||b.id FROM loomplane_bindings b JOIN attachments a ON a.org_id=b.org_id AND a.runtime_connection_id=b.connection_id
 UNION SELECT 'operation:'||id FROM loomplane_cluster_operations WHERE org_id=$1 AND id<>$4 AND status IN ('accepted','running','waiting_provider','needs_attention') AND (resource_id=$2 OR body->'input'->>'resource_id'=$2)
 UNION SELECT 'reservation:'||id FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND status IN ('reserved','committed') AND body->>'managed_resource_id'=$2
 UNION SELECT 'workload_hold:'||id FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND resource_id=$2 AND NOT released AND ($3='' OR body->'input'->>'attachment_id'=$3)
 UNION SELECT 'credential_refresh:'||f.attachment_id FROM loomplane_cluster_credential_refresh f JOIN attachments a ON a.org_id=f.org_id AND a.id=f.attachment_id WHERE f.lease_until>clock_timestamp()
 UNION SELECT 'deployment:'||d.id FROM loomplane_deployments d WHERE d.org_id=$1 AND d.status NOT IN ('succeeded','failed','cancelled') AND loomplane_plan_uses_cluster(d.org_id,d.plan_id,$2)
 ORDER BY 1`, org, resource, attachment, operation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var reason string
		if checkErr := rows.Scan(&reason); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, reason)
	}
	return out, rows.Err()
}

func retirementPinsTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if o.Input.Retirement == nil {
		return ErrInvalid
	}
	resource, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, o.OrgID, o.CloudID, o.Input.ResourceID))
	if err != nil {
		return err
	}
	if checkErr := checkRetirementResourcePins(resource, o.CloudID, o.Input, retirementRecoveryStarted(o)); checkErr != nil {
		return checkErr
	}
	var valid bool
	attachment := ""
	if o.Kind == "detach-runtime" {
		a := o.Input.Retirement.Attachment
		if a == nil {
			return ErrInvalid
		}
		attachment = a.ID
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_runtime_attachments WHERE org_id=$1 AND id=$2 AND cluster_id=$3 AND ((revision=$4 AND deleted_at IS NULL) OR (revision=$4+1 AND body->>'retirement_operation_id'=$5 AND deleted_at IS NOT NULL)))`, o.OrgID, a.ID, o.Input.ResourceID, a.Revision, o.ID).Scan(&valid); checkErr != nil {
			return checkErr
		}
		if !valid {
			return ErrConflict
		}
	}
	deps, err := retirementDependenciesTx(ctx, tx, o.OrgID, o.Input.ResourceID, attachment, o.ID)
	if err != nil {
		return err
	}
	if len(deps) > 0 {
		return ErrBusy
	}
	return nil
}

func (s *PgStore) ValidateClusterRetirementEffect(ctx context.Context, o *ClusterOperation) error {
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
	if saved.Revision != o.Revision || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || saved.CancelRequested || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now().Add(5*time.Second)) {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if checkErr := retirementPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *PgStore) DisableClusterAttachment(ctx context.Context, o *ClusterOperation) error {
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
	if saved.Revision != o.Revision || saved.FencingEpoch != o.FencingEpoch || saved.CancelRequested || saved.LeaseOwner != o.LeaseOwner || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now().Add(5*time.Second)) {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if o.Input.Retirement == nil || o.Input.Retirement.Attachment == nil {
		return ErrInvalid
	}
	if checkErr := retirementPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	a := *o.Input.Retirement.Attachment
	now := time.Now().UTC()
	a.DeletedAt = &now
	a.Revision++
	a.UpdatedAt = now
	a.RetirementOperationID = o.ID
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	// Remove target eligibility before retiring the runtime attachment. Its receipt remains.
	if _, deleteErr := tx.Exec(ctx, `DELETE FROM loomplane_cloud_targets WHERE org_id=$1 AND connection_id=$2 AND managed_resource_id=$3`, o.OrgID, a.RuntimeConnectionID, a.ClusterID); deleteErr != nil {
		return deleteErr
	}
	if _, updateErr := tx.Exec(ctx, `UPDATE loomplane_cloud_connections SET status='error',generation=generation+1,message='Runtime attachment detached',updated_at=now() WHERE org_id=$1 AND id=$2`, o.OrgID, a.RuntimeConnectionID); updateErr != nil {
		return updateErr
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_runtime_attachments SET deleted_at=$3,revision=$4,body=$5 WHERE org_id=$1 AND id=$2 AND revision=$4-1 AND deleted_at IS NULL`, a.OrgID, a.ID, now, a.Revision, raw)
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
	old := o.Revision
	o.Revision++
	o.UpdatedAt = now
	if checkErr := checkpointClusterTx(ctx, tx, o, old); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *PgStore) ClusterRetirementVaultKeys(ctx context.Context, o *ClusterOperation) ([]string, error) {
	a := o.Input.Retirement.Attachment
	rows, err := s.db.Query(ctx, `SELECT vault_key FROM loomplane_cluster_credential_attempts WHERE org_id=$1 AND attachment_id=$2
 UNION SELECT $3::text WHERE $3<>''
 UNION SELECT jsonb_array_elements_text(coalesce(body->'enrollment'->'retired_vault_keys','[]'::jsonb)) FROM loomplane_cluster_operations WHERE org_id=$1 AND id=$4
 UNION SELECT body->'enrollment'->>'vault_key' FROM loomplane_cluster_operations WHERE org_id=$1 AND id=$4 AND coalesce(body->'enrollment'->>'vault_key','')<>''`, o.OrgID, a.ID, a.VaultKey, a.OperationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if checkErr := rows.Scan(&key); checkErr != nil {
			return nil, checkErr
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
