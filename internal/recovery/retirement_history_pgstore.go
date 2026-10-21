package recovery

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// RetirementHistory returns a bounded page in descending commit sequence order.
// Cursors are strings so clients do not round PostgreSQL's 64-bit sequence.
func (s *PgStore) RetirementHistory(ctx context.Context, org, id string, in RetirementHistoryInput) (*RetirementHistory, error) {
	if _, err := s.GetRetirement(ctx, org, id); err != nil {
		return nil, err
	}
	limit, before, err := in.bounds()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT sequence::text,kind,status,attempt,attempt_limit,actor,previous_actor,error,previous_error,created_at
 FROM loomplane_backup_retirement_events WHERE org_id=$1 AND retirement_id=$2 AND ($3::bigint=0 OR sequence<$3)
 ORDER BY sequence DESC LIMIT $4`, org, id, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RetirementHistory{Events: []RetirementEvent{}}
	for rows.Next() {
		var event RetirementEvent
		if checkErr := rows.Scan(&event.Cursor, &event.Kind, &event.Status, &event.Attempt, &event.AttemptLimit, &event.Actor, &event.PreviousActor, &event.Error, &event.PreviousError, &event.CreatedAt); checkErr != nil {
			return nil, checkErr
		}
		page.Events = append(page.Events, event)
	}
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextBefore = page.Events[limit-1].Cursor
	}
	return page, nil
}

func (in RetirementHistoryInput) bounds() (limit int, before int64, err error) {
	limit, err = retirementPageLimit(in.Limit)
	if err != nil {
		return 0, 0, err
	}
	if in.Before != "" {
		before, err = strconv.ParseInt(in.Before, 10, 64)
		if err != nil || before <= 0 || strconv.FormatInt(before, 10) != in.Before {
			return 0, 0, ErrInvalid
		}
	}
	return limit, before, nil
}

func (s *PgStore) RetryRetirement(ctx context.Context, org, id, actor, requestID string, guard RetirementGuard) (result *Retirement, err error) {
	if org == "" || id == "" || actor == "" || !requestKey.MatchString(requestID) || guard == nil {
		return nil, ErrInvalid
	}
	current, err := s.GetRetirement(ctx, org, id)
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
	if checkErr := lockRetirementSource(ctx, tx, current.SourceKey); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	// Lock in the same order as completion: candidate first, then retirement.
	var backupID string
	if checkErr := tx.QueryRow(ctx, `SELECT id FROM loomplane_service_backups WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, org, current.ProjectID, current.BackupID).Scan(&backupID); checkErr != nil {
		return nil, checkErr
	}
	current, err = scanRetirement(tx.QueryRow(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	var existingID, existingActor string
	lookupErr := tx.QueryRow(ctx, `SELECT retirement_id,actor FROM loomplane_backup_retirement_events WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, org, current.ProjectID, requestID).Scan(&existingID, &existingActor)
	if lookupErr == nil {
		if existingID != id || existingActor != actor {
			return nil, ErrConflict
		}
		if checkErr := guard(ctx); checkErr != nil {
			return nil, checkErr
		}
		return current, tx.Commit()
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, lookupErr
	}
	if current.Status != "failed" || current.Attempt > 2147483644 {
		return nil, ErrConflict
	}
	var barrier bool
	if checkErr := tx.QueryRow(ctx, `SELECT status='retiring' FROM loomplane_service_backups WHERE org_id=$1 AND id=$2`, org, backupID).Scan(&barrier); checkErr != nil {
		return nil, checkErr
	}
	if !barrier {
		return nil, ErrConflict
	}
	var sequence int64
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,previous_actor,previous_error,request_id)
 VALUES($1,$2,$3,'retry','queued',$4,$5,$6,$7,$8,$9) ON CONFLICT(org_id,project_id,request_id) DO NOTHING RETURNING sequence`, org, current.ProjectID, id, current.Attempt, current.Attempt+3, actor, current.ExecutionBy, current.Error, requestID).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	result, err = scanRetirement(tx.QueryRow(ctx, `UPDATE loomplane_backup_retirements SET status='queued',execution_by=$3,attempt_limit=attempt+3,token='',lease_until=NULL,next_at=now(),error='',completed_at=NULL WHERE org_id=$1 AND id=$2 RETURNING `+retirementColumns, org, id, actor))
	if err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return result, tx.Commit()
}
