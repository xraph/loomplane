package planning

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type groupQuery interface {
	QueryRow(context.Context, string, ...any) driver.Row
}

func (s *PgStore) groupTx(ctx context.Context, org string, fn func(driver.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackLocationAdoption(tx)
	if sourceErr := environments.LockRuntimeSourcesTx(ctx, tx); sourceErr != nil {
		return sourceErr
	}
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, org); lockErr != nil {
		return lockErr
	}
	if operationErr := fn(tx); operationErr != nil {
		return operationErr
	}
	return tx.Commit()
}

// GroupAttachmentScope reads the authoritative attachment and current target grant.
func (s *PgStore) GroupAttachmentScope(ctx context.Context, org, project, attachment, target string) (*GroupAttachmentScope, error) {
	return readGroupAttachmentScope(ctx, s.db, org, project, attachment, target, false)
}
func readGroupAttachmentScope(ctx context.Context, q groupQuery, org, project, attachment, target string, lock bool) (*GroupAttachmentScope, error) {
	suffix := ""
	if lock {
		suffix = " FOR SHARE OF a,t,g,c"
	}
	var raw []byte
	out := &GroupAttachmentScope{}
	err := q.QueryRow(ctx, `SELECT a.body,t.id,t.revision,g.revision FROM loomplane_runtime_attachments a
 JOIN loomplane_cloud_targets t ON t.org_id=a.org_id AND t.cloud_id=a.cloud_id AND t.project_id=a.project_id AND t.connection_id=a.runtime_connection_id AND t.managed_resource_id=a.cluster_id AND t.provider='kubernetes'
 JOIN loomplane_cloud_project_grants g ON g.org_id=a.org_id AND g.cloud_id=a.cloud_id AND g.project_id=a.project_id AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
 JOIN loomplane_cloud_connections c ON c.org_id=a.org_id AND c.id=a.runtime_connection_id AND c.status='ready' AND c.generation=(a.body->>'trust_revision')::bigint
 AND c.details->>'namespace_uid'=a.namespace_uid AND c.details->>'namespace'=a.namespace AND c.details->>'cluster_ca'=a.trust_fingerprint
 WHERE a.org_id=$1 AND a.project_id=$2 AND a.id=$3 AND a.deleted_at IS NULL AND coalesce(a.body->>'retirement_operation_id','')='' AND ($4='' OR t.id=$4)
 ORDER BY t.id LIMIT 1`+suffix, org, project, attachment, target).Scan(&raw, &out.TargetID, &out.TargetRevision, &out.GrantRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	var a cloudinfra.RuntimeAttachment
	if json.Unmarshal(raw, &a) != nil || a.SchemaVersion != 1 || a.OrgID != org || a.ProjectID != project || a.ID != attachment || a.Revision < 1 || a.NamespaceUID == "" || a.DeletedAt != nil {
		return nil, ErrConflict
	}
	out.Attachment = &a
	return out, nil
}
func recheckGroupAttachment(ctx context.Context, tx driver.Tx, snapshot *GroupAttachmentScope) error {
	a := snapshot.Attachment
	current, err := readGroupAttachmentScope(ctx, tx, a.OrgID, a.ProjectID, a.ID, snapshot.TargetID, true)
	if err != nil {
		return err
	}
	if current.TargetRevision != snapshot.TargetRevision || current.GrantRevision != snapshot.GrantRevision || current.Attachment.Revision != a.Revision || current.Attachment.NamespaceUID != a.NamespaceUID || current.Attachment.TrustRevision != a.TrustRevision {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) groupAuthority(ctx context.Context, org, project, attachment, id string) (*TLSRenewalAuthority, error) {
	return readGroupAuthority(ctx, s.db, org, project, attachment, id)
}
func readGroupAuthority(ctx context.Context, q groupQuery, org, project, attachment, id string) (*TLSRenewalAuthority, error) {
	var raw []byte
	var generation int64
	var version int
	var active bool
	err := q.QueryRow(ctx, `SELECT body,generation,schema_version,active FROM loomplane_tls_renewal_authorities WHERE org_id=$1 AND project_id=$2 AND attachment_id=$3 AND id=$4`, org, project, attachment, id).Scan(&raw, &generation, &version, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	var a TLSRenewalAuthority
	if json.Unmarshal(raw, &a) != nil || version != 1 || a.SchemaVersion != 1 || a.OrgID != org || a.ID != id || a.Generation != generation || a.Active != active {
		return nil, ErrConflict
	}
	return &a, nil
}
func (s *PgStore) saveGroupAuthority(ctx context.Context, project string, scope *GroupAttachmentScope, a *TLSRenewalAuthority, expected int64) error {
	return s.groupTx(ctx, a.OrgID, func(tx driver.Tx) error {
		if err := recheckGroupAttachment(ctx, tx, scope); err != nil {
			return err
		}
		if expected > 0 {
			old, err := readGroupAuthority(ctx, tx, a.OrgID, project, scope.Attachment.ID, a.ID)
			if err != nil {
				return err
			}
			if old.Generation != expected {
				return ErrConflict
			}
			a.CreatedAt = old.CreatedAt
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `INSERT INTO loomplane_tls_renewal_authorities(org_id,id,project_id,attachment_id,generation,active,body) SELECT $1,$2,$3,$4,$5,$6,$7 WHERE $8=0 ON CONFLICT DO NOTHING`, a.OrgID, a.ID, project, scope.Attachment.ID, a.Generation, a.Active, raw, expected)
		if err != nil {
			return err
		}
		if expected > 0 {
			result, err = tx.Exec(ctx, `UPDATE loomplane_tls_renewal_authorities SET generation=$3,active=$4,body=$5,updated_at=now() WHERE org_id=$1 AND id=$2 AND generation=$6`, a.OrgID, a.ID, a.Generation, a.Active, raw, expected)
		}
		if err != nil {
			return err
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrConflict
		}
		return nil
	})
}
func (s *PgStore) saveGroupTimeout(ctx context.Context, p security.Principal, project string, scope *GroupAttachmentScope, id string, in TimeoutVerificationInput) error {
	return s.groupTx(ctx, p.OrgID, func(tx driver.Tx) error {
		if err := recheckGroupAttachment(ctx, tx, scope); err != nil {
			return err
		}
		policy, err := json.Marshal(in.DrainPolicy)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_group_timeout_verifications(org_id,id,project_id,attachment_id,namespace_uid,config_revision,policy,evidence_ref,verified_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.OrgID, id, project, in.AttachmentID, scope.Attachment.NamespaceUID, in.ConfigRevision, policy, in.EvidenceRef, p.UserID)
		return err
	})
}
func (s *PgStore) groupTimeout(ctx context.Context, org, project, attachment, namespace string, policy DrainPolicy, id string) (verificationID, configRevision string, readErr error) {
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", "", err
	}
	var found, config string
	err = s.db.QueryRow(ctx, `SELECT id,config_revision FROM loomplane_group_timeout_verifications WHERE org_id=$1 AND project_id=$2 AND attachment_id=$3 AND namespace_uid=$4 AND policy=$5::jsonb AND ($6='' OR id=$6) AND schema_version=1 ORDER BY created_at DESC,id LIMIT 1`, org, project, attachment, namespace, raw, id).Scan(&found, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrConflict
	}
	return found, config, err
}
func (s *PgStore) GetMemberPrerequisite(ctx context.Context, org, project, id string) (*MemberPrerequisite, error) {
	var raw []byte
	var revision int64
	var schema int
	err := s.db.QueryRow(ctx, `SELECT body,revision,schema_version FROM loomplane_member_prerequisites WHERE org_id=$1 AND project_id=$2 AND id=$3`, org, project, id).Scan(&raw, &revision, &schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p MemberPrerequisite
	if json.Unmarshal(raw, &p) != nil || schema != 1 || p.SchemaVersion != 1 || p.ID != id || p.OrgID != org || p.ProjectID != project || p.Revision != revision {
		return nil, ErrConflict
	}
	return &p, nil
}
func (s *PgStore) saveGroupPrerequisite(ctx context.Context, scope *GroupAttachmentScope, p *MemberPrerequisite, o MemberPrerequisiteObservation, expected int64) error {
	return s.groupTx(ctx, p.OrgID, func(tx driver.Tx) error {
		if err := recheckGroupAttachment(ctx, tx, scope); err != nil {
			return err
		}
		a, err := readGroupAuthority(ctx, tx, p.OrgID, p.ProjectID, p.AttachmentID, p.RenewalAuthorityID)
		if err != nil {
			return err
		}
		if !a.Active || a.Generation != p.RenewalAuthorityGeneration {
			return ErrConflict
		}
		if expected > 0 {
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT body FROM loomplane_member_prerequisites WHERE org_id=$1 AND project_id=$2 AND attachment_id=$3 AND id=$4 AND revision=$5 FOR UPDATE`, p.OrgID, p.ProjectID, p.AttachmentID, p.ID, expected).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			var old MemberPrerequisite
			if json.Unmarshal(raw, &old) != nil {
				return ErrConflict
			}
			p.CreatedAt = old.CreatedAt
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `INSERT INTO loomplane_member_prerequisites(org_id,id,project_id,attachment_id,revision,authority_id,body) SELECT $1,$2,$3,$4,$5,$6,$7 WHERE $8=0 ON CONFLICT DO NOTHING`, p.OrgID, p.ID, p.ProjectID, p.AttachmentID, p.Revision, p.RenewalAuthorityID, raw, expected)
		if err != nil {
			return err
		}
		if expected > 0 {
			result, err = tx.Exec(ctx, `UPDATE loomplane_member_prerequisites SET revision=$3,authority_id=$4,body=$5,updated_at=now() WHERE org_id=$1 AND id=$2 AND revision=$6`, p.OrgID, p.ID, p.Revision, p.RenewalAuthorityID, raw, expected)
		}
		if err != nil {
			return err
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrConflict
		}
		return saveGroupObservation(ctx, tx, p, o)
	})
}
func (s *PgStore) saveGroupMapping(ctx context.Context, scope domain.Scope, destination string, facts *GroupAttachmentScope, expected int64) error {
	return s.groupTx(ctx, scope.OrgID, func(tx driver.Tx) error {
		if err := guardGroupEnvironment(ctx, tx, scope.OrgID, scope.ProjectID, scope.EnvironmentID); err != nil {
			return err
		}
		if err := recheckGroupAttachment(ctx, tx, facts); err != nil {
			return err
		}
		var match bool
		err := tx.QueryRow(ctx, `SELECT connection_id=$5 AND identity->>'namespace_uid'=$6 AND identity->>'account'=$7 AND NOT quarantined FROM loomplane_federation_destinations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND id=$4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, destination, facts.Attachment.RuntimeConnectionID, facts.Attachment.NamespaceUID, facts.Attachment.TrustFingerprint).Scan(&match)
		if err != nil {
			return err
		}
		if !match {
			return ErrConflict
		}
		result, err := tx.Exec(ctx, `INSERT INTO loomplane_group_destination_mappings(org_id,project_id,environment_id,destination_id,target_id,attachment_id,revision) SELECT $1,$2,$3,$4,$5,$6,1 WHERE $7=0 ON CONFLICT DO NOTHING`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, destination, facts.TargetID, facts.Attachment.ID, expected)
		if err != nil {
			return err
		}
		if expected > 0 {
			result, err = tx.Exec(ctx, `UPDATE loomplane_group_destination_mappings SET target_id=$5,revision=revision+1,updated_at=now() WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND destination_id=$4 AND attachment_id=$6 AND revision=$7`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, destination, facts.TargetID, facts.Attachment.ID, expected)
		}
		if err != nil {
			return err
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrConflict
		}
		return nil
	})
}

func sameGroupPrerequisite(a, b domain.PrerequisitePin) bool { return reflect.DeepEqual(a, b) }

func saveGroupObservation(ctx context.Context, tx driver.Tx, p *MemberPrerequisite, o MemberPrerequisiteObservation) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_member_prerequisite_observations(org_id,prerequisite_id,prerequisite_revision,revision,body) VALUES($1,$2,$3,1,$4) ON CONFLICT(org_id,prerequisite_id) DO UPDATE SET prerequisite_revision=excluded.prerequisite_revision,revision=loomplane_member_prerequisite_observations.revision+1,body=excluded.body,updated_at=now()`, p.OrgID, p.ID, p.Revision, raw)
	return err
}
func (s *PgStore) recordGroupObservation(ctx context.Context, scope *GroupAttachmentScope, p *MemberPrerequisite, o MemberPrerequisiteObservation) error {
	if o.SecretUID != p.TLSSecretUID || o.NamespaceUID != p.NamespaceUID || !reflect.DeepEqual(o.Hostnames, p.Hostnames) {
		return ErrConflict
	}
	return s.groupTx(ctx, p.OrgID, func(tx driver.Tx) error {
		if err := recheckGroupAttachment(ctx, tx, scope); err != nil {
			return err
		}
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM loomplane_member_prerequisites WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR SHARE`, p.OrgID, p.ProjectID, p.ID).Scan(&revision); err != nil {
			return err
		}
		if revision != p.Revision {
			return ErrConflict
		}
		return saveGroupObservation(ctx, tx, p, o)
	})
}
