package recovery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

const nativeRetentionPolicyColumns = `id,org_id,project_id,source_id,revision,policy,enabled,created_by,authorized_by,created_at,updated_at,next_at,last_error,COALESCE(last_retirement_id,'')`

func scanNativeRetentionPolicy(row pgx.Row) (*NativeRetentionPolicy, error) {
	var p NativeRetentionPolicy
	var raw []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.SourceID, &p.Revision, &raw, &p.Enabled, &p.CreatedBy, &p.AuthorizedBy, &p.CreatedAt, &p.UpdatedAt, &p.NextAt, &p.LastError, &p.LastRetirementID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &p.Policy); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PgStore) NativeRetentionPolicyForSource(ctx context.Context, org, source string) (*NativeRetentionPolicy, error) {
	return scanNativeRetentionPolicy(s.db.QueryRow(ctx, `SELECT `+nativeRetentionPolicyColumns+` FROM loomplane_native_retention_policies WHERE org_id=$1 AND source_id=$2`, org, source))
}
func (s *PgStore) DueNativeRetentionPolicies(ctx context.Context) ([]NativeRetentionPolicy, error) {
	rows, err := s.db.Query(ctx, `SELECT `+nativeRetentionPolicyColumns+` FROM loomplane_native_retention_policies WHERE enabled AND next_at<=now() ORDER BY next_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NativeRetentionPolicy{}
	for rows.Next() {
		p, scanErr := scanNativeRetentionPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *PgStore) PutNativeRetentionPolicy(ctx context.Context, in *NativeRetentionPolicy, expected int64, guard RetirementGuard) (*NativeRetentionPolicy, error) {
	if in == nil || in.ID == "" || in.OrgID == "" || in.ProjectID == "" || in.SourceID == "" || in.AuthorizedBy == "" || expected < 0 || expected >= 2147483647 || guard == nil {
		return nil, ErrInvalid
	}
	if err := in.Policy.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 AND project_id=$3 FOR UPDATE`, in.OrgID, in.SourceID, in.ProjectID))
	if err != nil {
		return nil, err
	}
	if source.Provider != "rds" && source.Provider != "cloud-sql" {
		return nil, ErrInvalid
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	current, lookupErr := scanNativeRetentionPolicy(tx.QueryRow(ctx, `SELECT `+nativeRetentionPolicyColumns+` FROM loomplane_native_retention_policies WHERE org_id=$1 AND source_id=$2 FOR UPDATE`, in.OrgID, in.SourceID))
	if lookupErr != nil && !errors.Is(lookupErr, ErrNotFound) {
		return nil, lookupErr
	}
	if current == nil && expected != 0 || current != nil && (current.Revision != expected || current.ProjectID != in.ProjectID) {
		return nil, ErrConflict
	}
	raw, err := json.Marshal(in.Policy)
	if err != nil {
		return nil, err
	}
	var out *NativeRetentionPolicy
	if current == nil {
		out, err = scanNativeRetentionPolicy(tx.QueryRow(ctx, `INSERT INTO loomplane_native_retention_policies(id,org_id,project_id,source_id,revision,policy,enabled,created_by,authorized_by) VALUES($1,$2,$3,$4,1,$5,$6,$7,$7) RETURNING `+nativeRetentionPolicyColumns, in.ID, in.OrgID, in.ProjectID, in.SourceID, raw, in.Enabled, in.AuthorizedBy))
	} else {
		out, err = scanNativeRetentionPolicy(tx.QueryRow(ctx, `UPDATE loomplane_native_retention_policies SET revision=revision+1,policy=$3,enabled=$4,authorized_by=$5,updated_at=now(),next_at=now(),last_error='' WHERE org_id=$1 AND id=$2 RETURNING `+nativeRetentionPolicyColumns, in.OrgID, current.ID, raw, in.Enabled, in.AuthorizedBy))
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_native_retention_revisions(org_id,policy_id,revision,policy,enabled,authorized_by) VALUES($1,$2,$3,$4,$5,$6)`, out.OrgID, out.ID, out.Revision, raw, out.Enabled, out.AuthorizedBy); err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}

func checkNativeRetentionGrant(ctx context.Context, tx driver.Tx, in *NativeRetirement) error {
	if in.PolicyID == "" {
		if in.PolicyRevision != 0 || !in.ExpectedPolicyAt.IsZero() {
			return ErrInvalid
		}
		return nil
	}
	p, err := scanNativeRetentionPolicy(tx.QueryRow(ctx, `SELECT `+nativeRetentionPolicyColumns+` FROM loomplane_native_retention_policies WHERE org_id=$1 AND id=$2 FOR UPDATE`, in.OrgID, in.PolicyID))
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !p.Enabled || p.ProjectID != in.ProjectID || p.SourceID != in.SourceID || p.Revision != in.PolicyRevision || !p.NextAt.Equal(in.ExpectedPolicyAt) || p.AuthorizedBy != in.CreatedBy || p.Policy != in.Policy || in.RequestID != nativeRetentionPolicyRequestKey(p) {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) AdvanceNativeRetentionPolicy(ctx context.Context, p *NativeRetentionPolicy, retirement string, cause error) error {
	if p == nil || p.SourceID == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	if _, err = scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, p.SourceID)); err != nil {
		return err
	}
	current, err := scanNativeRetentionPolicy(tx.QueryRow(ctx, `SELECT `+nativeRetentionPolicyColumns+` FROM loomplane_native_retention_policies WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, p.ID))
	if err != nil {
		return err
	}
	if current.ProjectID != p.ProjectID || current.SourceID != p.SourceID {
		return ErrConflict
	}
	if !current.Enabled || current.Revision != p.Revision || !current.NextAt.Equal(p.NextAt) {
		return tx.Commit()
	}
	var accepted *NativeRetirement
	if retirement != "" {
		accepted, err = scanNativeRetirement(tx.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND id=$2`, p.OrgID, retirement))
	} else {
		accepted, err = scanNativeRetirement(tx.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2 AND request_id=$3`, p.OrgID, p.SourceID, nativeRetentionPolicyRequestKey(p)))
		if errors.Is(err, ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if accepted != nil {
		if accepted.SourceID != p.SourceID || accepted.ProjectID != p.ProjectID || accepted.PolicyID != p.ID || accepted.PolicyRevision != p.Revision || accepted.CreatedBy != p.AuthorizedBy || accepted.Policy != p.Policy {
			return ErrConflict
		}
		retirement = accepted.ID
	}
	seconds, message := 60, ""
	if cause != nil {
		seconds, message = 3600, "Native retention needs attention. Check source access, the verified witness and any unresolved deletion; evaluation resumes in one hour."
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_native_retention_policies SET next_at=now()+($5*interval '1 second'),last_error=$6,last_retirement_id=COALESCE(NULLIF($7,''),last_retirement_id) WHERE org_id=$1 AND id=$2 AND revision=$3 AND next_at=$4 AND enabled`, p.OrgID, p.ID, p.Revision, p.NextAt, seconds, message, retirement); err != nil {
		return err
	}
	return tx.Commit()
}
