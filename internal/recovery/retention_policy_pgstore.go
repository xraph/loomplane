package recovery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

const retentionPolicyColumns = `id,org_id,project_id,anchor_id,source_key,revision,policy,enabled,created_by,authorized_by,created_at,updated_at,next_at,last_error,COALESCE(last_retirement_id,'')`

func scanRetentionPolicy(row pgx.Row) (*RetentionPolicy, error) {
	var p RetentionPolicy
	var raw []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.AnchorID, &p.SourceKey, &p.Revision, &raw, &p.Enabled, &p.CreatedBy, &p.AuthorizedBy, &p.CreatedAt, &p.UpdatedAt, &p.NextAt, &p.LastError, &p.LastRetirementID); err != nil {
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

func (s *PgStore) GetRetentionPolicy(ctx context.Context, org, id string) (*RetentionPolicy, error) {
	return scanRetentionPolicy(s.db.QueryRow(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND id=$2`, org, id))
}
func (s *PgStore) FindRetentionPolicy(ctx context.Context, org, anchorID string) (*RetentionPolicy, error) {
	anchor, err := s.Get(ctx, org, anchorID)
	if err != nil {
		return nil, err
	}
	key, err := retirementSourceKey(anchor)
	if err != nil {
		return nil, err
	}
	return scanRetentionPolicy(s.db.QueryRow(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND source_key=$2`, org, key))
}
func (s *PgStore) queryRetentionPolicies(ctx context.Context, query string, args ...any) ([]RetentionPolicy, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RetentionPolicy{}
	for rows.Next() {
		p, scanErr := scanRetentionPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *PgStore) ListRetentionPolicies(ctx context.Context, org, project string) ([]RetentionPolicy, error) {
	return s.queryRetentionPolicies(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND project_id=$2 ORDER BY created_at,id`, org, project)
}
func (s *PgStore) DueRetentionPolicies(ctx context.Context) ([]RetentionPolicy, error) {
	return s.queryRetentionPolicies(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE enabled=true AND next_at<=now() ORDER BY next_at,id LIMIT 100`)
}

func (s *PgStore) PutRetentionPolicy(ctx context.Context, in *RetentionPolicy, expected int64, guard RetirementGuard) (result *RetentionPolicy, err error) {
	if in == nil || in.ID == "" || in.OrgID == "" || in.ProjectID == "" || in.AnchorID == "" || in.AuthorizedBy == "" || guard == nil || expected < 0 || expected >= 2147483647 {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	anchor, err := s.Get(ctx, in.OrgID, in.AnchorID)
	if err != nil {
		return nil, err
	}
	if anchor.ProjectID != in.ProjectID {
		return nil, ErrNotFound
	}
	key, err := retirementSourceKey(anchor)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if checkErr := lockRetirementSource(ctx, tx, key); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	current, lookupErr := scanRetentionPolicy(tx.QueryRow(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND source_key=$2 FOR UPDATE`, in.OrgID, key))
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
	if current == nil {
		result, err = scanRetentionPolicy(tx.QueryRow(ctx, `INSERT INTO loomplane_retention_policies(id,org_id,project_id,anchor_id,source_key,revision,policy,enabled,created_by,authorized_by,next_at)
 VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$8,now()) ON CONFLICT DO NOTHING RETURNING `+retentionPolicyColumns, in.ID, in.OrgID, in.ProjectID, in.AnchorID, key, raw, in.Enabled, in.AuthorizedBy))
		if errors.Is(err, ErrNotFound) {
			return nil, ErrConflict
		}
	} else {
		result, err = scanRetentionPolicy(tx.QueryRow(ctx, `UPDATE loomplane_retention_policies SET revision=revision+1,policy=$3,enabled=$4,authorized_by=$5,updated_at=now(),next_at=now(),last_error=''
 WHERE org_id=$1 AND id=$2 RETURNING `+retentionPolicyColumns, in.OrgID, current.ID, raw, in.Enabled, in.AuthorizedBy))
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_retention_policy_revisions(org_id,project_id,policy_id,revision,policy,enabled,authorized_by)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, result.OrgID, result.ProjectID, result.ID, result.Revision, raw, result.Enabled, result.AuthorizedBy); err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return result, tx.Commit()
}

// Advance resolves an accepted evaluation under the same source and policy locks
// as admission. Otherwise a lost response or empty concurrent preview could
// advance the request identity without linking its committed retirement.
func (s *PgStore) AdvanceRetentionPolicy(ctx context.Context, p *RetentionPolicy, retirement string, cause error) (err error) {
	if p == nil || p.SourceKey == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if checkErr := lockRetirementSource(ctx, tx, p.SourceKey); checkErr != nil {
		return checkErr
	}
	current, err := scanRetentionPolicy(tx.QueryRow(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, p.ID))
	if err != nil {
		return err
	}
	if current.ProjectID != p.ProjectID || current.SourceKey != p.SourceKey {
		return ErrConflict
	}
	if !current.Enabled || current.Revision != p.Revision || !current.NextAt.Equal(p.NextAt) {
		return tx.Commit()
	}
	var accepted *Retirement
	if retirement != "" {
		accepted, err = scanRetirement(tx.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE org_id=$1 AND id=$2`, p.OrgID, retirement))
	} else {
		accepted, err = scanRetirement(tx.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, p.OrgID, p.ProjectID, retentionPolicyRequestKey(p)))
		if errors.Is(err, ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if accepted != nil {
		if accepted.ProjectID != p.ProjectID || accepted.SourceKey != p.SourceKey || accepted.RetentionPolicyID != p.ID || accepted.RetentionRevision != p.Revision || accepted.CreatedBy != p.AuthorizedBy || accepted.Policy != p.Policy {
			return ErrConflict
		}
		retirement = accepted.ID
	}
	seconds, message := 60, ""
	if cause != nil {
		seconds, message = 3600, "Automatic retention could not be applied. Check policy authority, the retained recovery point and storage; retry in one hour."
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_retention_policies SET next_at=now()+($5*interval '1 second'),last_error=$6,last_retirement_id=COALESCE(NULLIF($7,''),last_retirement_id)
 WHERE org_id=$1 AND id=$2 AND revision=$3 AND next_at=$4 AND enabled=true`, p.OrgID, p.ID, p.Revision, p.NextAt, seconds, message, retirement); err != nil {
		return err
	}
	return tx.Commit()
}

func checkRetentionGrant(ctx context.Context, tx driver.Tx, in *Retirement, sourceKey string) error {
	if in.RetentionPolicyID == "" {
		return nil
	}
	p, err := scanRetentionPolicy(tx.QueryRow(ctx, `SELECT `+retentionPolicyColumns+` FROM loomplane_retention_policies WHERE org_id=$1 AND id=$2 FOR UPDATE`, in.OrgID, in.RetentionPolicyID))
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !p.Enabled || p.ProjectID != in.ProjectID || p.SourceKey != sourceKey || p.Revision != in.RetentionRevision || !p.NextAt.Equal(in.RetentionExpectedAt) || p.AuthorizedBy != in.CreatedBy || p.Policy != in.Policy {
		return ErrConflict
	}
	return nil
}
