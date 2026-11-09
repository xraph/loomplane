package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/notifications"
)

// PgStore is the Postgres Store, over loomplane_agent_watchers. Leases run
// on the database's clock, so replicas whose clocks differ agree on them.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const watcherColumns = `org_id, event_cursor, paused, paused_reason, paused_at, checks, swept_at, rechecked_at, ticked_at`

func scanState(row driver.Row) (*State, error) {
	var st State
	var checks []byte
	if err := row.Scan(&st.OrgID, &st.EventCursor, &st.Paused, &st.PausedReason, &st.PausedAt, &checks,
		&st.SweptAt, &st.RecheckedAt, &st.TickedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(checks, &st.Checks); err != nil {
		return nil, fmt.Errorf("read the watcher's checks: %w", err)
	}
	for _, t := range []*time.Time{st.PausedAt, st.SweptAt, st.RecheckedAt, st.TickedAt} {
		if t != nil {
			*t = t.UTC()
		}
	}
	return &st, nil
}

// Claim implements Store in one statement: the insert path sets the
// cursor from highWater, read before it.
func (s *PgStore) Claim(ctx context.Context, orgID, owner string, ttl time.Duration, highWater func() (int64, error)) (*State, bool, error) {
	if orgID == "" || owner == "" || ttl <= 0 {
		return nil, false, errors.New("agentwatch: a claim names its workspace, owner and lease")
	}
	cursor, err := highWater()
	if err != nil {
		return nil, false, fmt.Errorf("read the workspace's event high water: %w", err)
	}
	st, err := scanState(s.db.QueryRow(ctx, `INSERT INTO loomplane_agent_watchers
			(org_id, event_cursor, lease_owner, lease_until, created_at, updated_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4), now(), now())
		ON CONFLICT (org_id) DO UPDATE SET lease_owner = EXCLUDED.lease_owner, lease_until = EXCLUDED.lease_until, updated_at = now()
		WHERE loomplane_agent_watchers.lease_until IS NULL OR loomplane_agent_watchers.lease_until < now()
			OR loomplane_agent_watchers.lease_owner = EXCLUDED.lease_owner
		RETURNING `+watcherColumns, orgID, cursor, owner, ttl.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim the workspace's watcher: %w", err)
	}
	return st, true, nil
}

// Save implements Store.
func (s *PgStore) Save(ctx context.Context, owner string, st *State) error {
	return s.save(ctx, s.db, owner, st)
}

// SaveNotifying implements Store.
func (s *PgStore) SaveNotifying(ctx context.Context, owner string, st *State, send func(ctx context.Context, q notifications.Execer) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a committed transaction cannot roll back
	if serr := s.save(ctx, tx, owner, st); serr != nil {
		return serr
	}
	if serr := send(ctx, tx); serr != nil {
		return serr
	}
	return tx.Commit()
}

func (s *PgStore) save(ctx context.Context, q notifications.Execer, owner string, st *State) error {
	checks := st.Checks
	if checks == nil {
		checks = map[string]Check{}
	}
	raw, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	res, err := q.Exec(ctx, `UPDATE loomplane_agent_watchers
		SET event_cursor = $3, paused = $4, paused_reason = $5, paused_at = $6, checks = $7::jsonb,
			swept_at = $8, rechecked_at = $9, ticked_at = $10, updated_at = now()
		WHERE org_id = $1 AND lease_owner = $2 AND lease_until > now()`,
		st.OrgID, owner, st.EventCursor, st.Paused, st.PausedReason, utc(st.PausedAt), string(raw),
		utc(st.SweptAt), utc(st.RecheckedAt), utc(st.TickedAt))
	if err != nil {
		return fmt.Errorf("save the workspace's watcher: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// Release implements Store.
func (s *PgStore) Release(ctx context.Context, orgID, owner string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_agent_watchers SET lease_owner = '', lease_until = NULL, updated_at = now()
		WHERE org_id = $1 AND lease_owner = $2`, orgID, owner)
	if err != nil {
		return fmt.Errorf("release the workspace's watcher: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID string) (*State, error) {
	st, err := scanState(s.db.QueryRow(ctx, `SELECT `+watcherColumns+` FROM loomplane_agent_watchers WHERE org_id = $1`, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &State{OrgID: orgID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get the workspace's watcher: %w", err)
	}
	return st, nil
}
