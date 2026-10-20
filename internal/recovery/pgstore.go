package recovery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

const columns = `id,org_id,project_id,environment_id,name,status,attempt,error,created_by,created_at,completed_at,restored_at,request_id,source,object,token`

func scan(row pgx.Row, extra ...any) (*Backup, error) {
	var b Backup
	var source, object []byte
	fields := make([]any, 0, 16+len(extra))
	fields = append(fields, &b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.Name, &b.Status, &b.Attempt, &b.Error, &b.CreatedBy, &b.CreatedAt, &b.CompletedAt, &b.RestoredAt, &b.RequestID, &source, &object, &b.Token)
	if err := row.Scan(append(fields, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(source, &b.Source); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(object, &b.Object); err != nil {
		return nil, err
	}
	b.Bytes, b.SHA256 = b.Object.Size, b.Object.SHA256
	return &b, nil
}
func (s *PgStore) Create(ctx context.Context, b *Backup) (*Backup, error) {
	source, err := json.Marshal(b.Source)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,name,status,request_id,source,created_by,created_at,connection_id)
 VALUES($1,$2,$3,$4,$5,'queued',$6,$7,$8,$9,$10) ON CONFLICT(org_id,project_id,request_id) DO NOTHING`, b.ID, b.OrgID, b.ProjectID, b.EnvironmentID, b.Name, b.RequestID, source, b.CreatedBy, b.CreatedAt, b.Source.ConnectionID)
	if err != nil {
		return nil, err
	}
	existing, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_service_backups WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, b.OrgID, b.ProjectID, b.RequestID))
	if err != nil {
		return nil, err
	}
	if existing.EnvironmentID != b.EnvironmentID || existing.Source.ID != b.Source.ID || existing.Source.ExternalID != b.Source.ExternalID || existing.Source.Revision != b.Source.Revision || existing.Source.ConnectionID != b.Source.ConnectionID {
		return nil, ErrConflict
	}
	return existing, nil
}
func (s *PgStore) Get(ctx context.Context, org, id string) (*Backup, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_service_backups WHERE org_id=$1 AND id=$2`, org, id))
}
func (s *PgStore) List(ctx context.Context, org, project string) ([]Backup, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_service_backups WHERE org_id=$1 AND project_id=$2 ORDER BY created_at DESC,id DESC LIMIT 100`, org, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		item, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}
func (s *PgStore) Claim(ctx context.Context, org, id, token string) (*Backup, error) {
	b, err := scan(s.db.QueryRow(ctx, `UPDATE loomplane_service_backups SET status='running',attempt=attempt+1,token=$3,error='',lease_until=now()+interval '50 minutes'
 WHERE org_id=$1 AND id=$2 AND attempt<3 AND (status IN ('queued','failed') OR (status='running' AND lease_until<now())) RETURNING `+columns, org, id, token))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return b, err
}
func (s *PgStore) Finish(ctx context.Context, b *Backup, object backupfiles.Object, cause error) error {
	status, message := "succeeded", ""
	if cause != nil {
		status, message = "failed", "The backup did not finish. Check the source and backup store, then retry."
		object = backupfiles.Object{}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `UPDATE loomplane_service_backups SET status=$4,error=$5,object=$6,completed_at=now(),lease_until=NULL
 WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running'`, b.OrgID, b.ID, b.Token, status, message, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) Retry(ctx context.Context, org, id string) error {
	result, err := s.db.Exec(ctx, `UPDATE loomplane_service_backups SET status='queued',attempt=0,error='',completed_at=NULL WHERE org_id=$1 AND id=$2 AND status='failed'`, org, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) Pending(ctx context.Context) ([]Backup, error) {
	if err := s.expireFinalClaims(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_service_backups WHERE attempt<3 AND (status='queued' OR (status='running' AND lease_until<now())) ORDER BY created_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// Expired final claims become visible failures instead of remaining running
// forever after a worker disappears on its final attempt.
func (s *PgStore) expireFinalClaims(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_service_backups SET status='failed',error='The backup worker stopped before completion. Retry the backup.',completed_at=now(),lease_until=NULL
 WHERE status='running' AND lease_until<now() AND attempt>=3`)
	return err
}
