package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func scanManagedResource(row pgx.Row) (*ManagedResource, error) {
	var r ManagedResource
	var raw []byte
	if err := row.Scan(&raw, &r.ID, &r.OrgID, &r.CloudID, &r.Revision); err != nil {
		return nil, storeError(err)
	}
	id, org, cloud, revision := r.ID, r.OrgID, r.CloudID, r.Revision
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	r.ID, r.OrgID, r.CloudID, r.Revision = id, org, cloud, revision
	return &r, nil
}

const managedResourceColumns = `body,id,org_id,coalesce(cloud_id,''),revision`

func (s *PgStore) GetManagedResource(ctx context.Context, org, id string) (*ManagedResource, error) {
	return scanManagedResource(s.db.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2`, org, id))
}
func (s *PgStore) ListManagedResources(ctx context.Context, org, cloud, cursor string, limit int) (ResourcePage, error) {
	limit = resourcePageLimit(limit)
	page := ResourcePage{Items: []*ManagedResource{}}
	query := `SELECT ` + managedResourceColumns + ` FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id>$3 ORDER BY id LIMIT $4`
	if cloud == "" {
		query = `SELECT ` + managedResourceColumns + ` FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id IS NULL AND $2='' AND id>$3 ORDER BY id LIMIT $4`
	}
	rows, err := s.db.Query(ctx, query, org, cloud, cursor, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanManagedResource(rows)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, nil
}

// ClaimManagedResource persists a service-authorized claim with a revision fence.
func (s *PgStore) ClaimManagedResource(ctx context.Context, r *ManagedResource, expectedRevision int64) (*ManagedResource, error) {
	if r == nil || expectedRevision < 0 || r.Revision != expectedRevision+1 {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if r.CloudID != "" {
		err = lockCapacityCloud(ctx, tx, r.OrgID, r.CloudID)
	} else {
		err = cloudprofiles.LockOrganization(ctx, tx, r.OrgID)
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if expectedRevision == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,connection_id,external_id,incarnation,ownership,status,revision,deleted_at,body) VALUES($1,$2,nullif($3,''),$4,$5,$6,nullif($7,''),$8,$9,$10,$11,$12,$13,$14)`, r.ID, r.OrgID, r.CloudID, r.Kind, r.Provider, r.AccountID, r.ConnectionID, r.ExternalID, r.Incarnation, r.Ownership, r.Status, r.Revision, r.DeletedAt, raw)
	} else {
		tag, e := tx.Exec(ctx, `UPDATE loomplane_managed_resources SET connection_id=nullif($15,''),cloud_id=nullif($3,''),ownership=$4,status=$5,revision=$6,deleted_at=$7,body=$8 WHERE org_id=$1 AND id=$2 AND revision=$9 AND (cloud_id IS NULL OR cloud_id=$3) AND kind=$10 AND provider=$11 AND account_id=$12 AND external_id=$13 AND incarnation=$14 AND deleted_at IS NULL`, r.OrgID, r.ID, r.CloudID, r.Ownership, r.Status, r.Revision, r.DeletedAt, raw, expectedRevision, r.Kind, r.Provider, r.AccountID, r.ExternalID, r.Incarnation, r.ConnectionID)
		if e != nil {
			return nil, storeError(e)
		}
		n, e := tag.RowsAffected()
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, ErrConflict
		}
	}
	if err != nil {
		return nil, storeError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	claimed := *r
	return &claimed, nil
}

func (s *PgStore) SaveRuntimeAttachment(ctx context.Context, a *RuntimeAttachment, expectedRevision int64) error {
	if a == nil || expectedRevision < 0 || a.Revision != expectedRevision+1 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, a.OrgID, a.CloudID); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO loomplane_runtime_attachments(id,org_id,cloud_id,cluster_id,project_id,namespace,namespace_uid,service_account_uid,runtime_connection_id,endpoint_fingerprint,trust_fingerprint,revision,deleted_at,body) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14 WHERE $15=0 ON CONFLICT(id) DO NOTHING`, a.ID, a.OrgID, a.CloudID, a.ClusterID, a.ProjectID, a.Namespace, a.NamespaceUID, a.ServiceAccountUID, a.RuntimeConnectionID, a.EndpointFingerprint, a.TrustFingerprint, a.Revision, a.DeletedAt, raw, expectedRevision)
	if expectedRevision > 0 {
		tag, err = tx.Exec(ctx, `UPDATE loomplane_runtime_attachments SET revision=$3,body=$4,deleted_at=$5,endpoint_fingerprint=$13,trust_fingerprint=$14 WHERE org_id=$1 AND id=$2 AND revision=$6 AND cloud_id=$7 AND cluster_id=$8 AND project_id=$9 AND namespace_uid=$10 AND service_account_uid=$11 AND runtime_connection_id=$12 AND namespace=$15 AND deleted_at IS NULL`, a.OrgID, a.ID, a.Revision, raw, a.DeletedAt, expectedRevision, a.CloudID, a.ClusterID, a.ProjectID, a.NamespaceUID, a.ServiceAccountUID, a.RuntimeConnectionID, a.EndpointFingerprint, a.TrustFingerprint, a.Namespace)
	}
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

func (s *PgStore) GetRuntimeAttachment(ctx context.Context, org, id string) (*RuntimeAttachment, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_runtime_attachments WHERE org_id=$1 AND id=$2`, org, id).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var attachment RuntimeAttachment
	if err := json.Unmarshal(raw, &attachment); err != nil {
		return nil, err
	}
	return &attachment, nil
}
