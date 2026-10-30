package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

type RuntimeAccessRecord struct {
	OrgID         string                      `json:"org_id"`
	AttachmentID  string                      `json:"attachment_id"`
	OperationID   string                      `json:"operation_id"`
	SchemaVersion int                         `json:"schema_version"`
	Revision      int64                       `json:"revision"`
	CreatedAt     time.Time                   `json:"created_at"`
	UpdatedAt     time.Time                   `json:"updated_at"`
	Input         provider.RuntimeAccessInput `json:"input"`
	Receipt       kube.RuntimeAccessReceipt   `json:"receipt"`
}

type RuntimeAccessStore interface {
	GetRuntimeAccess(context.Context, string, string) (*RuntimeAccessRecord, error)
	PublishRuntimeAccess(context.Context, *ClusterOperation) error
}

func (s *PgStore) GetRuntimeAccess(ctx context.Context, org, attachment string) (*RuntimeAccessRecord, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_runtime_access WHERE org_id=$1 AND attachment_id=$2 ORDER BY revision DESC LIMIT 1`, org, attachment).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out RuntimeAccessRecord
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func runtimeAccessPinsTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	in := o.Input.RuntimeAccess
	if in == nil || !in.Profile.Valid() {
		return ErrInvalid
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT true FROM loomplane_runtime_attachments a
 JOIN loomplane_managed_resources r ON r.org_id=a.org_id AND r.id=a.cluster_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=a.org_id AND g.cloud_id=a.cloud_id AND g.project_id=a.project_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.cloud_id=$3 AND a.cluster_id=$4 AND a.revision=$5 AND a.deleted_at IS NULL
 AND a.project_id=$6 AND a.namespace=$7 AND a.namespace_uid=$8 AND a.service_account_uid=$9
 AND r.revision=$10 AND r.deleted_at IS NULL AND g.revision=$11 AND NOT g.revoked
 AND (cardinality(g.target_ids)=0 OR EXISTS(SELECT 1 FROM loomplane_cloud_targets t WHERE t.org_id=a.org_id AND t.connection_id=a.runtime_connection_id AND t.project_id=a.project_id AND t.id=ANY(g.target_ids)))
 AND coalesce((SELECT max(revision) FROM loomplane_runtime_access c WHERE c.org_id=a.org_id AND c.attachment_id=a.id),0)=$12
 FOR SHARE OF a,r,g`, o.OrgID, in.AttachmentID, o.CloudID, o.Input.ResourceID, in.AttachmentRevision, in.ProjectID, in.Namespace, in.NamespaceUID, in.ServiceAccountUID, o.Input.ResourceRevision, in.GrantRevision, in.CapabilityRevision).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func insertRuntimeAccessTx(ctx context.Context, tx driver.Tx, o *ClusterOperation, in provider.RuntimeAccessInput, receipt *kube.RuntimeAccessReceipt) error {
	if receipt == nil || receipt.RoleUID != in.RoleUID || receipt.OperationID != o.ID || receipt.RequestDigest != o.Digest || receipt.ResourceVersion == "" || receipt.ObservedAt.IsZero() || receipt.ObservedAt.After(time.Now()) || time.Since(receipt.ObservedAt) > 2*time.Minute {
		return ErrConflict
	}
	now := time.Now().UTC()
	record := RuntimeAccessRecord{OrgID: o.OrgID, AttachmentID: in.AttachmentID, OperationID: o.ID, SchemaVersion: 1, Revision: in.CapabilityRevision + 1, CreatedAt: now, UpdatedAt: now, Input: in, Receipt: *receipt}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_runtime_access(org_id,attachment_id,revision,schema_version,operation_id,created_at,updated_at,body) VALUES($1,$2,$3,1,$4,$5,$5,$6)`, o.OrgID, in.AttachmentID, record.Revision, o.ID, now, raw)
	return err
}

func (s *PgStore) PublishRuntimeAccess(ctx context.Context, o *ClusterOperation) error {
	if o.RuntimeAccess == nil || o.RuntimeAccess.Receipt == nil || o.Input.RuntimeAccess == nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if protocolErr := runtimeAccessProtocolTx(ctx, tx); protocolErr != nil {
		return protocolErr
	}
	if err = lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); err != nil {
		return err
	}
	saved, err := clusterOperationTx(ctx, tx, o.OrgID, o.CloudID, o.ID)
	if err != nil {
		return err
	}
	if saved.Revision != o.Revision || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now()) || saved.CancelRequested {
		return ErrConflict
	}
	if err = clusterPinsTx(ctx, tx, o); err != nil {
		return err
	}
	if err = runtimeAccessPinsTx(ctx, tx, o); err != nil {
		return err
	}
	prior := o.Revision
	o.Status = "succeeded"
	o.Error = ""
	o.Revision++
	o.UpdatedAt = time.Now().UTC()
	o.Steps[0].State = "completed"
	o.Steps[0].CompletedAt = &o.UpdatedAt
	if err = checkpointClusterTx(ctx, tx, o, prior); err != nil {
		return err
	}
	if err = insertRuntimeAccessTx(ctx, tx, o, *o.Input.RuntimeAccess, o.RuntimeAccess.Receipt); err != nil {
		return err
	}
	return tx.Commit()
}

func runtimeAccessProtocolTx(ctx context.Context, tx driver.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('loomplane.runtime_access_protocol','runtime-access-v1',true)`)
	return err
}
