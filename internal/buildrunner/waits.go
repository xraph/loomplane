package buildrunner

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

// waitExpiry is how long a wait counts without its build looking again. A
// worker that crashed mid-wait stops holding a place after it.
const waitExpiry = 2 * time.Minute

// WaitStore keeps the builds waiting for a pool's runners.
type WaitStore interface {
	// Wait records, or refreshes, operation waiting for pool, and returns
	// how many live waits for the pool are older.
	Wait(ctx context.Context, orgID, pool, operation string, now time.Time) (ahead int, err error)
	// Unwait removes operation's wait.
	Unwait(ctx context.Context, operation string) error
}

var _ WaitStore = (*PgStore)(nil)

// Wait implements WaitStore.
func (s *PgStore) Wait(ctx context.Context, orgID, pool, operation string, now time.Time) (int, error) {
	if _, err := s.db.Exec(ctx, `INSERT INTO loomplane_runner_waits(operation_id, org_id, pool, since, seen_at) VALUES($1,$2,$3,$4,$4)
ON CONFLICT (operation_id) DO UPDATE SET seen_at=$4`, operation, orgID, pool, now); err != nil {
		return 0, storeError(err)
	}
	var ahead int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_runner_waits w, loomplane_runner_waits me
WHERE me.operation_id=$1 AND w.pool=me.pool AND w.operation_id<>me.operation_id AND w.seen_at>$2
AND (w.since<me.since OR (w.since=me.since AND w.operation_id<me.operation_id))`, operation, now.Add(-waitExpiry)).Scan(&ahead)
	return ahead, storeError(err)
}

// Unwait implements WaitStore.
func (s *PgStore) Unwait(ctx context.Context, operation string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_runner_waits WHERE operation_id=$1 OR seen_at<$2`, operation, time.Now().Add(-10*waitExpiry))
	return storeError(err)
}

// Wait records a build waiting for a runner of pool, a connection or a
// platform pool's ID, and says how many builds wait ahead of it.
func (s *Service) Wait(ctx context.Context, orgID, pool, operation string) (int, error) {
	store, err := s.st()
	if err != nil {
		return 0, err
	}
	ws, ok := store.(WaitStore)
	if !ok {
		return 0, nil
	}
	return ws.Wait(ctx, orgID, pool, operation, s.now())
}

// Unwait removes a build's wait, once it has a runner or ended.
func (s *Service) Unwait(ctx context.Context, operation string) error {
	store, err := s.st()
	if err != nil {
		return err
	}
	if ws, ok := store.(WaitStore); ok {
		return ws.Unwait(ctx, operation)
	}
	return nil
}

// RunnerCounts are an organization's pools' online runners, keyed by both
// the pool's ID and its connection, so a plan can warn about a pool no
// runner can serve now.
func (s *Service) RunnerCounts(ctx context.Context, orgID string) (map[string]planning.RunnerAvailability, error) {
	store, err := s.st()
	if err != nil {
		return nil, nil //nolint:nilerr // without runner pools there is nothing to count
	}
	pools, err := store.ListPools(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if s.globalAllowed(ctx, orgID) {
		global, gerr := store.ListPools(ctx, "")
		if gerr != nil {
			return nil, gerr
		}
		pools = append(pools, global...)
	}
	out := map[string]planning.RunnerAvailability{}
	for i := range pools {
		p := &pools[i]
		online, oerr := s.online(ctx, store, p, false)
		if oerr != nil {
			return nil, oerr
		}
		build, berr := s.online(ctx, store, p, true)
		if berr != nil {
			return nil, berr
		}
		c := planning.RunnerAvailability{Online: len(online), Build: len(build), OnDemand: p.Mode == ModeOnDemand}
		out[p.ID] = c
		if p.ConnectionID != "" {
			out[p.ConnectionID] = c
		}
	}
	return out, nil
}
