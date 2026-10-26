package fabriqloom

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/deployments"
)

// PgStore is the Postgres KeyStore. The schema is migration group
// "loomplane" (database/migrations).
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
	// lease is how long a service lock lasts unless its holder renews it.
	lease time.Duration
}

// keyLockLease outlasts any one of a bind's calls to Fabriq, Vault or the
// key table several times over; the holder renews it at a third of that
// for as long as it works.
const keyLockLease = 2 * time.Minute

// NewPgStore returns a KeyStore over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore {
	return &PgStore{db: db, now: time.Now, lease: keyLockLease}
}

var _ KeyStore = (*PgStore)(nil)

// LockService takes a lease on the service's keys in
// loomplane_fabriq_key_locks. It holds no connection while the caller
// works: it renews the lease in the background and deletes its own row on
// release. A process that dies stops renewing, so the next writer takes the
// lease over once it expires and reconciles what was left in Fabriq. Times
// are the database's, so the processes' clocks don't matter.
func (s *PgStore) LockService(ctx context.Context, orgID, environmentID, resource, service string) (func(), error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("lock Fabriq keys: %w", err)
	}
	holder := hex.EncodeToString(nonce[:])
	key := []any{orgID, environmentID, resource, service}
	lease := s.lease.Milliseconds()
	delay := 20 * time.Millisecond
	for {
		var got string
		err := s.db.QueryRow(ctx, `INSERT INTO loomplane_fabriq_key_locks AS l (org_id, environment_id, resource, service, holder, expires_at)
			VALUES ($1, $2, $3, $4, $5, now() + $6 * interval '1 millisecond')
			ON CONFLICT (org_id, environment_id, resource, service)
			DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at WHERE l.expires_at < now()
			RETURNING holder`, append(key, holder, lease)...).Scan(&got)
		if err == nil {
			return s.holdLease(key, holder), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("lock Fabriq keys: %w", err)
		}
		// Someone else holds it: wait without a connection, so the holder
		// has the pool to record its key.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, time.Second)
	}
}

// holdLease renews the lease until the returned release, which deletes it
// if it is still the holder's.
func (s *PgStore) holdLease(key []any, holder string) func() {
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A renewal that fails is tried again at the next tick; the
				// lease outlasts two of them.
				_, _ = s.db.Exec(ctx, `UPDATE loomplane_fabriq_key_locks SET expires_at = now() + $6 * interval '1 millisecond'
					WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4 AND holder = $5`,
					append(key, holder, s.lease.Milliseconds())...) //nolint:errcheck // retried at the next tick
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			stop()
			<-done
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// A release that fails leaves the lease to expire.
			_, _ = s.db.Exec(ctx, `DELETE FROM loomplane_fabriq_key_locks
				WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4 AND holder = $5`,
				append(key, holder)...) //nolint:errcheck // the lease expires on its own
		})
	}
}

const keyColumns = `id, org_id, project_id, environment_id, resource, service, tenant, prefix, state, created_at, updated_at, revoked_at`

// Insert implements KeyStore.
func (s *PgStore) Insert(ctx context.Context, r *KeyRecord) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_fabriq_keys (`+keyColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.Resource, r.Service, r.Tenant, r.Prefix, string(r.State), r.CreatedAt, r.UpdatedAt, r.RevokedAt)
	if err != nil {
		return keyStoreError("record Fabriq key", err)
	}
	return nil
}

// SetState implements KeyStore. Revoking a key stamps revoked_at.
func (s *PgStore) SetState(ctx context.Context, orgID, id string, from []KeyState, to KeyState) error {
	states := make([]string, 0, len(from))
	for _, st := range from {
		states = append(states, string(st))
	}
	at := s.now().UTC()
	res, err := s.db.Exec(ctx, `UPDATE loomplane_fabriq_keys
		SET state = $4, updated_at = $5, revoked_at = CASE WHEN $4 = 'revoked' THEN $5 ELSE revoked_at END
		WHERE org_id = $1 AND id = $2 AND state = ANY($3::text[])`, orgID, id, states, string(to), at)
	if err != nil {
		return keyStoreError("change Fabriq key state", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("change Fabriq key state: %w", err)
	}
	if n != 1 {
		return ErrStateChanged
	}
	return nil
}

// ForService implements KeyStore.
func (s *PgStore) ForService(ctx context.Context, orgID, environmentID, resource, service string) ([]*KeyRecord, error) {
	return s.list(ctx, `WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4`, orgID, environmentID, resource, service)
}

// ForResource implements KeyStore.
func (s *PgStore) ForResource(ctx context.Context, orgID, environmentID, resource string) ([]*KeyRecord, error) {
	return s.list(ctx, `WHERE org_id = $1 AND environment_id = $2 AND resource = $3`, orgID, environmentID, resource)
}

func (s *PgStore) list(ctx context.Context, where string, args ...any) ([]*KeyRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+keyColumns+` FROM loomplane_fabriq_keys `+where+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list Fabriq keys: %w", err)
	}
	defer rows.Close()
	var out []*KeyRecord
	for rows.Next() {
		var r KeyRecord
		var state string
		if err := rows.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.Resource, &r.Service, &r.Tenant, &r.Prefix, &state, &r.CreatedAt, &r.UpdatedAt, &r.RevokedAt); err != nil {
			return nil, fmt.Errorf("read Fabriq key: %w", err)
		}
		r.State = KeyState(state)
		r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
		if r.RevokedAt != nil {
			at := r.RevokedAt.UTC()
			r.RevokedAt = &at
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Fabriq keys: %w", err)
	}
	return out, nil
}

// keyStoreError is ErrConflict for a unique index's violation.
func keyStoreError(what string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName != "loomplane_fabriq_keys_pkey" {
		return ErrConflict
	}
	return fmt.Errorf("%s: %w", what, err)
}

// PgRebuildStore is the Postgres RebuildStore, over table
// loomplane_fabriq_rebuilds (database/migrations).
type PgRebuildStore struct{ db *pgdriver.PgDB }

// NewPgRebuildStore returns a RebuildStore over db.
func NewPgRebuildStore(db *pgdriver.PgDB) *PgRebuildStore { return &PgRebuildStore{db: db} }

var _ RebuildStore = (*PgRebuildStore)(nil)

const rebuildColumns = `id, org_id, project_id, environment_id, resource, projection, request_id, status, attempt, output, created_by, created_at, finished_at`

func scanRebuild(row pgx.Row) (*Rebuild, error) {
	var r Rebuild
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.Resource, &r.Projection, &r.RequestID, &r.Status, &r.Attempt, &r.Output,
		&r.CreatedBy, &r.CreatedAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	if r.FinishedAt != nil {
		at := r.FinishedAt.UTC()
		r.FinishedAt = &at
	}
	return &r, nil
}

// Create implements RebuildStore. The table's unique indexes keep one
// request id per organization and one queued or running rebuild per
// resource and projection, so two requests racing store one rebuild.
func (s *PgRebuildStore) Create(ctx context.Context, r *Rebuild) (*Rebuild, error) {
	// A rebuild that finishes between the insert and the reads below
	// frees its projection: try again.
	for range 3 {
		if _, err := s.db.Exec(ctx, `INSERT INTO loomplane_fabriq_rebuilds (id, org_id, project_id, environment_id, resource, projection, request_id, status, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'queued', $8, $9) ON CONFLICT DO NOTHING`,
			r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.Resource, r.Projection, r.RequestID, r.CreatedBy, r.CreatedAt); err != nil {
			return nil, fmt.Errorf("record Fabriq rebuild: %w", err)
		}
		same, err := scanRebuild(s.db.QueryRow(ctx, `SELECT `+rebuildColumns+` FROM loomplane_fabriq_rebuilds WHERE org_id = $1 AND request_id = $2`, r.OrgID, r.RequestID))
		switch {
		case err == nil:
			if same.ProjectID != r.ProjectID || same.EnvironmentID != r.EnvironmentID || same.Resource != r.Resource || same.Projection != r.Projection {
				return nil, ErrRequestConflict
			}
			return same, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("read Fabriq rebuild: %w", err)
		}
		active, err := scanRebuild(s.db.QueryRow(ctx, `SELECT `+rebuildColumns+` FROM loomplane_fabriq_rebuilds
			WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND projection = $4 AND status IN ('queued','running')`,
			r.OrgID, r.EnvironmentID, r.Resource, r.Projection))
		switch {
		case err == nil:
			return active, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("read Fabriq rebuild: %w", err)
		}
	}
	return nil, errors.New("record Fabriq rebuild: another rebuild of that projection kept starting and finishing; request it again")
}

// List implements RebuildStore: the latest 50.
func (s *PgRebuildStore) List(ctx context.Context, orgID string, ref deployments.ResourceRef) ([]Rebuild, error) {
	return s.list(ctx, `WHERE org_id = $1 AND project_id = $2 AND environment_id = $3 AND resource = $4 ORDER BY created_at DESC, id DESC LIMIT 50`,
		orgID, ref.ProjectID, ref.EnvironmentID, ref.Resource)
}

// Claim implements RebuildStore.
func (s *PgRebuildStore) Claim(ctx context.Context, orgID, id, attempt string) (*Rebuild, error) {
	r, err := scanRebuild(s.db.QueryRow(ctx, `UPDATE loomplane_fabriq_rebuilds SET status = 'running', attempt = $3
		WHERE org_id = $1 AND id = $2 AND status = 'queued' RETURNING `+rebuildColumns, orgID, id, attempt))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRebuildClaimed
	}
	if err != nil {
		return nil, fmt.Errorf("claim Fabriq rebuild: %w", err)
	}
	return r, nil
}

// Finish implements RebuildStore.
func (s *PgRebuildStore) Finish(ctx context.Context, orgID, id, attempt, status, output string, at time.Time) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_fabriq_rebuilds SET status = $4, output = $5, finished_at = $6
		WHERE org_id = $1 AND id = $2 AND attempt = $3 AND status = 'running'`, orgID, id, attempt, status, output, at)
	if err != nil {
		return fmt.Errorf("finish Fabriq rebuild: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish Fabriq rebuild: %w", err)
	}
	if n != 1 {
		return ErrRebuildClaimed
	}
	return nil
}

// Unfinished implements RebuildStore: the oldest 100.
func (s *PgRebuildStore) Unfinished(ctx context.Context) ([]Rebuild, error) {
	return s.list(ctx, `WHERE status IN ('queued','running') ORDER BY created_at, id LIMIT 100`)
}

func (s *PgRebuildStore) list(ctx context.Context, where string, args ...any) ([]Rebuild, error) {
	rows, err := s.db.Query(ctx, `SELECT `+rebuildColumns+` FROM loomplane_fabriq_rebuilds `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list Fabriq rebuilds: %w", err)
	}
	defer rows.Close()
	out := []Rebuild{}
	for rows.Next() {
		r, err := scanRebuild(rows)
		if err != nil {
			return nil, fmt.Errorf("read Fabriq rebuild: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Fabriq rebuilds: %w", err)
	}
	return out, nil
}
