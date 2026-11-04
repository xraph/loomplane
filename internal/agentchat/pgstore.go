package agentchat

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store over agentruns' loomplane_agent_tasks,
// read through the partial indexes of database/migrations
// 20261220000500.
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore {
	return &PgStore{db: db}
}

var _ Store = (*PgStore)(nil)

// conversationColumns are the columns scanConversation reads, from the
// first task f, the latest task l and the turn count n. The title needs
// only the first message's start: 320 characters (4 * TitleLen) fill
// TitleLen runes once runs of spaces are folded.
const conversationColumns = `f.session_id, f.org_id, f.project_id, left(f.input, 320), f.requested_by,
	l.status, l.id, n.turns, f.created_at, l.updated_at`

func scanConversation(row driver.Row) (*Conversation, error) {
	var c Conversation
	var first string
	if err := row.Scan(&c.ID, &c.OrgID, &c.ProjectID, &first, &c.StartedBy, &c.Status, &c.LastTaskID, &c.Turns,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Title = titleOf(first)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return &c, nil
}

// Conversation implements Store.
func (s *PgStore) Conversation(ctx context.Context, orgID, id string) (*Conversation, error) {
	if orgID == "" || id == "" {
		return nil, ErrNotFound
	}
	c, err := scanConversation(s.db.QueryRow(ctx, `SELECT `+conversationColumns+`
		FROM (SELECT count(*) AS turns FROM loomplane_agent_tasks
				WHERE org_id = $1 AND session_id = $2 AND kind = 'chat') n,
			LATERAL (SELECT * FROM loomplane_agent_tasks WHERE org_id = $1 AND session_id = $2 AND kind = 'chat'
				ORDER BY created_at, id LIMIT 1) f,
			LATERAL (SELECT * FROM loomplane_agent_tasks WHERE org_id = $1 AND session_id = $2 AND kind = 'chat'
				ORDER BY created_at DESC, id DESC LIMIT 1) l`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read agent conversation: %w", err)
	}
	return c, nil
}

// List implements Store. Every task of a session has its first task's
// requester and project (agentruns refuses a turn that changes them), so
// filtering tasks by either filters whole conversations.
func (s *PgStore) List(ctx context.Context, orgID, startedBy string, f ListFilter) ([]Conversation, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	rows, err := s.db.Query(ctx, `WITH s AS (
			SELECT session_id, count(*) AS turns,
				(array_agg(id ORDER BY created_at, id))[1] AS first_id,
				(array_agg(id ORDER BY created_at DESC, id DESC))[1] AS last_id
			FROM loomplane_agent_tasks
			WHERE org_id = $1 AND kind = 'chat' AND session_id <> ''
				AND ($2 = '' OR requested_by = $2) AND ($3 = '' OR project_id = $3)
			GROUP BY session_id
		)
		SELECT `+conversationColumns+`
		FROM s n
			JOIN loomplane_agent_tasks f ON f.id = n.first_id
			JOIN loomplane_agent_tasks l ON l.id = n.last_id
		WHERE (NOT $4 OR l.updated_at < $5)
		ORDER BY l.updated_at DESC, n.session_id DESC
		LIMIT $6`, orgID, startedBy, f.ProjectID, !f.Before.IsZero(), f.Before, limit)
	if err != nil {
		return nil, fmt.Errorf("list agent conversations: %w", err)
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("read agent conversation: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Turns implements Store. A before that isn't one of the conversation's
// tasks reads nothing.
func (s *PgStore) Turns(ctx context.Context, orgID, id, before string, limit int) ([]TurnRow, bool, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	rows, err := s.db.Query(ctx, `SELECT id, input, status, error, event_seq, created_at, finished_at
		FROM loomplane_agent_tasks
		WHERE org_id = $1 AND session_id = $2 AND kind = 'chat'
			AND ($3 = '' OR (created_at, id) < (SELECT created_at, id FROM loomplane_agent_tasks
				WHERE org_id = $1 AND session_id = $2 AND kind = 'chat' AND id = $3))
		ORDER BY created_at DESC, id DESC
		LIMIT $4`, orgID, id, before, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("read agent conversation turns: %w", err)
	}
	defer rows.Close()
	out := []TurnRow{}
	for rows.Next() {
		var r TurnRow
		if err := rows.Scan(&r.TaskID, &r.Input, &r.Status, &r.Error, &r.EventSeq, &r.CreatedAt, &r.FinishedAt); err != nil {
			return nil, false, fmt.Errorf("read agent conversation turn: %w", err)
		}
		r.CreatedAt = r.CreatedAt.UTC()
		if r.FinishedAt != nil {
			f := r.FinishedAt.UTC()
			r.FinishedAt = &f
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
