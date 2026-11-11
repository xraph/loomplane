package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

func namespaceTable(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }

// This gate follows the source and identity fences. It preserves the coordinator's
// two-second lock timeout instead of the ordinary Cloud writer's longer bound.
func lockNamespaceCloud(ctx context.Context, tx driver.Tx, schema string, in namespaceAction) error {
	if schema == "" {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))`, "cloud-organization/"+in.Record.OrgID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT `+namespaceTable(schema, "loomplane_cluster_controls_current")+`($1,'')`, in.Record.OrgID); err != nil {
		return err
	}
	var revision int64
	var platform bool
	if err := tx.QueryRow(ctx, `SELECT revision,platform_owned FROM `+namespaceTable(schema, "loomplane_cloud_profiles")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $2 FOR UPDATE`, in.Record.OrgID, in.Record.CloudID).Scan(&revision, &platform); err != nil {
		return storeError(err)
	}
	if platform || revision != in.Operation.Input.CloudRevision {
		return ErrConflict
	}
	_, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('loomplane.runtime_access_protocol','runtime-access-v1',true)`)
	return err
}

type lockedNamespaceAction struct{ operation *ClusterOperation }

func (p *lockedNamespaceAction) validateAt(in namespaceAction, at time.Time) error {
	if p == nil || at.IsZero() || !in.Record.QualificationExpiresAt.After(at) {
		return ErrConflict
	}
	if in.Kind == "publish" && (in.Attachment == nil || !in.Attachment.CredentialExpiry.After(at)) {
		return ErrConflict
	}
	if in.Kind == "reserve" {
		if !in.Operation.ExpiresAt.After(at) {
			return ErrConflict
		}
		return nil
	}
	if p.operation == nil || p.operation.CancelRequested {
		return ErrConflict
	}
	if in.Kind != "renew" && !p.operation.ExpiresAt.After(at) {
		return ErrConflict
	}
	if in.Kind != "renew" && p.operation.FencingEpoch != 0 && (p.operation.LeaseExpiresAt == nil || !p.operation.LeaseExpiresAt.After(at)) {
		return ErrConflict
	}
	return nil
}

func loadNamespaceAction(ctx context.Context, tx driver.Tx, schema string, in namespaceAction) (*lockedNamespaceAction, error) {
	table := func(name string) string { return namespaceTable(schema, name) }
	o, r := &in.Operation, in.Record
	// The organization gate serializes writers of set membership and absent rows.
	resource, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM `+table("loomplane_managed_resources")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $2 FOR SHARE`, r.OrgID, r.ClusterID))
	if err != nil {
		return nil, err
	}
	if resource.CloudID != r.CloudID || resource.DeletedAt != nil || resource.Status != "running" || resource.Kind != "cluster" || resource.Revision != o.Input.ResourceRevision || resource.ConnectionID != o.Input.ConnectionID || resource.Provider != o.Input.Provider || resource.AccountID != o.Input.AccountID || resource.Observation == nil || resource.Observation.CAHash != o.Input.Attachment.TrustFingerprint || endpointFingerprint(resource.Observation.Endpoint) != o.Input.Attachment.EndpointFingerprint || !namespaceEqual(resource.Observation.Ref, o.Input.Ref) {
		return nil, ErrConflict
	}
	var cloud, status string
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT cloud,generation,status FROM `+table("loomplane_cloud_connections")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $2 FOR SHARE`, r.OrgID, o.Input.ConnectionID).Scan(&cloud, &generation, &status); err != nil {
		return nil, storeError(err)
	}
	if cloud != o.Input.Provider || generation != o.Input.ConnectionGeneration || status != "ready" {
		return nil, ErrConflict
	}
	var revision int64
	var revoked bool
	var targets []string
	if err = tx.QueryRow(ctx, `SELECT revision,revoked,target_ids FROM `+table("loomplane_cloud_project_grants")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND cloud_id OPERATOR(pg_catalog.=) $2 AND project_id OPERATOR(pg_catalog.=) $3 FOR SHARE`, r.OrgID, r.CloudID, r.ProjectID).Scan(&revision, &revoked, &targets); err != nil {
		return nil, storeError(err)
	}
	if revoked || revision != o.Input.Attachment.GrantRevision || len(targets) != 0 {
		return nil, ErrConflict
	}
	var busy bool
	if busyQueryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table("loomplane_cluster_operations")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND (resource_id OPERATOR(pg_catalog.=) $2 OR ((body OPERATOR(pg_catalog.->) 'input') OPERATOR(pg_catalog.->>) 'resource_id') OPERATOR(pg_catalog.=) $2) AND status IN ('accepted','running','waiting_provider','needs_attention') AND (body OPERATOR(pg_catalog.->>) 'kind') OPERATOR(pg_catalog.<>) 'instance-namespace')`, r.OrgID, r.ClusterID).Scan(&busy); busyQueryErr != nil {
		return nil, busyQueryErr
	}
	if busy {
		return nil, ErrBusy
	}
	saved, err := readNamespaceOperation(tx.QueryRow(ctx, `SELECT body,principal,revision,digest,fencing_epoch,status FROM `+table("loomplane_cluster_operations")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND cloud_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3 FOR UPDATE`, r.OrgID, r.CloudID, r.OperationID))
	if in.Kind == "reserve" {
		if !errors.Is(err, ErrNotFound) {
			if err == nil {
				return nil, ErrConflict
			}
			return nil, err
		}
		var exists bool
		if namespaceExistsErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table("loomplane_fleet_namespaces")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND (operation_id OPERATOR(pg_catalog.=) $2 OR (instance_id OPERATOR(pg_catalog.=) $3 AND ownership_epoch OPERATOR(pg_catalog.=) $4) OR (cluster_id OPERATOR(pg_catalog.=) $5 AND namespace OPERATOR(pg_catalog.=) $6)))`, r.OrgID, r.OperationID, r.Pins.InstanceID, r.Pins.OwnershipEpoch, r.ClusterID, r.Namespace).Scan(&exists); namespaceExistsErr != nil {
			return nil, namespaceExistsErr
		}
		if exists {
			return nil, ErrConflict
		}
	} else {
		if err != nil {
			return nil, err
		}
		if saved.NamespaceAttemptID != o.NamespaceAttemptID || saved.NamespaceInputDigest != o.NamespaceInputDigest || saved.Revision != in.ExpectedOperationRevision || saved.Digest != o.Digest || saved.Kind != InstanceNamespaceKind || saved.CancelRequested || saved.SchemaVersion != o.SchemaVersion || saved.ResourceID != o.ResourceID || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || !namespaceEqual(saved.LeaseExpiresAt, o.LeaseExpiresAt) || !namespaceEqual(saved.Input, o.Input) || !namespaceEqual(saved.Principal, r.CreationAuthority.Principal) || !namespaceEqual(saved.MoneyEstimate, o.MoneyEstimate) || !namespaceEqual(saved.CapacityTarget, o.CapacityTarget) || !saved.ExpiresAt.Equal(o.ExpiresAt) || !saved.CreatedAt.Equal(o.CreatedAt) {
			return nil, ErrConflict
		}
		if in.Kind == "renew" && (saved.Status != "succeeded" || !namespaceEqual(saved, o)) {
			return nil, ErrConflict
		}
		prior := r
		if in.Prior != nil {
			prior = *in.Prior
		}
		current, recordErr := readNamespaceRecord(tx.QueryRow(ctx, namespaceRecordSelect(schema)+` WHERE n.org_id OPERATOR(pg_catalog.=) $1 AND n.operation_id OPERATOR(pg_catalog.=) $2 FOR UPDATE OF n`, r.OrgID, r.OperationID))
		if recordErr != nil {
			return nil, recordErr
		}
		if current.Revision != in.ExpectedNamespaceRevision || !namespaceEqual(current, prior) || !namespaceEqual(current.Snapshot, prior.Snapshot) {
			return nil, ErrConflict
		}
	}
	if in.Kind != "reserve" {
		if stepLockErr := lockNamespaceSteps(ctx, tx, schema, o); stepLockErr != nil {
			return nil, stepLockErr
		}
	}
	if in.Kind == "publish" {
		if publicationErr := validateNamespacePublication(in, resource); publicationErr != nil {
			return nil, publicationErr
		}
		var exists bool
		if publicationExistsErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table("loomplane_runtime_attachments")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND (id OPERATOR(pg_catalog.=) $2 OR runtime_connection_id OPERATOR(pg_catalog.=) $3 OR (cluster_id OPERATOR(pg_catalog.=) $4 AND namespace OPERATOR(pg_catalog.=) $5)) UNION ALL SELECT 1 FROM `+table("loomplane_cloud_connections")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $3 UNION ALL SELECT 1 FROM `+table("loomplane_cloud_targets")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND (id OPERATOR(pg_catalog.=) $6 OR (cloud_id OPERATOR(pg_catalog.=) $7 AND project_id OPERATOR(pg_catalog.=) $8 AND name OPERATOR(pg_catalog.=) $9)))`, r.OrgID, in.Attachment.ID, in.Connection.ID, r.ClusterID, r.Namespace, o.Enrollment.TargetID, r.CloudID, r.ProjectID, o.Input.Attachment.TargetSlug).Scan(&exists); publicationExistsErr != nil {
			return nil, publicationExistsErr
		}
		if exists {
			return nil, ErrConflict
		}
	}
	return &lockedNamespaceAction{operation: saved}, nil
}

func namespaceRecordSelect(schema string) string {
	return `SELECT n.body,n.authority,n.generation,n.snapshot_digest,n.revision,n.creation_attempt_id,n.current_attempt_id,j.result FROM ` + namespaceTable(schema, "loomplane_fleet_namespaces") + ` n LEFT JOIN ` + namespaceTable(schema, "loomplane_fleet_namespace_attempts") + ` j ON j.org_id OPERATOR(pg_catalog.=) n.org_id AND j.operation_id OPERATOR(pg_catalog.=) n.operation_id AND j.id OPERATOR(pg_catalog.=) n.current_attempt_id`
}

func readNamespaceRecord(row driver.Row) (InstanceNamespaceRecord, error) {
	var r InstanceNamespaceRecord
	var body, authority string
	var generation, revision int64
	var digest string
	var creation, current *string
	var result []byte
	if err := row.Scan(&body, &authority, &generation, &digest, &revision, &creation, &current, &result); err != nil {
		return r, storeError(err)
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(authority), &r.CurrentAuthority); err != nil {
		return r, err
	}
	r.CurrentGeneration, r.CurrentSnapshotDigest, r.Revision = generation, digest, revision
	if creation != nil {
		r.CreationAttemptID = *creation
	}
	if current != nil {
		r.CurrentAttemptID = *current
		var evidence namespaceEvidence[namespaceResult]
		if err := json.Unmarshal(result, &evidence); err != nil {
			return r, err
		}
		r.Snapshot = evidence.Value.Snapshot
		if !namespaceEqual(r, evidence.Value.Record) {
			return r, ErrConflict
		}
	}
	return r, nil
}

func validateNamespacePublication(in namespaceAction, r *ManagedResource) error {
	o, a, c, record := in.Operation, in.Attachment, in.Connection, in.Record
	if a == nil || c == nil || o.Enrollment == nil || r == nil {
		return ErrInvalid
	}
	e := o.Enrollment
	if a.ID == "" || a.ID != e.AttachmentID || a.OrgID != record.OrgID || a.CloudID != record.CloudID || a.ClusterID != record.ClusterID || a.ProjectID != record.ProjectID || a.OperationID != o.ID || a.Revision != 1 || a.Namespace != record.Namespace || a.NamespaceUID == "" || a.NamespaceUID != e.State.NamespaceUID || a.ServiceAccountUID == "" || a.ServiceAccountUID != e.State.ServiceAccountUID || a.RuntimeConnectionID != c.ID || c.ID != e.RuntimeConnectionID || c.OrgID != record.OrgID || c.Cloud != "kubernetes" || c.Generation != 1 || c.Status != "ready" || e.TargetID == "" || e.ResourceRevision != r.Revision || a.TrustFingerprint != o.Input.Attachment.TrustFingerprint || a.EndpointFingerprint != o.Input.Attachment.EndpointFingerprint || c.Details["managed_instance_id"] != record.Pins.InstanceID || c.Details["managed_environment_id"] != record.Pins.EnvironmentID {
		return ErrConflict
	}
	return nil
}

func lockNamespaceSteps(ctx context.Context, tx driver.Tx, schema string, o *ClusterOperation) (outErr error) {
	rows, err := tx.Query(ctx, `SELECT name,request_digest,body FROM `+namespaceTable(schema, "loomplane_cluster_operation_steps")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 ORDER BY name FOR UPDATE`, o.OrgID, o.ID)
	if err != nil {
		return err
	}
	defer func() { outErr = errors.Join(outErr, rows.Close()) }()
	count := 0
	for rows.Next() {
		var name, digest string
		var body []byte
		if stepScanErr := rows.Scan(&name, &digest, &body); stepScanErr != nil {
			return stepScanErr
		}
		if len(body) > 256*1024 || name == "" || digest != o.Digest {
			return ErrConflict
		}
		count++
		if count > 16 {
			return ErrInvalid
		}
	}
	if stepsReadErr := rows.Err(); stepsReadErr != nil {
		return stepsReadErr
	}
	if count != len(o.Steps) {
		return ErrConflict
	}
	return nil
}

func readNamespaceOperation(row driver.Row) (*ClusterOperation, error) {
	var body, principal []byte
	var revision, fence int64
	var digest, status string
	if err := row.Scan(&body, &principal, &revision, &digest, &fence, &status); err != nil {
		return nil, storeError(err)
	}
	var operation ClusterOperation
	if err := json.Unmarshal(body, &operation); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(principal, &operation.Principal); err != nil {
		return nil, err
	}
	if operation.Revision != revision || operation.FencingEpoch != fence || operation.Digest != digest || operation.Status != status {
		return nil, ErrConflict
	}
	return &operation, nil
}
