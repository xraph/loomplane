package recovery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const restoreColumns = `id,org_id,project_id,backup_id,status,external_id,error,request_id,created_by,created_at,expires_at,verified_at,duration_ms`

func scanRestore(row pgx.Row) (*Restore, error) {
	var r Restore
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.BackupID, &r.Status, &r.ExternalID, &r.Error, &r.RequestID, &r.CreatedBy, &r.CreatedAt, &r.ExpiresAt, &r.VerifiedAt, &r.DurationMS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}
func (s *PgStore) CreateRestore(ctx context.Context, r *Restore) (*Restore, error) {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_service_restores(id,org_id,project_id,backup_id,status,request_id,created_by,created_at,expires_at) VALUES($1,$2,$3,$4,'queued',$5,$6,$7,$8) ON CONFLICT(org_id,project_id,request_id) DO NOTHING`, r.ID, r.OrgID, r.ProjectID, r.BackupID, r.RequestID, r.CreatedBy, r.CreatedAt, r.ExpiresAt)
	if err != nil {
		return nil, err
	}
	result, err := scanRestore(s.db.QueryRow(ctx, `SELECT `+restoreColumns+` FROM loomplane_service_restores WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, r.OrgID, r.ProjectID, r.RequestID))
	if err != nil {
		return nil, err
	}
	if result.BackupID != r.BackupID {
		return nil, ErrConflict
	}
	return result, nil
}
func (s *PgStore) GetRestore(ctx context.Context, org, id string) (*Restore, error) {
	return scanRestore(s.db.QueryRow(ctx, `SELECT `+restoreColumns+` FROM loomplane_service_restores WHERE org_id=$1 AND id=$2`, org, id))
}
func (s *PgStore) ListRestores(ctx context.Context, org, project string) ([]Restore, error) {
	return s.queryRestores(ctx, `SELECT `+restoreColumns+` FROM loomplane_service_restores WHERE org_id=$1 AND project_id=$2 ORDER BY created_at DESC,id DESC LIMIT 100`, org, project)
}
func (s *PgStore) queryRestores(ctx context.Context, query string, args ...any) ([]Restore, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Restore{}
	for rows.Next() {
		r, scanErr := scanRestore(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
func (s *PgStore) ClaimRestore(ctx context.Context, org, id string) (*Restore, error) {
	r, err := scanRestore(s.db.QueryRow(ctx, `UPDATE loomplane_service_restores SET status='running',lease_until=now()+interval '50 minutes' WHERE org_id=$1 AND id=$2 AND status='queued' AND expires_at>now() RETURNING `+restoreColumns, org, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return r, err
}
func (s *PgStore) FinishRestore(ctx context.Context, r *Restore, external string, duration time.Duration, cause error) error {
	status, message := "succeeded", ""
	var verified *time.Time
	if cause != nil {
		status, message = "failed", "Restore failed. The isolated copy can be removed; the source was not changed."
	} else {
		now := time.Now().UTC()
		verified = &now
	}
	// Both evidence records change together, and only the active claim can publish.
	row := s.db.QueryRow(ctx, `WITH finished AS (
 UPDATE loomplane_service_restores SET status=$3,error=$4,external_id=$5,verified_at=$6,duration_ms=$7,lease_until=NULL
 WHERE org_id=$1 AND id=$2 AND status='running' RETURNING backup_id,org_id,verified_at
 ), evidence AS (UPDATE loomplane_service_backups b SET restored_at=f.verified_at FROM finished f
 WHERE b.id=f.backup_id AND b.org_id=f.org_id AND f.verified_at IS NOT NULL RETURNING b.id)
 SELECT count(*) FROM finished`, r.OrgID, r.ID, status, message, external, verified, duration.Milliseconds())
	var count int
	if err := row.Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) ClaimCleanup(ctx context.Context, org, id string) (*Restore, error) {
	r, err := scanRestore(s.db.QueryRow(ctx, `UPDATE loomplane_service_restores SET status='deleting',lease_until=now()+interval '10 minutes'
 WHERE org_id=$1 AND id=$2 AND (status IN ('succeeded','failed','cleanup_failed') OR (status='deleting' AND lease_until<now())) RETURNING `+restoreColumns, org, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return r, err
}
func (s *PgStore) FinishCleanup(ctx context.Context, r *Restore, cause error) error {
	status, message := "deleted", ""
	if cause != nil {
		status, message = "cleanup_failed", "The temporary copy could not be removed. Check the target and retry cleanup."
	}
	result, err := s.db.Exec(ctx, `UPDATE loomplane_service_restores SET status=$3,error=$4,lease_until=NULL WHERE org_id=$1 AND id=$2 AND status='deleting'`, r.OrgID, r.ID, status, message)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) PendingRestores(ctx context.Context) ([]Restore, error) {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_service_restores SET status='failed',error='Restore worker stopped. Remove the temporary copy and request a new restore.',lease_until=NULL WHERE (status='running' AND lease_until<now()) OR (status='queued' AND expires_at<=now())`)
	if err != nil {
		return nil, err
	}
	return s.queryRestores(ctx, `SELECT `+restoreColumns+` FROM loomplane_service_restores WHERE status='queued' ORDER BY created_at LIMIT 100`)
}
func (s *PgStore) ExpiredRestores(ctx context.Context) ([]Restore, error) {
	return s.queryRestores(ctx, `SELECT `+restoreColumns+` FROM loomplane_service_restores WHERE (expires_at<=now() AND status IN ('succeeded','failed','cleanup_failed')) OR (status='deleting' AND lease_until<now()) ORDER BY expires_at LIMIT 10`)
}
