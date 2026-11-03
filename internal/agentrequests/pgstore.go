package agentrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store, over loomplane_agent_requests
// (database/migrations 20261209000200).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const requestColumns = `id, org_id, project_id, environment_id, task_id, run_id, kind, status, question, context, options, target,
	suspension_id, checkpoint_id, tool_call_id, asked_for, answered_by, answer, outcome, team_id, team_name, route_source,
	team_reason, route_note, expires_at, created_at, updated_at, answered_at`

func scanRequest(row driver.Row) (*Request, error) {
	var r Request
	var kind string
	var ctxJSON, optsJSON, targetJSON []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.TaskID, &r.RunID, &kind, &r.Status, &r.Question,
		&ctxJSON, &optsJSON, &targetJSON, &r.SuspensionID, &r.CheckpointID, &r.ToolCallID, &r.AskedFor, &r.AnsweredBy,
		&r.Answer, &r.Outcome, &r.TeamID, &r.TeamName, &r.RouteSource, &r.TeamReason, &r.RouteNote, &r.ExpiresAt,
		&r.CreatedAt, &r.UpdatedAt, &r.AnsweredAt); err != nil {
		return nil, err
	}
	r.Kind = Kind(kind)
	r.Context, r.Options = []Link{}, []string{}
	for _, part := range []struct {
		raw []byte
		to  any
	}{{ctxJSON, &r.Context}, {optsJSON, &r.Options}, {targetJSON, &r.Target}} {
		if len(part.raw) > 0 {
			if err := json.Unmarshal(part.raw, part.to); err != nil {
				return nil, fmt.Errorf("read agent request %s: %w", r.ID, err)
			}
		}
	}
	r.ExpiresAt, r.CreatedAt, r.UpdatedAt = r.ExpiresAt.UTC(), r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	if r.AnsweredAt != nil {
		at := r.AnsweredAt.UTC()
		r.AnsweredAt = &at
	}
	return &r, nil
}

func (s *PgStore) query(ctx context.Context, q string, args ...any) ([]*Request, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Begin implements Store.
func (s *PgStore) Begin(ctx context.Context) (Tx, error) { return s.db.BeginTx(ctx, nil) }

// Insert implements Store.
func (s *PgStore) Insert(ctx context.Context, tx Tx, r *Request) (bool, error) {
	ctxJSON, err := json.Marshal(nonNil(r.Context))
	if err != nil {
		return false, err
	}
	options := r.Options
	if options == nil {
		options = []string{}
	}
	optsJSON, err := json.Marshal(options)
	if err != nil {
		return false, err
	}
	targetJSON, err := json.Marshal(r.Target)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(ctx, `INSERT INTO loomplane_agent_requests (`+requestColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
			$25, $26, $27, $28)
		ON CONFLICT (run_id, tool_call_id) DO NOTHING`,
		r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.TaskID, r.RunID, string(r.Kind), r.Status, r.Question,
		ctxJSON, optsJSON, targetJSON, r.SuspensionID, r.CheckpointID, r.ToolCallID, r.AskedFor, r.AnsweredBy, r.Answer,
		r.Outcome, r.TeamID, r.TeamName, r.RouteSource, r.TeamReason, r.RouteNote, r.ExpiresAt.UTC(), r.CreatedAt.UTC(),
		r.UpdatedAt.UTC(), r.AnsweredAt)
	if err != nil {
		return false, fmt.Errorf("create agent request: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// micro is t as Postgres keeps it, so a Settlement read back compares
// equal to the one written.
func micro(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func nonNil(links []Link) []Link {
	if links == nil {
		return []Link{}
	}
	return links
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, reqID string) (*Request, error) {
	r, err := scanRequest(s.db.QueryRow(ctx, `SELECT `+requestColumns+` FROM loomplane_agent_requests WHERE org_id = $1 AND id = $2`, orgID, reqID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// List implements Store. A team counts for Mine only while they're a
// current member of the workspace.
func (s *PgStore) List(ctx context.Context, orgID string, f StoreFilter) ([]*Request, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	return s.query(ctx, `SELECT `+requestColumns+` FROM loomplane_agent_requests r
		WHERE r.org_id = $1 AND ($2 = '' OR r.status = $2) AND ($3 = '' OR r.project_id = $3) AND ($4 = '' OR r.task_id = $4)
			AND ($5 = '' OR r.asked_for = $5
				OR (r.team_id <> '' AND EXISTS (SELECT 1 FROM loomplane_team_members tm
					JOIN authsome_members am ON am.id = tm.member_id AND am.org_id = tm.org_id AND am.user_id = tm.user_id
					WHERE tm.org_id = r.org_id AND tm.team_id = r.team_id AND tm.user_id = $5))
				OR ($6 AND r.route_source = 'admins'))
		ORDER BY r.created_at DESC, r.id DESC LIMIT $7`, orgID, f.Status, f.ProjectID, f.TaskID, f.Mine, f.Admin, limit)
}

// Pause implements Store.
func (s *PgStore) Pause(ctx context.Context, orgID, runID, suspensionID string) ([]*Request, error) {
	return s.query(ctx, `SELECT `+requestColumns+` FROM loomplane_agent_requests
		WHERE org_id = $1 AND run_id = $2 AND suspension_id = $3 ORDER BY created_at, id`, orgID, runID, suspensionID)
}

// Settle implements Store.
func (s *PgStore) Settle(ctx context.Context, orgID, reqID string, st Settlement) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_agent_requests
		SET status = $3, outcome = $4, answered_by = $5, answer = $6, answered_at = $7, updated_at = $7
		WHERE org_id = $1 AND id = $2 AND status = 'pending'`, orgID, reqID, st.Status, st.Outcome, st.AnsweredBy, st.Answer, micro(st.At))
	if err != nil {
		return false, fmt.Errorf("settle agent request: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Reopen implements Store.
func (s *PgStore) Reopen(ctx context.Context, orgID, reqID string, st Settlement) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_agent_requests
		SET status = 'pending', outcome = '', answered_by = '', answer = '', answered_at = NULL, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = $3 AND outcome = $4 AND answered_by = $5 AND answered_at = $6`,
		orgID, reqID, st.Status, st.Outcome, st.AnsweredBy, micro(st.At))
	if err != nil {
		return false, fmt.Errorf("reopen agent request: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CancelTask implements Store.
func (s *PgStore) CancelTask(ctx context.Context, orgID, taskID string, at time.Time) ([]*Request, error) {
	return s.query(ctx, `UPDATE loomplane_agent_requests
		SET status = 'cancelled', outcome = 'cancelled', answered_at = $3, updated_at = $3
		WHERE org_id = $1 AND task_id = $2 AND status = 'pending'
		RETURNING `+requestColumns, orgID, taskID, at.UTC())
}

// Due implements Store. It's the one read across workspaces, for the
// sweep: it returns only which requests, which the sweep then reads each
// through its own workspace.
func (s *PgStore) Due(ctx context.Context, now time.Time, limit int) ([]Ref, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id, id FROM loomplane_agent_requests
		WHERE status = 'pending' AND (kind IN ('approve_plan', 'connect_github', 'create_connection') OR expires_at < $1)
		ORDER BY created_at LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ref{}
	for rows.Next() {
		var ref Ref
		if serr := rows.Scan(&ref.OrgID, &ref.ID); serr != nil {
			return nil, serr
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// PgPeople names people from authsome's users.
type PgPeople struct {
	DB    *pgdriver.PgDB
	AppID string
}

// Name returns the person's name, or their username or email, or
// "someone".
func (p PgPeople) Name(ctx context.Context, userID string) string {
	if p.DB == nil || userID == "" {
		return "someone"
	}
	var first, last, display, username, email string
	err := p.DB.QueryRow(ctx, `SELECT first_name, last_name, display_username, username, email FROM authsome_users
		WHERE id = $1 AND app_id = $2 AND deleted_at IS NULL`, userID, p.AppID).Scan(&first, &last, &display, &username, &email)
	if err != nil {
		return "someone"
	}
	for _, name := range []string{strings.TrimSpace(first + " " + last), strings.TrimSpace(display), strings.TrimSpace(username), strings.TrimSpace(email)} {
		if name != "" {
			return name
		}
	}
	return "someone"
}
