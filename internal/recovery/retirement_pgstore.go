package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

const retirementColumns = `id,org_id,project_id,backup_id,source_key,witness_id,request_id,policy,object,witness_object,status,attempt,token,error,created_by,created_at,completed_at,execution_by,attempt_limit`

func (s *PgStore) GetRetirement(ctx context.Context, org, id string) (*Retirement, error) {
	return scanRetirement(s.db.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE org_id=$1 AND id=$2`, org, id))
}

func (s *PgStore) PendingRetirements(ctx context.Context) ([]Retirement, error) {
	_, err := s.db.Exec(ctx, `WITH changed AS (UPDATE loomplane_backup_retirements SET status='failed',lease_until=NULL,next_at=now()+interval '1 hour',
 error='Retirement worker stopped. Restore admission remains blocked; check the retained recovery point before retrying.'
 WHERE status='running' AND lease_until<now() AND attempt>=attempt_limit RETURNING *)
 INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,error)
 SELECT org_id,project_id,id,'expired',status,attempt,attempt_limit,execution_by,error FROM changed`)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE attempt<attempt_limit
 AND (status='queued' OR (status='failed' AND next_at<=now()) OR (status='running' AND lease_until<now()))
 ORDER BY next_at,created_at,id LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Retirement{}
	for rows.Next() {
		r, scanErr := scanRetirement(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanRetirement(row pgx.Row) (*Retirement, error) {
	var r Retirement
	var policy, object, witness []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.BackupID, &r.SourceKey, &r.WitnessID, &r.RequestID, &policy, &object, &witness, &r.Status, &r.Attempt, &r.Token, &r.Error, &r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ExecutionBy, &r.AttemptLimit); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for _, value := range []struct {
		raw []byte
		dst any
	}{{policy, &r.Policy}, {object, &r.Object}, {witness, &r.WitnessObject}} {
		if err := json.Unmarshal(value.raw, value.dst); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func retirementSourceKey(b *Backup) (string, error) {
	scope, err := retentionScope(b)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal([]string{scope.org, scope.project, scope.environment, scope.name, scope.binding, scope.instance, scope.connection, scope.provider, scope.format})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *PgStore) BeginRetirement(ctx context.Context, in *Retirement, guard RetirementGuard) (result *Retirement, err error) {
	if in == nil || in.ID == "" || in.OrgID == "" || in.ProjectID == "" || in.CreatedBy == "" || !requestKey.MatchString(in.RequestID) || guard == nil {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	anchor, err := s.Get(ctx, in.OrgID, in.BackupID)
	if err != nil {
		return nil, err
	}
	if anchor.ProjectID != in.ProjectID {
		return nil, ErrNotFound
	}
	sourceKey, err := retirementSourceKey(anchor)
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loomplane-backup-retirement:"+sourceKey); err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	existing, lookupErr := scanRetirement(tx.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, in.OrgID, in.ProjectID, in.RequestID))
	if lookupErr == nil {
		if existing.BackupID != in.BackupID || existing.Policy != in.Policy {
			return nil, ErrConflict
		}
		if checkErr := guard(ctx); checkErr != nil {
			return nil, checkErr
		}
		return existing, tx.Commit()
	}
	if !errors.Is(lookupErr, ErrNotFound) {
		return nil, lookupErr
	}
	var active bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_backup_retirements WHERE org_id=$1 AND source_key=$2 AND status<>'succeeded')`, in.OrgID, sourceKey).Scan(&active); checkErr != nil {
		return nil, checkErr
	}
	if active {
		return nil, ErrConflict
	}
	anchor, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_service_backups WHERE org_id=$1 AND id=$2 FOR UPDATE`, in.OrgID, in.BackupID))
	if err != nil {
		return nil, err
	}
	if anchor.Status != "succeeded" || anchor.Object.DestinationID == "" {
		return nil, ErrConflict
	}
	currentKey, err := retirementSourceKey(anchor)
	if err != nil || currentKey != sourceKey {
		return nil, ErrConflict
	}
	points, err := retentionInventory(ctx, tx, anchor)
	if err != nil {
		return nil, err
	}
	preview, err := previewRetention(time.Now().UTC(), in.Policy, anchor, points)
	if err != nil {
		return nil, err
	}
	eligible := false
	for _, point := range preview.Points {
		if point.BackupID == in.BackupID && point.Eligible {
			eligible = true
		}
	}
	witness := verifiedRetentionPoint(time.Now().UTC(), points)
	if !eligible || witness == nil || witness.Object.DestinationID == "" {
		return nil, ErrConflict
	}
	policy, err := json.Marshal(in.Policy)
	if err != nil {
		return nil, err
	}
	object, err := json.Marshal(anchor.Object)
	if err != nil {
		return nil, err
	}
	witnessObject, err := json.Marshal(witness.Object)
	if err != nil {
		return nil, err
	}
	result, err = scanRetirement(tx.QueryRow(ctx, `INSERT INTO loomplane_backup_retirements(id,org_id,project_id,backup_id,source_key,witness_id,request_id,policy,object,witness_object,status,created_by,execution_by)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued',$11,$11) ON CONFLICT(org_id,project_id,request_id) DO NOTHING RETURNING `+retirementColumns, in.ID, in.OrgID, in.ProjectID, in.BackupID, sourceKey, witness.ID, in.RequestID, policy, object, witnessObject, in.CreatedBy))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if checkErr := recordRetirementEvent(ctx, tx, result, "requested", "queued", ""); checkErr != nil {
		return nil, checkErr
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_service_backups SET status='retiring' WHERE org_id=$1 AND id=$2`, in.OrgID, in.BackupID); err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return result, tx.Commit()
}

func (s *PgStore) ClaimRetirement(ctx context.Context, org, id, token string) (result *Retirement, err error) {
	if token == "" {
		return nil, ErrInvalid
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
	previous, err := scanRetirement(tx.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements
 WHERE org_id=$1 AND id=$2 AND attempt<attempt_limit AND (status='queued' OR (status='failed' AND next_at<=now()) OR (status='running' AND lease_until<now())) FOR UPDATE`, org, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if previous.Status == "running" {
		if checkErr := recordRetirementEvent(ctx, tx, previous, "expired", "failed", "Worker lease expired before a result was recorded. Restore admission remains blocked."); checkErr != nil {
			return nil, checkErr
		}
	}
	result, err = scanRetirement(tx.QueryRow(ctx, `UPDATE loomplane_backup_retirements SET status='running',attempt=attempt+1,token=$3,lease_until=now()+interval '50 minutes',error='' WHERE org_id=$1 AND id=$2 RETURNING `+retirementColumns, org, id, token))
	if err != nil {
		return nil, err
	}
	if checkErr := recordRetirementEvent(ctx, tx, result, "claimed", "running", ""); checkErr != nil {
		return nil, checkErr
	}
	return result, tx.Commit()
}

func recordRetirementEvent(ctx context.Context, tx driver.Tx, r *Retirement, kind, status, message string) error {
	_, err := tx.Exec(ctx, `INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,error) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.OrgID, r.ProjectID, r.ID, kind, status, r.Attempt, r.AttemptLimit, r.ExecutionBy, message)
	return err
}

func (s *PgStore) FinishRetirement(ctx context.Context, r *Retirement, cause error) error {
	status, message := "succeeded", ""
	if cause != nil {
		status, message = "failed", "Retirement did not finish. Restore admission remains blocked; check the retained recovery point and retry."
	}
	var count int
	err := s.db.QueryRow(ctx, `WITH locked AS (
 SELECT id,org_id,project_id FROM loomplane_service_backups WHERE org_id=$1 AND id=$6 AND project_id=$7 AND status='retiring' FOR UPDATE
 ), finished AS (
 UPDATE loomplane_backup_retirements SET status=$4,error=$5,lease_until=NULL,next_at=now()+interval '1 hour',completed_at=CASE WHEN $4='succeeded' THEN now() ELSE NULL END
 WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running' AND backup_id=$6 AND project_id=$7 AND attempt=$8 AND execution_by=$9 AND EXISTS(SELECT 1 FROM locked) RETURNING *
 ), recorded AS (
 INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,error)
 SELECT org_id,project_id,id,status,status,attempt,attempt_limit,execution_by,error FROM finished
 ), retired AS (
 UPDATE loomplane_service_backups b SET status='retired' FROM finished f WHERE $4='succeeded' AND b.org_id=f.org_id AND b.project_id=f.project_id AND b.id=f.backup_id AND b.status='retiring' RETURNING b.id
 ) SELECT count(*) FROM finished`, r.OrgID, r.ID, r.Token, status, message, r.BackupID, r.ProjectID, r.Attempt, r.ExecutionBy).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
