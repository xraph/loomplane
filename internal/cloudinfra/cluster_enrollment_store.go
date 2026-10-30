package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

func (s *PgStore) FindClusterEnrollment(ctx context.Context, org, cloud, resource string) (*ClusterOperation, error) {
	return scanClusterOperation(s.db.QueryRow(ctx, `SELECT body,principal FROM loomplane_cluster_operations WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 AND body->'input'->'attachment' IS NOT NULL AND status<>'preview' ORDER BY updated_at DESC LIMIT 1`, org, cloud, resource))
}
func (s *PgStore) ReserveClusterEnrollment(ctx context.Context, o *ClusterOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	in := o.Input.Attachment
	old := o.Revision
	o.Revision++
	o.UpdatedAt = time.Now().UTC()
	if checkErr := checkpointClusterTx(ctx, tx, o, old); checkErr != nil {
		return checkErr
	}
	tag, err := tx.Exec(ctx, `INSERT INTO loomplane_cluster_enrollment_claims(org_id,cluster_id,namespace,operation_id,project_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,cluster_id,namespace) DO UPDATE SET operation_id=excluded.operation_id WHERE loomplane_cluster_enrollment_claims.operation_id=excluded.operation_id AND loomplane_cluster_enrollment_claims.project_id=excluded.project_id`, o.OrgID, o.ResourceID, in.Namespace, o.ID, in.ProjectID)
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
	return tx.Commit()
}
func (s *PgStore) PublishClusterEnrollment(ctx context.Context, o *ClusterOperation, a *RuntimeAttachment, c *connections.Connection) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if protocolErr := runtimeAccessProtocolTx(ctx, tx); protocolErr != nil {
		return protocolErr
	}
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_resources r JOIN loomplane_cloud_profiles p ON p.org_id=r.org_id AND p.id=r.cloud_id JOIN loomplane_cloud_project_grants g ON g.org_id=p.org_id AND g.cloud_id=p.id JOIN loomplane_cloud_connections c ON c.org_id=r.org_id AND c.id=r.connection_id WHERE r.org_id=$1 AND r.id=$2 AND r.revision=$3 AND r.deleted_at IS NULL AND p.revision=$4 AND g.project_id=$5 AND g.revision=$6 AND NOT g.revoked AND cardinality(g.target_ids)=0 AND c.generation=$7 AND c.status='ready')`, o.OrgID, o.ResourceID, o.Enrollment.ResourceRevision, o.Input.CloudRevision, a.ProjectID, o.Input.Attachment.GrantRevision, o.Input.ConnectionGeneration).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	details, err := json.Marshal(c.Details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,details,status,missing,message,checked_at,created_by,created_at,updated_at,generation,method,signed_in_by) VALUES($1,$2,$3,$4,$5,$6,$7,'[]','',$8,$9,$8,$8,$10,$11,'')`, c.ID, c.OrgID, c.Cloud, c.Name, c.Account, details, c.Status, c.CreatedAt, c.CreatedBy, c.Generation, c.Method)
	if err != nil {
		return storeError(err)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_runtime_attachments(id,org_id,cloud_id,cluster_id,project_id,namespace,namespace_uid,service_account_uid,runtime_connection_id,endpoint_fingerprint,trust_fingerprint,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, a.ID, a.OrgID, a.CloudID, a.ClusterID, a.ProjectID, a.Namespace, a.NamespaceUID, a.ServiceAccountUID, a.RuntimeConnectionID, a.EndpointFingerprint, a.TrustFingerprint, a.Revision, raw)
	if err != nil {
		return storeError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,tags,managed_resource_id) VALUES($1,$2,$3,$4,$5,$5,'kubernetes',$6,'',1,$7,$7,'managed','{}',$8)`, o.Enrollment.TargetID, a.OrgID, a.ProjectID, a.CloudID, o.Input.Attachment.TargetSlug, c.ID, c.CreatedAt, a.ClusterID)
	if err != nil {
		return storeError(err)
	}
	old := o.Revision
	o.Revision++
	o.UpdatedAt = time.Now().UTC()
	if checkErr := checkpointClusterTx(ctx, tx, o, old); checkErr != nil {
		return checkErr
	}
	if o.Input.Attachment.RuntimeAccess != nil {
		if o.Enrollment.State.AccessInput == nil || o.Enrollment.State.AccessReceipt == nil {
			return ErrConflict
		}
		if err = insertRuntimeAccessTx(ctx, tx, o, *o.Enrollment.State.AccessInput, o.Enrollment.State.AccessReceipt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type CredentialRefresh struct {
	RecoveryKey    string
	RecoveryExpiry *time.Time
	Version        int64
	VaultKey       string
	LeaseOwner     string
	ExpiresAt      *time.Time
}
type ClusterCredentialStore interface {
	OrphanClusterCredentialKeys(context.Context, *RuntimeAttachment) ([]string, error)
	CheckClusterCredentialRefresh(context.Context, *RuntimeAttachment, *CredentialRefresh) error
	BeginClusterCredentialRefresh(context.Context, *RuntimeAttachment, string, string) (*CredentialRefresh, error)
	RecordClusterCredentialExpiry(context.Context, *RuntimeAttachment, *CredentialRefresh, time.Time) error
	FinishClusterCredentialRefresh(context.Context, *RuntimeAttachment, *CredentialRefresh) error
}

func (s *PgStore) BeginClusterCredentialRefresh(ctx context.Context, a *RuntimeAttachment, owner, key string) (*CredentialRefresh, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out := &CredentialRefresh{}
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cluster_credential_refresh(org_id,attachment_id,version,vault_key,lease_owner,lease_until) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '90 seconds') ON CONFLICT(org_id,attachment_id) DO UPDATE SET version=excluded.version,vault_key=excluded.vault_key,expires_at=NULL,recovery_key=CASE WHEN loomplane_cluster_credential_refresh.version=excluded.version THEN loomplane_cluster_credential_refresh.vault_key ELSE '' END,recovery_expiry=CASE WHEN loomplane_cluster_credential_refresh.version=excluded.version THEN loomplane_cluster_credential_refresh.expires_at ELSE NULL END,lease_owner=excluded.lease_owner,lease_until=excluded.lease_until WHERE loomplane_cluster_credential_refresh.lease_until<clock_timestamp() RETURNING version,vault_key,lease_owner,expires_at,recovery_key,recovery_expiry`, a.OrgID, a.ID, a.CredentialVersion+1, key, owner).Scan(&out.Version, &out.VaultKey, &out.LeaseOwner, &out.ExpiresAt, &out.RecoveryKey, &out.RecoveryExpiry)
	if err != nil {
		return nil, storeError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_credential_attempts(org_id,attachment_id,vault_key,version,lease_owner,lease_until) SELECT org_id,attachment_id,vault_key,version,lease_owner,lease_until FROM loomplane_cluster_credential_refresh WHERE org_id=$1 AND attachment_id=$2 AND lease_owner=$3`, a.OrgID, a.ID, owner)
	if err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return out, nil
}
func (s *PgStore) RecordClusterCredentialExpiry(ctx context.Context, a *RuntimeAttachment, r *CredentialRefresh, expiry time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_cluster_credential_refresh SET expires_at=$5 WHERE org_id=$1 AND attachment_id=$2 AND lease_owner=$3 AND version=$4 AND lease_until>clock_timestamp()`, a.OrgID, a.ID, r.LeaseOwner, r.Version, expiry)
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
	r.ExpiresAt = &expiry
	return nil
}
func (s *PgStore) FinishClusterCredentialRefresh(ctx context.Context, a *RuntimeAttachment, r *CredentialRefresh) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, a.OrgID, a.CloudID); checkErr != nil {
		return checkErr
	}
	operation, operationErr := clusterOperationTx(ctx, tx, a.OrgID, a.CloudID, a.OperationID)
	if operationErr != nil {
		return operationErr
	}
	if operation.Status != "succeeded" || operation.CancelRequested || operation.Enrollment == nil || operation.Enrollment.AttachmentID != a.ID {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, operation); checkErr != nil {
		return checkErr
	}
	resource, resourceErr := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 AND deleted_at IS NULL`, a.OrgID, a.ClusterID))
	if resourceErr != nil {
		return resourceErr
	}
	if !clusterAttachmentPins(operation, resource, a) {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_runtime_attachments a SET revision=$3,body=$4 WHERE org_id=$1 AND id=$2 AND revision=$3-1 AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM loomplane_cluster_credential_refresh f WHERE f.org_id=a.org_id AND f.attachment_id=a.id AND f.version=$5 AND f.lease_owner=$6 AND f.vault_key=$7 AND f.lease_until>clock_timestamp())`, a.OrgID, a.ID, a.Revision, raw, r.Version, r.LeaseOwner, r.VaultKey)
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
	_, err = tx.Exec(ctx, `DELETE FROM loomplane_cluster_credential_refresh WHERE org_id=$1 AND attachment_id=$2 AND lease_owner=$3`, a.OrgID, a.ID, r.LeaseOwner)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) CheckClusterCredentialRefresh(ctx context.Context, a *RuntimeAttachment, r *CredentialRefresh) error {
	var valid bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_credential_refresh f JOIN loomplane_runtime_attachments a ON a.org_id=f.org_id AND a.id=f.attachment_id WHERE f.org_id=$1 AND f.attachment_id=$2 AND f.lease_owner=$3 AND f.version=$4 AND f.lease_until>clock_timestamp()+interval '15 seconds' AND a.revision=$5 AND a.deleted_at IS NULL)`, a.OrgID, a.ID, r.LeaseOwner, r.Version, a.Revision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

// OrphanClusterCredentialKeys excludes active and still leased recovery candidates.
func (s *PgStore) OrphanClusterCredentialKeys(ctx context.Context, a *RuntimeAttachment) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT v.vault_key FROM loomplane_cluster_credential_attempts v JOIN loomplane_runtime_attachments a ON a.org_id=v.org_id AND a.id=v.attachment_id WHERE v.org_id=$1 AND v.attachment_id=$2 AND v.lease_until<clock_timestamp() AND v.vault_key<>a.body->>'vault_key' AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_credential_refresh f WHERE f.org_id=v.org_id AND f.attachment_id=v.attachment_id AND (f.vault_key=v.vault_key OR f.recovery_key=v.vault_key))`, a.OrgID, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if scanErr := rows.Scan(&key); scanErr != nil {
			return nil, scanErr
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
