package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/pkg/manifest"
)

type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ forge.EventLog = (*PgStore)(nil)

func validOrg(org string) bool {
	return org != "" && len(org) <= 256 && !strings.ContainsAny(org, ":\x00\r\n")
}

// Append is Forge's producer-owned EventLog contract. Business stores append
// through the same SQL function inside their state transaction.
func (s *PgStore) Append(ctx context.Context, org, event string, data []byte) (string, error) {
	if !validOrg(org) || event == "" || event == forge.EventGap || event == forge.EventResumed || len(event) > 128 || !json.Valid(data) || len(data) > 4<<20 {
		return "", ErrInvalid
	}
	var sequence int64
	if err := s.db.QueryRow(ctx, `SELECT loomplane_append_event($1,'',$2,'',$3)`, org, event, data).Scan(&sequence); err != nil {
		return "", fmt.Errorf("append event: %w", err)
	}
	return cursor(org, sequence), nil
}

func (s *PgStore) HighWater(ctx context.Context, org string) (string, error) {
	if !validOrg(org) {
		return "", ErrInvalid
	}
	var seq int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1),0)`, org).Scan(&seq); err != nil {
		return "", err
	}
	return cursor(org, seq), nil
}

// Events returns one bounded, authorized-by-channel page. Callers enforce
// project grants before delivering any row; this store never crosses orgs.
func (s *PgStore) Events(ctx context.Context, org, after string, limit int) ([]Event, bool, error) {
	if limit < 1 || limit > MaxReplayEvents+1 || !validOrg(org) {
		return nil, false, ErrInvalid
	}
	seq, err := position(org, after)
	if err != nil {
		return nil, false, nil
	}
	var floor, high int64
	if rangeErr := s.db.QueryRow(ctx, `SELECT coalesce((SELECT floor_seq FROM loomplane_event_cursors WHERE org_id=$1),0),coalesce((SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1),0)`, org).Scan(&floor, &high); rangeErr != nil {
		return nil, false, rangeErr
	}
	if seq < floor || seq > high {
		return nil, false, nil
	}
	rows, err := s.db.Query(ctx, `SELECT sequence,project_id,type,entity_id,data,created_at FROM loomplane_events WHERE org_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, org, seq, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var event Event
		var n int64
		if scanErr := rows.Scan(&n, &event.ProjectID, &event.Type, &event.EntityID, &event.Data, &event.Time); scanErr != nil {
			return nil, false, scanErr
		}
		event.ID, event.OrgID = cursor(org, n), org
		if hydrateErr := hydrateEvent(&event); hydrateErr != nil {
			return nil, false, hydrateErr
		}
		out = append(out, event)
	}
	return out, true, rows.Err()
}

func hydrateEvent(event *Event) error {
	if event.Type != "manifest.updated" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &fields); err != nil {
		return err
	}
	var yaml string
	if err := json.Unmarshal(fields["yaml"], &yaml); err != nil {
		return err
	}
	parsed, err := manifest.Parse([]byte(yaml))
	if err != nil {
		return err
	}
	body, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	fields["manifest"] = body
	event.Data, err = json.Marshal(fields)
	return err
}

func (s *PgStore) Since(ctx context.Context, channel, id string) (logged []forge.LoggedEvent, resolvable bool, err error) {
	events, found, err := s.Events(ctx, channel, id, MaxReplayEvents+1)
	if err != nil || !found || len(events) > MaxReplayEvents {
		return nil, false, err
	}
	out := make([]forge.LoggedEvent, 0, len(events))
	for _, event := range events {
		out = append(out, forge.LoggedEvent{ID: event.ID, Event: event.Type, Data: event.Data})
	}
	return out, true, nil
}

const logColumns = `sequence,org_id,project_id,deployment_id,action_id,attempt,operation_id,provider_id,stream,source,level,message,time,truncated`

func scanLog(row pgx.Row) (*Log, error) {
	var out Log
	var sequence int64
	err := row.Scan(&sequence, &out.OrgID, &out.ProjectID, &out.DeploymentID, &out.ActionID, &out.Attempt, &out.OperationID, &out.ProviderID, &out.Stream, &out.Source, &out.Level, &out.Message, &out.Time, &out.Truncated)
	if err != nil {
		return nil, err
	}
	out.ID = cursor(out.OrgID, sequence)
	return &out, nil
}

func rollback(tx driver.Tx, err error) error {
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, rollbackErr)
		}
	}
	return err
}

// AppendLog deduplicates provider positions and emits a single attempt-limit
// marker. A saturated attempt returns nil after that marker is retained.
func (s *PgStore) AppendLog(ctx context.Context, input LogInput, redactor *Redactor) (out *Log, err error) {
	if !validOrg(input.OrgID) || input.ProjectID == "" || input.DeploymentID == "" || input.ActionID == "" || input.Attempt == "" || input.ProviderID == "" || len(input.ProviderID) > 512 || len(input.Message) > 1<<20 {
		return nil, ErrInvalid
	}
	for _, field := range []string{input.ProjectID, input.DeploymentID, input.ActionID, input.Attempt, input.OperationID, input.Stream, input.Source, input.Level} {
		if len(field) > 512 {
			return nil, ErrInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_event_cursors(org_id) VALUES($1) ON CONFLICT DO NOTHING`, input.OrgID); err != nil {
		return nil, err
	}
	var high int64
	if readErr := tx.QueryRow(ctx, `SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1 FOR UPDATE`, input.OrgID).Scan(&high); readErr != nil {
		return nil, readErr
	}
	out, err = scanLog(tx.QueryRow(ctx, `SELECT `+logColumns+` FROM loomplane_build_logs WHERE org_id=$1 AND deployment_id=$2 AND action_id=$3 AND attempt=$4 AND operation_id=$5 AND stream=$6 AND provider_id=$7`, input.OrgID, input.DeploymentID, input.ActionID, input.Attempt, input.OperationID, input.Stream, input.ProviderID))
	if err == nil {
		err = tx.Commit()
		return out, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = nil
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_build_log_budgets(org_id,deployment_id,action_id,attempt) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, input.OrgID, input.DeploymentID, input.ActionID, input.Attempt); err != nil {
		return nil, err
	}
	var used int64
	var stopped bool
	if readErr := tx.QueryRow(ctx, `SELECT bytes,truncated FROM loomplane_build_log_budgets WHERE org_id=$1 AND deployment_id=$2 AND action_id=$3 AND attempt=$4 FOR UPDATE`, input.OrgID, input.DeploymentID, input.ActionID, input.Attempt).Scan(&used, &stopped); readErr != nil {
		return nil, readErr
	}
	if stopped {
		err = tx.Commit()
		return nil, err
	}
	message, truncated := redactor.Line(input.Message, MaxLineBytes)
	if used+int64(len(message)) > MaxAttemptBytes {
		message, truncated = "Build output reached the 10 MiB limit for this attempt. Later output is omitted.", true
		stopped = true
		input.Stream, input.Level = "system", "warning"
	}
	if input.Time.IsZero() {
		input.Time = time.Now().UTC()
	}
	out = &Log{ID: cursor(input.OrgID, high+1), OrgID: input.OrgID, ProjectID: input.ProjectID, DeploymentID: input.DeploymentID, ActionID: input.ActionID, Attempt: input.Attempt, OperationID: input.OperationID, ProviderID: input.ProviderID, Stream: input.Stream, Source: input.Source, Level: input.Level, Message: message, Time: input.Time.UTC(), Truncated: truncated}
	payload, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var seq int64
	if appendErr := tx.QueryRow(ctx, `SELECT loomplane_append_event($1,$2,'build.log',$3,$4)`, input.OrgID, input.ProjectID, out.ID, payload).Scan(&seq); appendErr != nil {
		return nil, appendErr
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_build_logs (`+logColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, seq, out.OrgID, out.ProjectID, out.DeploymentID, out.ActionID, out.Attempt, out.OperationID, out.ProviderID, out.Stream, out.Source, out.Level, out.Message, out.Time, out.Truncated); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_build_log_budgets SET bytes=bytes+$5,truncated=$6,updated_at=clock_timestamp() WHERE org_id=$1 AND deployment_id=$2 AND action_id=$3 AND attempt=$4`, input.OrgID, input.DeploymentID, input.ActionID, input.Attempt, len(message), stopped); err != nil {
		return nil, err
	}
	err = tx.Commit()
	return out, err
}

func (s *PgStore) Logs(ctx context.Context, org, deployment, after string, limit int) (LogPage, error) {
	out := LogPage{Logs: []Log{}}
	if !validOrg(org) || limit < 1 || limit > 1000 {
		return out, ErrInvalid
	}
	seq := int64(0)
	if after != "" {
		var err error
		seq, err = position(org, after)
		if err != nil {
			return out, ErrInvalid
		}
		var floor, high int64
		if rangeErr := s.db.QueryRow(ctx, `SELECT coalesce((SELECT floor_seq FROM loomplane_event_cursors WHERE org_id=$1),0),coalesce((SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1),0)`, org).Scan(&floor, &high); rangeErr != nil {
			return out, rangeErr
		}
		out.Gap = seq < floor || seq > high
		if out.Gap {
			seq = 0
		}
	}
	rows, err := s.db.Query(ctx, `SELECT `+logColumns+` FROM loomplane_build_logs WHERE org_id=$1 AND deployment_id=$2 AND sequence>$3 ORDER BY sequence LIMIT $4`, org, deployment, seq, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		line, scanErr := scanLog(rows)
		if scanErr != nil {
			return out, scanErr
		}
		out.Logs = append(out.Logs, *line)
	}
	if len(out.Logs) > limit {
		out.Logs = out.Logs[:limit]
		out.Next = out.Logs[limit-1].ID
	}
	return out, rows.Err()
}

// Prune raises the replay floor in the same transaction as retention deletion.
func (s *PgStore) Prune(ctx context.Context, before time.Time) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	rows, err := tx.Query(ctx, `SELECT org_id FROM loomplane_event_cursors ORDER BY org_id FOR UPDATE`)
	if err != nil {
		return err
	}
	orgs := []string{}
	for rows.Next() {
		var org string
		if err = rows.Scan(&org); err != nil {
			rows.Close()
			return err
		}
		orgs = append(orgs, org)
	}
	rows.Close()
	if readErr := rows.Err(); readErr != nil {
		return readErr
	}
	for _, org := range orgs {
		if _, err = tx.Exec(ctx, `WITH removed AS (DELETE FROM loomplane_events WHERE org_id=$1 AND created_at<$2 RETURNING sequence)
			UPDATE loomplane_event_cursors SET floor_seq=greatest(floor_seq,coalesce((SELECT max(sequence) FROM removed),0)) WHERE org_id=$1`, org, before.UTC()); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_build_log_budgets WHERE updated_at<$1`, before.UTC()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_log_checkpoints WHERE updated_at<$1`, before.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
