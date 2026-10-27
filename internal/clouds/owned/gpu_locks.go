package owned

import (
	"context"
	"errors"
	"sync"

	"github.com/xraph/grove/drivers/pgdriver"
)

// GPULocks serializes GPU device picking on one Docker daemon across
// Loomplane processes. Each lock is a transaction advisory lock on
// gpu-devices/<docker_id> in a transaction of its own that reads and locks
// no rows, so a holder waits behind nothing but another holder.
type GPULocks struct{ db *pgdriver.PgDB }

func NewGPULocks(db *pgdriver.PgDB) *GPULocks { return &GPULocks{db: db} }

// LockGPUs waits for daemon's GPU lock. The function it returns ends the
// lock's transaction and is safe to call more than once.
func (l *GPULocks) LockGPUs(ctx context.Context, daemon string) (func(), error) {
	if daemon == "" {
		return nil, errors.New("a GPU lock needs the server's Docker identity")
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gpu-devices/'||$1,0))`, daemon); err != nil {
		return nil, errors.Join(err, tx.Rollback())
	}
	var once sync.Once
	return func() { once.Do(func() { _ = tx.Rollback() }) }, nil //nolint:errcheck // ending the transaction releases the lock; it holds no writes
}
