package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
)

func writeNamespaceAction(ctx context.Context, tx driver.Tx, schema string, in namespaceAction, digest string, at time.Time) (*namespaceResult, error) {
	table := func(name string) string { return namespaceTable(schema, name) }
	floor, nanos, err := splitNamespaceAuthorizationInstant(at)
	if err != nil {
		return nil, err
	}
	stamp := floor.Format("2006-01-02T15:04:05.000000Z")
	r, o := in.Record, in.Operation
	r.Revision = in.ExpectedNamespaceRevision + 1
	r.CurrentAttemptID = in.AttemptID
	if in.Kind == "reserve" {
		r.CreationAttemptID = in.AttemptID
	}
	if in.Kind != "renew" {
		o.UpdatedAt = at.UTC()
		o.NamespaceAttemptID = in.AttemptID
		o.NamespaceInputDigest = digest
	}
	if in.Kind == "publish" {
		if publicationErr := writeNamespacePublication(ctx, tx, schema, in); publicationErr != nil {
			return nil, publicationErr
		}
	}
	if in.Kind != "renew" {
		if operationWriteErr := writeNamespaceOperation(ctx, tx, schema, in.Kind, &o, in.ExpectedOperationRevision); operationWriteErr != nil {
			return nil, operationWriteErr
		}
	}
	result := &namespaceResult{Record: r, Operation: o, Principal: o.Principal, Snapshot: in.Snapshot}
	inputJSON, err := json.Marshal(namespaceEvidence[namespaceAction]{AuthorizedAt: stamp, AuthorizedAtNS: nanos, Value: in})
	if err != nil {
		return nil, err
	}
	resultJSON, err := json.Marshal(namespaceEvidence[namespaceResult]{AuthorizedAt: stamp, AuthorizedAtNS: nanos, Value: *result})
	if err != nil {
		return nil, err
	}
	if len(inputJSON) > 2*1024*1024 || len(resultJSON) > 2*1024*1024 {
		return nil, ErrInvalid
	}
	if journalWriteErr := namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_fleet_namespace_attempts")+`(id,org_id,operation_id,project_id,fleet_id,instance_id,environment_id,key_id,kind,input_digest,expected_namespace_revision,result_namespace_revision,expected_operation_revision,result_operation_revision,generation,ownership_epoch,authority_id,authorized_at,authorized_at_ns,input,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, in.AttemptID, r.OrgID, r.OperationID, r.ProjectID, r.Pins.FleetID, r.Pins.InstanceID, r.Pins.EnvironmentID, in.Principal.KeyID, in.Kind, digest, in.ExpectedNamespaceRevision, r.Revision, in.ExpectedOperationRevision, o.Revision, r.CurrentGeneration, r.Pins.OwnershipEpoch, r.CurrentAuthority.ID, floor, nanos, inputJSON, resultJSON); journalWriteErr != nil {
		return nil, journalWriteErr
	}
	authority, err := json.Marshal(r.CurrentAuthority)
	if err != nil {
		return nil, err
	}
	if in.Kind == "reserve" {
		bodyRecord := r
		// The complete reference-only snapshot lives in the bounded attempt journal.
		bodyRecord.Snapshot = fleets.InstanceSnapshot{}
		body, bodyErr := json.Marshal(bodyRecord)
		if bodyErr != nil {
			return nil, bodyErr
		}
		err = namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_fleet_namespaces")+`(operation_id,org_id,project_id,fleet_id,instance_id,environment_id,cloud_id,cluster_id,namespace,ownership_epoch,generation,snapshot_digest,authority,body,revision,creation_attempt_id,current_attempt_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`, r.OperationID, r.OrgID, r.ProjectID, r.Pins.FleetID, r.Pins.InstanceID, r.Pins.EnvironmentID, r.CloudID, r.ClusterID, r.Namespace, r.Pins.OwnershipEpoch, r.CurrentGeneration, r.CurrentSnapshotDigest, string(authority), string(body), r.Revision, in.AttemptID)
	} else {
		err = namespaceExecOne(ctx, tx, `UPDATE `+table("loomplane_fleet_namespaces")+` SET generation=$3,snapshot_digest=$4,authority=$5,revision=$6,current_attempt_id=$7 WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND revision OPERATOR(pg_catalog.=) $8`, r.OrgID, r.OperationID, r.CurrentGeneration, r.CurrentSnapshotDigest, string(authority), r.Revision, in.AttemptID, in.ExpectedNamespaceRevision)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func namespaceExecOne(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (driver.Result, error)
}, sql string, args ...any) error {
	tag, err := tx.Exec(ctx, sql, args...)
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
	return nil
}

func writeNamespaceOperation(ctx context.Context, tx driver.Tx, schema, kind string, o *ClusterOperation, expected int64) error {
	table := func(name string) string { return namespaceTable(schema, name) }
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	principal, err := json.Marshal(o.Principal)
	if err != nil {
		return err
	}
	if kind == "reserve" {
		err = namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_cluster_operations")+`(id,org_id,cloud_id,resource_id,status,revision,digest,fencing_epoch,body,principal,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, o.ID, o.OrgID, o.CloudID, o.ResourceID, o.Status, o.Revision, o.Digest, o.FencingEpoch, raw, principal, o.UpdatedAt)
	} else {
		// All mutable eligibility and lease facts were locked before evaluation.
		err = namespaceExecOne(ctx, tx, `UPDATE `+table("loomplane_cluster_operations")+` SET status=$4,revision=$5,body=$6,updated_at=$7 WHERE org_id OPERATOR(pg_catalog.=) $1 AND cloud_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3 AND revision OPERATOR(pg_catalog.=) $8 AND digest OPERATOR(pg_catalog.=) $9 AND fencing_epoch OPERATOR(pg_catalog.=) $10 AND principal OPERATOR(pg_catalog.=) $11::pg_catalog.jsonb`, o.OrgID, o.CloudID, o.ID, o.Status, o.Revision, raw, o.UpdatedAt, expected, o.Digest, o.FencingEpoch, principal)
	}
	if err != nil {
		return err
	}
	if stepsWriteErr := writeNamespaceSteps(ctx, tx, schema, o); stepsWriteErr != nil {
		return stepsWriteErr
	}
	change, err := json.Marshal(clusterChange(o))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT `+table("loomplane_append_event")+`($1,'','cloud_cluster.updated',$2,$3)`, o.OrgID, o.ID, change)
	return err
}

func writeNamespaceSteps(ctx context.Context, tx driver.Tx, schema string, o *ClusterOperation) error {
	table := namespaceTable(schema, "loomplane_cluster_operation_steps")
	seen := make(map[string]bool, len(o.Steps))
	if len(o.Steps) > 16 {
		return ErrInvalid
	}
	for _, step := range o.Steps {
		if step.Name == "" || seen[step.Name] || step.RequestDigest != o.Digest {
			return ErrInvalid
		}
		seen[step.Name] = true
		raw, err := json.Marshal(step)
		if err != nil {
			return err
		}
		if stepWriteErr := namespaceExecOne(ctx, tx, `INSERT INTO `+table+` AS saved(org_id,operation_id,name,state,request_digest,body) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(org_id,operation_id,name) DO UPDATE SET state=excluded.state,body=excluded.body  OPERATOR(pg_catalog.||)  CASE WHEN saved.body OPERATOR(pg_catalog.?) 'namespace_receipts' THEN pg_catalog.jsonb_build_object('namespace_receipts',(saved.body OPERATOR(pg_catalog.->) 'namespace_receipts')) ELSE '{}'::pg_catalog.jsonb END WHERE saved.request_digest OPERATOR(pg_catalog.=) excluded.request_digest AND pg_catalog.array_position(ARRAY['pending','attempted','observed','completed'],saved.state) OPERATOR(pg_catalog.<=) pg_catalog.array_position(ARRAY['pending','attempted','observed','completed'],excluded.state) AND ((saved.body OPERATOR(pg_catalog.->) 'attempted_at') IS NULL OR (saved.body OPERATOR(pg_catalog.->) 'attempted_at') OPERATOR(pg_catalog.=) (excluded.body OPERATOR(pg_catalog.->) 'attempted_at')) AND (coalesce((saved.body OPERATOR(pg_catalog.->>) 'remote_operation_id'),'') OPERATOR(pg_catalog.=) '' OR (saved.body OPERATOR(pg_catalog.->) 'remote_operation_id') OPERATOR(pg_catalog.=) (excluded.body OPERATOR(pg_catalog.->) 'remote_operation_id')) AND (coalesce((saved.body OPERATOR(pg_catalog.->>) 'remote_resource_id'),'') OPERATOR(pg_catalog.=) '' OR (saved.body OPERATOR(pg_catalog.->) 'remote_resource_id') OPERATOR(pg_catalog.=) (excluded.body OPERATOR(pg_catalog.->) 'remote_resource_id'))`, o.OrgID, o.ID, step.Name, step.State, step.RequestDigest, raw); stepWriteErr != nil {
			return stepWriteErr
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM `+table+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2`, o.OrgID, o.ID).Scan(&count); err != nil {
		return err
	}
	if count != len(o.Steps) {
		return ErrConflict
	}
	return nil
}

func writeNamespacePublication(ctx context.Context, tx driver.Tx, schema string, in namespaceAction) error {
	table := func(name string) string { return namespaceTable(schema, name) }
	o, a, c := in.Operation, in.Attachment, in.Connection
	details, err := json.Marshal(c.Details)
	if err != nil {
		return err
	}
	if connectionWriteErr := namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_cloud_connections")+`(id,org_id,cloud,name,account,details,status,missing,message,checked_at,created_by,created_at,updated_at,generation,method,signed_in_by) VALUES($1,$2,$3,$4,$5,$6,$7,'[]','',$8,$9,$8,$8,$10,$11,'')`, c.ID, c.OrgID, c.Cloud, c.Name, c.Account, details, c.Status, c.CreatedAt, c.CreatedBy, c.Generation, c.Method); connectionWriteErr != nil {
		return connectionWriteErr
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if attachmentWriteErr := namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_runtime_attachments")+`(id,org_id,cloud_id,cluster_id,project_id,namespace,namespace_uid,service_account_uid,runtime_connection_id,endpoint_fingerprint,trust_fingerprint,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, a.ID, a.OrgID, a.CloudID, a.ClusterID, a.ProjectID, a.Namespace, a.NamespaceUID, a.ServiceAccountUID, a.RuntimeConnectionID, a.EndpointFingerprint, a.TrustFingerprint, a.Revision, raw); attachmentWriteErr != nil {
		return attachmentWriteErr
	}
	return namespaceExecOne(ctx, tx, `INSERT INTO `+table("loomplane_cloud_targets")+`(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,tags,managed_resource_id,location_id) VALUES($1,$2,$3,$4,$5,$5,'kubernetes',$6,'',1,$7,$7,'managed','{}',$8,NULL)`, o.Enrollment.TargetID, a.OrgID, a.ProjectID, a.CloudID, o.Input.Attachment.TargetSlug, c.ID, c.CreatedAt, a.ClusterID)
}

func (s *PgStore) reconcileNamespaceAttempt(ctx context.Context, schema string, in namespaceAction, digest string) (*namespaceResult, error) {
	call, cancel := context.WithTimeout(ctx, currentauthority.AttemptLimit)
	defer cancel()
	if schema == "" {
		return nil, currentauthority.ErrCommitUnknown
	}
	var input, result []byte
	var at time.Time
	var ns int16
	var primary bool
	err := s.db.QueryRow(call, `SELECT input,result,authorized_at,authorized_at_ns,NOT pg_catalog.pg_is_in_recovery() FROM `+namespaceTable(schema, "loomplane_fleet_namespace_attempts")+` WHERE id OPERATOR(pg_catalog.=) $1 AND org_id OPERATOR(pg_catalog.=) $2 AND operation_id OPERATOR(pg_catalog.=) $3 AND project_id OPERATOR(pg_catalog.=) $4 AND fleet_id OPERATOR(pg_catalog.=) $5 AND instance_id OPERATOR(pg_catalog.=) $6 AND environment_id OPERATOR(pg_catalog.=) $7 AND key_id OPERATOR(pg_catalog.=) $8 AND kind OPERATOR(pg_catalog.=) $9 AND input_digest OPERATOR(pg_catalog.=) $10 AND expected_namespace_revision OPERATOR(pg_catalog.=) $11 AND result_namespace_revision OPERATOR(pg_catalog.=) $12 AND expected_operation_revision OPERATOR(pg_catalog.=) $13 AND result_operation_revision OPERATOR(pg_catalog.=) $14 AND generation OPERATOR(pg_catalog.=) $15 AND ownership_epoch OPERATOR(pg_catalog.=) $16 AND authority_id OPERATOR(pg_catalog.=) $17`, in.AttemptID, in.Record.OrgID, in.Operation.ID, in.Record.ProjectID, in.Record.Pins.FleetID, in.Record.Pins.InstanceID, in.Record.Pins.EnvironmentID, in.Principal.KeyID, in.Kind, digest, in.ExpectedNamespaceRevision, in.ExpectedNamespaceRevision+1, in.ExpectedOperationRevision, in.Operation.Revision, in.Record.CurrentGeneration, in.Record.Pins.OwnershipEpoch, in.Record.CurrentAuthority.ID).Scan(&input, &result, &at, &ns, &primary)
	if err != nil || !primary {
		return nil, errors.Join(currentauthority.ErrCommitUnknown, err)
	}
	return decodeNamespaceAttempt(in, digest, input, result, at, ns)
}

func decodeNamespaceAttempt(in namespaceAction, digest string, input, result []byte, at time.Time, ns int16) (*namespaceResult, error) {
	in.Operation.Principal = in.Record.CreationAuthority.Principal
	exact, err := joinNamespaceAuthorizationInstant(at, ns)
	if err != nil {
		return nil, err
	}
	var saved namespaceEvidence[namespaceAction]
	var out namespaceEvidence[namespaceResult]
	if inputDecodeErr := json.Unmarshal(input, &saved); inputDecodeErr != nil {
		return nil, inputDecodeErr
	}
	if resultDecodeErr := json.Unmarshal(result, &out); resultDecodeErr != nil {
		return nil, resultDecodeErr
	}
	stamp := at.UTC().Format("2006-01-02T15:04:05.000000Z")
	if saved.AuthorizedAt != stamp || out.AuthorizedAt != stamp || saved.AuthorizedAtNS != ns || out.AuthorizedAtNS != ns || !namespaceEqual(saved.Value, in) {
		return nil, ErrConflict
	}
	_, hash, err := freezeNamespaceAction(saved.Value)
	if err != nil || hash != digest {
		return nil, errors.Join(ErrConflict, err)
	}
	expected := in.Record
	expected.Revision = in.ExpectedNamespaceRevision + 1
	expected.CurrentAttemptID = in.AttemptID
	if in.Kind == "reserve" {
		expected.CreationAttemptID = in.AttemptID
	}
	op := in.Operation
	if in.Kind != "renew" {
		op.UpdatedAt = exact
		op.NamespaceAttemptID = in.AttemptID
		op.NamespaceInputDigest = digest
	}
	if !namespaceEqual(out.Value.Record, expected) || !namespaceEqual(out.Value.Operation, op) || !namespaceEqual(out.Value.Principal, op.Principal) || !namespaceEqual(out.Value.Snapshot, in.Snapshot) {
		return nil, ErrConflict
	}
	out.Value.Operation.Principal = out.Value.Principal
	return &out.Value, nil
}
