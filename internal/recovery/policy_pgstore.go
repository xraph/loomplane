package recovery

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const policyColumns = `id,org_id,project_id,environment_id,environment,resource,every_hours,enabled,next_at,last_error,created_by`

func scanPolicy(row pgx.Row) (*Policy, error) {
	var p Policy
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.EnvironmentID, &p.Environment, &p.Resource, &p.EveryHours, &p.Enabled, &p.NextAt, &p.LastError, &p.CreatedBy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}
func (s *PgStore) PutPolicy(ctx context.Context, p *Policy) (*Policy, error) {
	return scanPolicy(s.db.QueryRow(ctx, `INSERT INTO loomplane_backup_policies(id,org_id,project_id,environment_id,environment,resource,every_hours,enabled,next_at,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(org_id,environment_id,resource) DO UPDATE SET every_hours=EXCLUDED.every_hours,enabled=EXCLUDED.enabled,next_at=EXCLUDED.next_at,created_by=EXCLUDED.created_by,last_error='' RETURNING `+policyColumns, p.ID, p.OrgID, p.ProjectID, p.EnvironmentID, p.Environment, p.Resource, p.EveryHours, p.Enabled, p.NextAt, p.CreatedBy))
}
func (s *PgStore) queryPolicies(ctx context.Context, query string, args ...any) ([]Policy, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		p, scanErr := scanPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *PgStore) ListPolicies(ctx context.Context, org, project string) ([]Policy, error) {
	return s.queryPolicies(ctx, `SELECT `+policyColumns+` FROM loomplane_backup_policies WHERE org_id=$1 AND project_id=$2 ORDER BY environment,resource`, org, project)
}
func (s *PgStore) DuePolicies(ctx context.Context) ([]Policy, error) {
	return s.queryPolicies(ctx, `SELECT `+policyColumns+` FROM loomplane_backup_policies WHERE enabled=true AND next_at<=now() ORDER BY next_at LIMIT 100`)
}
func (s *PgStore) AdvancePolicy(ctx context.Context, p *Policy, cause error) error {
	hours, message := p.EveryHours, ""
	if cause != nil {
		hours, message = 1, "The scheduled backup could not be requested. Check permissions, source and backup queue; retry in one hour."
	}
	_, err := s.db.Exec(ctx, `UPDATE loomplane_backup_policies SET next_at=now()+($5*interval '1 hour'),last_error=$6 WHERE org_id=$1 AND id=$2 AND next_at=$3 AND created_by=$4 AND enabled=true`, p.OrgID, p.ID, p.NextAt, p.CreatedBy, hours, message)
	return err
}
