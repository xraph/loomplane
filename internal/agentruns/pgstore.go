package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store, over loomplane_agent_tasks and
// loomplane_agent_run_events (database/migrations 20261220000200).
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore {
	return &PgStore{db: db, now: time.Now}
}

var _ Store = (*PgStore)(nil)

const taskColumns = `id, org_id, project_id, environment_id, kind, run_id, session_id, status, requested_by,
	max_tokens, tokens, event_seq, error, metadata, created_at, updated_at, finished_at`

func scanTask(row driver.Row) (*Task, error) {
	var t Task
	var kind string
	var metadata []byte
	if err := row.Scan(&t.ID, &t.OrgID, &t.ProjectID, &t.EnvironmentID, &kind, &t.RunID, &t.SessionID, &t.Status,
		&t.RequestedBy, &t.MaxTokens, &t.Tokens, &t.EventSeq, &t.Error, &metadata, &t.CreatedAt, &t.UpdatedAt,
		&t.FinishedAt); err != nil {
		return nil, err
	}
	t.Kind = Kind(kind)
	t.Metadata = map[string]string{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &t.Metadata); err != nil {
			return nil, fmt.Errorf("read agent task metadata: %w", err)
		}
	}
	t.ReleasePlanID, t.ReleaseRevision = t.Metadata[metaReleasePlan], t.Metadata[metaReleaseRevision]
	delete(t.Metadata, metaReleasePlan)
	delete(t.Metadata, metaReleaseRevision)
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if t.FinishedAt != nil {
		f := t.FinishedAt.UTC()
		t.FinishedAt = &f
	}
	return &t, nil
}

func one(row driver.Row) (*Task, error) {
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, t *Task, rec Record) error {
	stored := map[string]string{}
	maps.Copy(stored, t.Metadata)
	if t.ReleasePlanID != "" {
		stored[metaReleasePlan], stored[metaReleaseRevision] = t.ReleasePlanID, t.ReleaseRevision
	}
	metadata, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	var pageContext []byte // NULL without one
	if len(rec.PageContext) > 0 {
		pageContext = rec.PageContext
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_agent_tasks (id, org_id, project_id, environment_id, kind, run_id,
			session_id, status, requested_by, principal, input, max_tokens, metadata, enqueued_at, created_at, updated_at,
			input_preface, page_context)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, $14, $15, $16)`,
		t.ID, t.OrgID, t.ProjectID, t.EnvironmentID, string(t.Kind), t.RunID, t.SessionID, t.Status, t.RequestedBy,
		rec.Principal, rec.Input, t.MaxTokens, metadata, t.CreatedAt, rec.Preface, pageContext)
	if err != nil {
		return fmt.Errorf("create agent task: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, id string) (*Task, error) {
	return one(s.db.QueryRow(ctx, `SELECT `+taskColumns+` FROM loomplane_agent_tasks WHERE org_id = $1 AND id = $2`, orgID, id))
}

// ByRun implements Store.
func (s *PgStore) ByRun(ctx context.Context, orgID, runID string) (*Task, error) {
	return one(s.db.QueryRow(ctx, `SELECT `+taskColumns+` FROM loomplane_agent_tasks WHERE org_id = $1 AND run_id = $2`, orgID, runID))
}

// Record implements Store.
func (s *PgStore) Record(ctx context.Context, orgID, id string) (*Record, error) {
	var rec Record
	var pageContext []byte
	err := s.db.QueryRow(ctx, `SELECT principal, input, input_preface, page_context
		FROM loomplane_agent_tasks WHERE org_id = $1 AND id = $2`, orgID, id).
		Scan(&rec.Principal, &rec.Input, &rec.Preface, &pageContext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read agent task record: %w", err)
	}
	if len(pageContext) > 0 {
		rec.PageContext = pageContext
	}
	return &rec, nil
}

// Session implements Store.
func (s *PgStore) Session(ctx context.Context, orgID, sessionID string) (*Task, error) {
	return one(s.db.QueryRow(ctx, `SELECT `+taskColumns+` FROM loomplane_agent_tasks
		WHERE org_id = $1 AND session_id = $2 ORDER BY created_at DESC LIMIT 1`, orgID, sessionID))
}

func (s *PgStore) query(ctx context.Context, q string, args ...any) ([]*Task, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// List implements Store.
func (s *PgStore) List(ctx context.Context, orgID string, f StoreFilter) ([]*Task, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	return s.query(ctx, `SELECT `+taskColumns+` FROM loomplane_agent_tasks
		WHERE org_id = $1 AND ($2 = '' OR project_id = $2) AND ($3 = '' OR kind = $3) AND ($4 = '' OR requested_by = $4)
		ORDER BY created_at DESC, id DESC LIMIT $5`, orgID, f.ProjectID, string(f.Kind), f.RequestedBy, limit)
}

// Open implements Store.
func (s *PgStore) Open(ctx context.Context, orgID string) ([]*Task, error) {
	return s.query(ctx, `SELECT `+taskColumns+` FROM loomplane_agent_tasks
		WHERE org_id = $1 AND status IN ('queued', 'running', 'waiting') ORDER BY created_at`, orgID)
}

// Transition implements Store.
func (s *PgStore) Transition(ctx context.Context, orgID, id string, tr Transition) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	now := s.now().UTC()
	var tokens any
	if tr.Tokens != nil {
		tokens = *tr.Tokens
	}
	var seq int64
	err = tx.QueryRow(ctx, `UPDATE loomplane_agent_tasks
		SET status = $3,
			error = CASE WHEN $4 = '' THEN error ELSE $4 END,
			tokens = COALESCE($5::integer, tokens),
			finished_at = CASE WHEN $3 IN ('completed', 'failed', 'cancelled') THEN $6 ELSE finished_at END,
			updated_at = $6
		WHERE org_id = $1 AND id = $2 AND status = ANY($7)
		RETURNING event_seq`, orgID, id, tr.Status, tr.Error, tokens, now, tr.From).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("move agent task: %w", err)
	}
	if tr.Event != nil {
		if _, err = appendEvents(ctx, tx, orgID, id, []NewEvent{*tr.Event}); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a committed transaction cannot roll back

// ClaimContinue implements Store.
func (s *PgStore) ClaimContinue(ctx context.Context, orgID, id string, now time.Time, stale time.Duration) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_agent_tasks SET continue_queued_at = $3
		WHERE org_id = $1 AND id = $2 AND status IN ('queued', 'running', 'waiting')
			AND (continue_queued_at IS NULL OR continue_queued_at < $4)`, orgID, id, now.UTC(), now.UTC().Add(-stale))
	if err != nil {
		return false, fmt.Errorf("claim agent task continuation: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearContinue implements Store.
func (s *PgStore) ClearContinue(ctx context.Context, orgID, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_agent_tasks SET continue_queued_at = NULL WHERE org_id = $1 AND id = $2`, orgID, id)
	return err
}

// MarkEnqueued implements Store.
func (s *PgStore) MarkEnqueued(ctx context.Context, orgID, id string, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_agent_tasks SET enqueued_at = $3 WHERE org_id = $1 AND id = $2`, orgID, id, at.UTC())
	return err
}

// AppendEvents implements Store.
func (s *PgStore) AppendEvents(ctx context.Context, orgID, taskID string, events []NewEvent) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	seq, err := appendEvents(ctx, tx, orgID, taskID, events)
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// appendEvents numbers events after the task's event_seq, which it locks
// and bumps, so two writers never take the same number.
func appendEvents(ctx context.Context, tx driver.Tx, orgID, taskID string, events []NewEvent) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `SELECT event_seq FROM loomplane_agent_tasks WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, taskID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	for _, e := range events {
		seq++
		data := e.Data
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_agent_run_events (task_id, org_id, seq, kind, data) VALUES ($1, $2, $3, $4, $5)`,
			taskID, orgID, seq, e.Kind, []byte(data)); err != nil {
			return 0, fmt.Errorf("write agent run event: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_agent_tasks SET event_seq = $3 WHERE org_id = $1 AND id = $2`, orgID, taskID, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// WithholdStep implements Store. The task's row is locked first, as
// appendEvents locks it, so no event lands between the rewrite and e.
func (s *PgStore) WithholdStep(ctx context.Context, orgID, taskID string, e NewEvent) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	var seq int64
	err = tx.QueryRow(ctx, `SELECT event_seq FROM loomplane_agent_tasks WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, taskID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var step int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(seq), 0) FROM loomplane_agent_run_events
		WHERE org_id = $1 AND task_id = $2 AND kind = 'step'`, orgID, taskID).Scan(&step); err != nil {
		return 0, fmt.Errorf("find the stopped step: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_agent_run_events SET data = $3
		WHERE org_id = $1 AND task_id = $2 AND kind = 'token' AND seq > $4`,
		orgID, taskID, []byte(withheldText), step); err != nil {
		return 0, fmt.Errorf("withhold agent run text: %w", err)
	}
	e.Data = withStep(e.Data, step)
	if seq, err = appendEvents(ctx, tx, orgID, taskID, []NewEvent{e}); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// Events implements Store.
func (s *PgStore) Events(ctx context.Context, orgID, taskID string, after int64, limit int) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT task_id, seq, kind, data, created_at FROM loomplane_agent_run_events
		WHERE org_id = $1 AND task_id = $2 AND seq > $3 ORDER BY seq LIMIT $4`, orgID, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data []byte
		if serr := rows.Scan(&e.TaskID, &e.Seq, &e.Kind, &data, &e.At); serr != nil {
			return nil, serr
		}
		e.Data, e.At = json.RawMessage(data), e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// Stale implements Store. It's the one read across workspaces, for the
// sweep: it returns only which tasks, which the sweep then reads each
// through its own workspace.
func (s *PgStore) Stale(ctx context.Context, queuedBefore, continueBefore time.Time, limit int) ([]StaleTask, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id, id, status = 'queued' AND enqueued_at < $1 FROM loomplane_agent_tasks
		WHERE (status = 'queued' AND enqueued_at < $1)
			OR (status IN ('queued', 'running', 'waiting') AND continue_queued_at < $2)
		ORDER BY created_at LIMIT $3`, queuedBefore.UTC(), continueBefore.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaleTask{}
	for rows.Next() {
		var t StaleTask
		if serr := rows.Scan(&t.OrgID, &t.ID, &t.Queued); serr != nil {
			return nil, serr
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
