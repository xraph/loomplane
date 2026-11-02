package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

var _ InstanceNamespaceStore = (*PgStore)(nil)

func (s *PgStore) AdvanceInstanceNamespaceAuthority(ctx context.Context, prior, next InstanceNamespaceRecord, verify func(context.Context, driver.Tx) error) error {
	identity := next
	identity.CurrentAuthority = prior.CurrentAuthority
	identity.CurrentGeneration = prior.CurrentGeneration
	identity.CurrentSnapshotDigest = prior.CurrentSnapshotDigest
	if verify == nil || !reflect.DeepEqual(identity, prior) || next.CurrentGeneration < prior.CurrentGeneration {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return err
	}
	if checkErr := verify(ctx, tx); checkErr != nil {
		return checkErr
	}
	if checkErr := lockInstanceNamespaceScope(ctx, tx, next); checkErr != nil {
		return checkErr
	}
	if checkErr := instanceNamespaceRecordTx(ctx, tx, prior); checkErr != nil {
		return checkErr
	}
	if checkErr := lockCapacityCloud(ctx, tx, next.OrgID, next.CloudID); checkErr != nil {
		return checkErr
	}
	o, err := clusterOperationTx(ctx, tx, next.OrgID, next.CloudID, next.OperationID)
	if err != nil {
		return err
	}
	if o.Kind != InstanceNamespaceKind || o.Status != "succeeded" || o.CancelRequested || o.Input.Attachment == nil || o.Input.Attachment.Instance == nil || *o.Input.Attachment.Instance != next.Pins {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(next.CurrentAuthority)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_namespaces SET generation=$3,snapshot_digest=$4,authority=$5 WHERE org_id=$1 AND operation_id=$2`, next.OrgID, next.OperationID, next.CurrentGeneration, next.CurrentSnapshotDigest, string(raw)); err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

func (s *PgStore) SaveInstanceNamespaceCheckpoint(ctx context.Context, o *ClusterOperation, expected int64, record InstanceNamespaceRecord, verify func(context.Context, driver.Tx) error) error {
	if o == nil || o.Kind != InstanceNamespaceKind || o.ID != record.OperationID || o.OrgID != record.OrgID || expected < 1 || o.Revision != expected+1 || verify == nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return err
	}
	if checkErr := verify(ctx, tx); checkErr != nil {
		return checkErr
	}
	if checkErr := lockInstanceNamespaceScope(ctx, tx, record); checkErr != nil {
		return checkErr
	}
	if checkErr := instanceNamespaceRecordTx(ctx, tx, record); checkErr != nil {
		return checkErr
	}
	if checkErr := runtimeAccessProtocolTx(ctx, tx); checkErr != nil {
		return checkErr
	}
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if checkErr := checkpointClusterTx(ctx, tx, o, expected); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *PgStore) GetInstanceNamespaceRecord(ctx context.Context, org, operation string) (InstanceNamespaceRecord, error) {
	var r InstanceNamespaceRecord
	var raw, authority string
	var generation int64
	var digest string
	if err := s.db.QueryRow(ctx, `SELECT body,authority,generation,snapshot_digest FROM loomplane_fleet_namespaces WHERE org_id=$1 AND operation_id=$2`, org, operation).Scan(&raw, &authority, &generation, &digest); err != nil {
		return r, storeError(err)
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(authority), &r.CurrentAuthority); err != nil {
		return r, err
	}
	r.CurrentGeneration = generation
	r.CurrentSnapshotDigest = digest
	return r, nil
}

const instanceNamespaceCurrentSQL = `SELECT EXISTS(
 SELECT 1 FROM loomplane_fleet_instances i
 JOIN loomplane_fleets f ON f.org_id=i.org_id AND f.id=i.fleet_id
 JOIN loomplane_fleet_authorities a ON a.org_id=i.org_id AND a.fleet_id=i.fleet_id
 JOIN loomplane_fleet_grants g ON g.org_id=a.org_id AND g.fleet_id=a.fleet_id AND g.id=a.grant_id
 JOIN loomplane_fleet_stages s ON s.org_id=i.org_id AND s.fleet_id=i.fleet_id AND s.id=i.stage_id
 WHERE i.org_id=$1 AND i.project_id=$2 AND i.fleet_id=$3 AND i.id=$4 AND i.environment_id=$5
 AND i.desired_generation=$6 AND i.ownership_epoch=$7 AND i.status NOT IN ('retired','deleted') AND f.status='active'
 AND a.id=$8 AND a.snapshot::jsonb=$9::jsonb AND a.released_at IS NULL
 AND (a.snapshot::jsonb->>'expires_at')::timestamptz>clock_timestamp()
 AND a.snapshot::jsonb->'request'->>'snapshot_digest'=$10
 AND i.desired_release_id=a.snapshot::jsonb->'request'->>'release_id'
 AND i.profile_id=a.snapshot::jsonb->'request'->>'profile_id'
 AND i.stage_id=a.snapshot::jsonb->'request'->>'stage_id'
 AND i.external_ref=a.snapshot::jsonb->'request'->>'external_ref'
 AND g.status='active' AND g.expires_at>clock_timestamp() AND g.revision=(a.snapshot::jsonb->>'grant_revision')::bigint
 AND s.policy_revision=(a.snapshot::jsonb->>'stage_revision')::bigint
 AND EXISTS(SELECT 1 FROM loomplane_fleet_stage_clouds sc WHERE sc.org_id=i.org_id AND sc.stage_id=i.stage_id AND sc.cloud_id=$11))`

func namespaceRecordArgs(r InstanceNamespaceRecord) ([]any, error) {
	a := r.CurrentAuthority
	if r.OrgID == "" || r.ProjectID == "" || r.Pins.FleetID == "" || r.Pins.InstanceID == "" || r.Pins.EnvironmentID == "" || r.CurrentGeneration < 1 || r.Pins.OwnershipEpoch < 1 || a.Scope.OrgID != r.OrgID || a.Scope.ProjectID != r.ProjectID || a.Scope.FleetID != r.Pins.FleetID || a.Request.CloudID != r.CloudID || a.Request.SnapshotDigest != r.CurrentSnapshotDigest {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return []any{r.OrgID, r.ProjectID, r.Pins.FleetID, r.Pins.InstanceID, r.Pins.EnvironmentID, r.CurrentGeneration, r.Pins.OwnershipEpoch, a.ID, string(raw), r.CurrentSnapshotDigest, r.CloudID}, nil
}

func (s *PgStore) CheckInstanceNamespaceRecord(ctx context.Context, r InstanceNamespaceRecord) error {
	current, err := s.GetInstanceNamespaceRecord(ctx, r.OrgID, r.OperationID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, r) {
		return ErrConflict
	}
	args, err := namespaceRecordArgs(r)
	if err != nil {
		return err
	}
	var valid bool
	if checkErr := s.db.QueryRow(ctx, instanceNamespaceCurrentSQL, args...).Scan(&valid); checkErr != nil {
		return checkErr
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

// lockInstanceNamespaceScope follows the global source gate and current-authority
// gate. It must run before the Cloud lock in every namespace publication.
func lockInstanceNamespaceScope(ctx context.Context, tx driver.Tx, r InstanceNamespaceRecord) error {
	var environment string
	if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, r.OrgID, r.ProjectID, r.Pins.EnvironmentID).Scan(&environment); err != nil {
		return storeError(err)
	}
	args, err := namespaceRecordArgs(r)
	if err != nil {
		return err
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, instanceNamespaceCurrentSQL, args...).Scan(&valid); checkErr != nil {
		return checkErr
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

func instanceNamespaceRecordTx(ctx context.Context, tx driver.Tx, r InstanceNamespaceRecord) error {
	var raw, authority, digest string
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT body,authority,generation,snapshot_digest FROM loomplane_fleet_namespaces WHERE org_id=$1 AND operation_id=$2 FOR UPDATE`, r.OrgID, r.OperationID).Scan(&raw, &authority, &generation, &digest); err != nil {
		return storeError(err)
	}
	var saved InstanceNamespaceRecord
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(authority), &saved.CurrentAuthority); err != nil {
		return err
	}
	saved.CurrentGeneration = generation
	saved.CurrentSnapshotDigest = digest
	if !reflect.DeepEqual(saved, r) {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) ReserveInstanceNamespaceOperation(ctx context.Context, r InstanceNamespaceRecord, o *ClusterOperation, verify func(context.Context, driver.Tx) error) (*ClusterOperation, error) {
	if verify == nil || o == nil || o.Kind != InstanceNamespaceKind || o.Input.Kind != InstanceNamespaceKind || o.Status != "accepted" || o.Revision != 1 || o.SchemaVersion != 1 || o.ID != r.OperationID || o.OrgID != r.OrgID || o.CloudID != r.CloudID || o.ResourceID != r.ClusterID || o.Input.Attachment == nil || o.Input.Attachment.Instance == nil || *o.Input.Attachment.Instance != r.Pins || o.Input.Attachment.Namespace != r.Namespace || o.Input.Attachment.ProjectID != r.ProjectID || o.Digest != clusterDigest(o.Input) || !reflect.DeepEqual(r.CreationAuthority, r.CurrentAuthority) || !reflect.DeepEqual(o.Principal, r.CreationAuthority.Principal) {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return nil, err
	}
	if checkErr := verify(ctx, tx); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := lockInstanceNamespaceScope(ctx, tx, r); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := runtimeAccessProtocolTx(ctx, tx); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	saved, err := clusterOperationTx(ctx, tx, o.OrgID, o.CloudID, o.ID)
	if err == nil {
		if saved.Digest != o.Digest || saved.Kind != InstanceNamespaceKind {
			return nil, ErrConflict
		}
		if checkErr := instanceNamespaceRecordTx(ctx, tx, r); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := tx.Commit(); checkErr != nil {
			return nil, checkErr
		}
		return saved, nil
	}
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 AND cloud_id=$3 AND revision=$4 AND deleted_at IS NULL AND status='running')`, o.OrgID, o.ResourceID, o.CloudID, o.Input.ResourceRevision).Scan(&valid); checkErr != nil {
		return nil, checkErr
	}
	if !valid {
		return nil, ErrConflict
	}
	var busy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_operations WHERE org_id=$1 AND resource_id=$2 AND status IN ('accepted','running','waiting_provider','needs_attention') AND body->>'kind'<>'instance-namespace')`, o.OrgID, o.ResourceID).Scan(&busy); checkErr != nil {
		return nil, checkErr
	}
	if busy {
		return nil, ErrBusy
	}
	body, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	principal, err := json.Marshal(o.Principal)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_operations(id,org_id,cloud_id,resource_id,status,revision,digest,fencing_epoch,body,principal) VALUES($1,$2,$3,$4,$5,$6,$7,0,$8,$9)`, o.ID, o.OrgID, o.CloudID, o.ResourceID, o.Status, o.Revision, o.Digest, body, principal); err != nil {
		return nil, storeError(err)
	}
	if checkErr := saveClusterSteps(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	record, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	authority, err := json.Marshal(r.CurrentAuthority)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_namespaces(operation_id,org_id,project_id,fleet_id,instance_id,environment_id,cloud_id,cluster_id,namespace,ownership_epoch,generation,snapshot_digest,authority,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, r.OperationID, r.OrgID, r.ProjectID, r.Pins.FleetID, r.Pins.InstanceID, r.Pins.EnvironmentID, r.CloudID, r.ClusterID, r.Namespace, r.Pins.OwnershipEpoch, r.CurrentGeneration, r.CurrentSnapshotDigest, string(authority), string(record)); err != nil {
		return nil, storeError(err)
	}
	if checkErr := appendClusterEvent(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return o, nil
}
