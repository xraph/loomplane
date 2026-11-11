package cloudinfra

import (
	"context"
	"encoding/json"
)

var _ InstanceNamespaceStore = (*PgStore)(nil)

func (s *PgStore) GetInstanceNamespaceRecord(ctx context.Context, org, operation string) (InstanceNamespaceRecord, error) {
	// Startup qualification fixes the main connection schema before this reader is wired.
	var schema string
	if err := s.db.QueryRow(ctx, `SELECT pg_catalog.current_schema()`).Scan(&schema); err != nil {
		return InstanceNamespaceRecord{}, err
	}
	return readNamespaceRecord(s.db.QueryRow(ctx, namespaceRecordSelect(schema)+` WHERE n.org_id=$1 AND n.operation_id=$2`, org, operation))
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
	if !namespaceEqual(current, r) {
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
