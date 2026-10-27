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
// that decide who holds the lease are the database's, so the processes'
// clocks don't matter.
//
// The context it returns ends, with cause ErrLeaseLost, when the lease is
// gone or the holder can no longer be sure it has it, and Insert and
// SetState made with it succeed only while the lease is still the holder's.
func (s *PgStore) LockService(ctx context.Context, orgID, environmentID, resource, service string) (context.Context, func(), error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, fmt.Errorf("lock Fabriq keys: %w", err)
	}
	l := &keyLease{key: []any{orgID, environmentID, resource, service}, holder: hex.EncodeToString(nonce[:])}
	delay := 20 * time.Millisecond
	for {
		var got string
		err := s.db.QueryRow(ctx, `INSERT INTO loomplane_fabriq_key_locks AS l (org_id, environment_id, resource, service, holder, expires_at)
			VALUES ($1, $2, $3, $4, $5, now() + $6 * interval '1 millisecond')
			ON CONFLICT (org_id, environment_id, resource, service)
			DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at WHERE l.expires_at < now()
			RETURNING holder`, l.args(s.lease.Milliseconds())...).Scan(&got)
		if err == nil {
			held, release := s.holdLease(ctx, l)
			return held, release, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			// The upsert may have committed before the answer was lost:
			// don't leave the service locked for a whole lease.
			s.dropLease(l)
			return nil, nil, fmt.Errorf("lock Fabriq keys: %w", err)
		}
		// Someone else holds it: wait without a connection, so the holder
		// has the pool to record its key.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, time.Second)
	}
}

// ErrLeaseLost is work on a service's Fabriq keys whose lease is gone:
// another holder may have taken it over, so the work stops and nothing it
// would record is.
var ErrLeaseLost = errors.New("the lock on the service's Fabriq keys was lost; try again")

// keyLease is one holder's lease on a service's keys.
type keyLease struct {
	key    []any // org_id, environment_id, resource, service
	holder string
}

// args are the lease's key and holder, then extra.
func (l *keyLease) args(extra ...any) []any {
	return append(append(append(make([]any, 0, 5+len(extra)), l.key...), l.holder), extra...)
}

type leaseContextKey struct{}

// leaseOf is the lease ctx was made under, or nil.
func leaseOf(ctx context.Context) *keyLease {
	if l, ok := ctx.Value(leaseContextKey{}).(*keyLease); ok {
		return l
	}
	return nil
}

// renewKeyLock and releaseKeyLock touch a lease only while it is still
// the holder's; a lease that has expired isn't renewed, since someone else
// may hold it already.
const (
	renewKeyLock = `UPDATE loomplane_fabriq_key_locks SET expires_at = now() + $6 * interval '1 millisecond'
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4 AND holder = $5 AND expires_at > now()`
	releaseKeyLock = `DELETE FROM loomplane_fabriq_key_locks
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4 AND holder = $5`
	// leaseHeld is a CTE that says whether lease $1..$5 is held right now.
	leaseHeld = `lease AS (SELECT EXISTS (SELECT 1 FROM loomplane_fabriq_key_locks
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND service = $4 AND holder = $5 AND expires_at > now()) AS held)`
)

// holdLease renews the lease every third of it, under a context made from
// ctx. The context ends when ctx does, when the release returned is called,
// and, with cause ErrLeaseLost, when a renewal finds the lease gone or none
// has succeeded for a lease less a tick (so the holder stops before the
// lease can expire under it). Release stops the renewal and deletes the
// lease if it is still the holder's.
func (s *PgStore) holdLease(ctx context.Context, l *keyLease) (held context.Context, release func()) {
	held, cancel := context.WithCancelCause(context.WithValue(ctx, leaseContextKey{}, l))
	tick := s.lease / 3
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		// Measured on the monotonic clock from just after the last renewal
		// (or the acquisition) succeeded.
		sure := time.NewTimer(s.lease - tick)
		defer sure.Stop()
		for {
			select {
			case <-held.Done():
				return
			case <-sure.C:
				cancel(ErrLeaseLost)
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(held, tick/2)
				res, err := s.db.Exec(renewCtx, renewKeyLock, l.args(s.lease.Milliseconds())...)
				stop()
				if err != nil {
					// Tried again at the next tick, until sure fires.
					continue
				}
				if n, nerr := res.RowsAffected(); nerr == nil && n == 0 {
					cancel(ErrLeaseLost)
					return
				}
				sure.Reset(s.lease - tick)
			}
		}
	}()
	var once sync.Once
	return held, func() {
		once.Do(func() {
			cancel(context.Canceled)
			<-done
			s.dropLease(l)
		})
	}
}

// dropLease deletes the lease if it is still the holder's, with a context
// of its own. A release that fails leaves the lease to expire.
func (s *PgStore) dropLease(l *keyLease) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.db.Exec(ctx, releaseKeyLock, l.args()...) //nolint:errcheck // the lease expires on its own
}

const keyColumns = `id, org_id, project_id, environment_id, resource, service, tenant, prefix, state, created_at, updated_at, revoked_at`

// Insert implements KeyStore. Under a lease (LockService's context), it
// records the key only while the lease is still the holder's.
func (s *PgStore) Insert(ctx context.Context, r *KeyRecord) error {
	values := []any{r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.Resource, r.Service, r.Tenant, r.Prefix, string(r.State), r.CreatedAt, r.UpdatedAt, r.RevokedAt}
	l := leaseOf(ctx)
	if l == nil {
		_, err := s.db.Exec(ctx, `INSERT INTO loomplane_fabriq_keys (`+keyColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`, values...)
		if err != nil {
			return keyStoreError("record Fabriq key", err)
		}
		return nil
	}
	var held bool
	var n int
	err := s.db.QueryRow(ctx, `WITH `+leaseHeld+`, ins AS (
			INSERT INTO loomplane_fabriq_keys (`+keyColumns+`)
			SELECT $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::timestamptz, $16::timestamptz, $17::timestamptz
			WHERE (SELECT held FROM lease) RETURNING 1)
		SELECT (SELECT held FROM lease), (SELECT count(*) FROM ins)`, l.args(values...)...).Scan(&held, &n)
	if err != nil {
		return keyStoreError("record Fabriq key", err)
	}
	if !held {
		return ErrLeaseLost
	}
	return nil
}

// SetState implements KeyStore. Revoking a key stamps revoked_at. Under a
// lease (LockService's context), it changes the key only while the lease is
// still the holder's.
func (s *PgStore) SetState(ctx context.Context, orgID, id string, from []KeyState, to KeyState) error {
	states := make([]string, 0, len(from))
	for _, st := range from {
		states = append(states, string(st))
	}
	at := s.now().UTC()
	const update = `UPDATE loomplane_fabriq_keys
		SET state = $4, updated_at = $5, revoked_at = CASE WHEN $4 = 'revoked' THEN $5 ELSE revoked_at END
		WHERE org_id = $1 AND id = $2 AND state = ANY($3::text[])`
	l := leaseOf(ctx)
	if l == nil {
		res, err := s.db.Exec(ctx, update, orgID, id, states, string(to), at)
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
	var held bool
	var n int
	err := s.db.QueryRow(ctx, `WITH `+leaseHeld+`, upd AS (UPDATE loomplane_fabriq_keys
			SET state = $9, updated_at = $10, revoked_at = CASE WHEN $9 = 'revoked' THEN $10 ELSE revoked_at END
			WHERE org_id = $6 AND id = $7 AND state = ANY($8::text[]) AND (SELECT held FROM lease) RETURNING 1)
		SELECT (SELECT held FROM lease), (SELECT count(*) FROM upd)`, l.args(orgID, id, states, string(to), at)...).Scan(&held, &n)
	if err != nil {
		return keyStoreError("change Fabriq key state", err)
	}
	if !held {
		return ErrLeaseLost
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
