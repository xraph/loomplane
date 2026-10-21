package cloudinfra

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/grove/driver"
)

// PoolExecutionLock serializes runtime effects and cleanup across processes.
// A lost lock never clears the durable active flag or releases capacity.
type PoolExecutionLock struct {
	tx                 driver.Tx
	org, plan, service string
}

func (s *PgStore) lockPoolExecution(ctx context.Context, org, plan, service string) (*PoolExecutionLock, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, org+"/"+plan+"/"+service).Scan(&locked)
	if err != nil || !locked {
		rollback(tx)
		return nil, errors.Join(err, fmt.Errorf("%w: a workload worker still owns execution", ErrConflict))
	}
	return &PoolExecutionLock{tx: tx, org: org, plan: plan, service: service}, nil
}
func (l *PoolExecutionLock) Close() { rollback(l.tx) }

// BeginPoolExecution allows Dispatch to resume an active checkpoint after a
// process restart. Abandonment requires an explicit completed return instead.
func (s *PgStore) BeginPoolExecution(ctx context.Context, org, plan, service string) (*PoolExecutionLock, error) {
	l, err := s.lockPoolExecution(ctx, org, plan, service)
	if err != nil {
		return nil, err
	}
	var revision int64
	err = s.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET started=true,active=true,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND phase='reserved' RETURNING revision`, org, plan, service).Scan(&revision)
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("%w: placement was closed for recovery", ErrConflict)
	}
	return l, nil
}

// Finish records quiescence only while the same execution lock is still held.
func (l *PoolExecutionLock) Finish(ctx context.Context) error {
	defer l.Close()
	// Touch the locking session before clearing active. A broken session leaves
	// active durable, so abandonment cannot infer safety from a lost connection.
	var one int
	if err := l.tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		return err
	}
	_, err := l.tx.Exec(ctx, `UPDATE loomplane_pool_execution SET active=false,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3`, l.org, l.plan, l.service)
	if err != nil {
		return err
	}
	return l.tx.Commit()
}
