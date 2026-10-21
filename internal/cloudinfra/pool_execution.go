package cloudinfra

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/grove/driver"
)

// PoolExecutionLock coordinates admission across processes. The host effect
// fence establishes quiescence after a lost database session.
type PoolExecutionLock struct {
	tx                 driver.Tx
	org, plan, service string
	generation         int64
	db                 *pgdriver.PgDB
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
	return &PoolExecutionLock{tx: tx, org: org, plan: plan, service: service, db: s.db}, nil
}
func (l *PoolExecutionLock) Close() { rollback(l.tx) }

// BeginPoolExecution allows Dispatch to resume an active checkpoint after a
// process restart. Its next effect must first drain and advance the host fence.
func (s *PgStore) BeginPoolExecution(ctx context.Context, org, plan, service string) (*PoolExecutionLock, error) {
	l, err := s.lockPoolExecution(ctx, org, plan, service)
	if err != nil {
		return nil, err
	}
	var generation int64
	err = s.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET active=true,generation=generation+1,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND phase='reserved' RETURNING generation`, org, plan, service).Scan(&generation)
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("%w: placement was closed for recovery", ErrConflict)
	}
	l.generation = generation
	return l, nil
}

// ExecutionScope follows the generation through durable writes and runtime calls.
type ExecutionScope struct {
	Org, Plan, Service string
	Generation         int64
}
type executionKey struct{}

func WithExecutionScope(ctx context.Context, v ExecutionScope) context.Context {
	return context.WithValue(ctx, executionKey{}, v)
}
func ExecutionFrom(ctx context.Context) (ExecutionScope, bool) {
	v, ok := ctx.Value(executionKey{}).(ExecutionScope)
	return v, ok
}
func (l *PoolExecutionLock) Context(ctx context.Context) context.Context {
	return WithExecutionScope(ctx, ExecutionScope{l.org, l.plan, l.service, l.generation})
}
func (l *PoolExecutionLock) Attempt(ctx context.Context) error {
	var generation int64
	err := l.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET started=true,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND generation=$4 AND active AND phase='reserved' RETURNING generation`, l.org, l.plan, l.service, l.generation).Scan(&generation)
	if err != nil {
		return fmt.Errorf("%w: execution was fenced before effects", ErrConflict)
	}
	return nil
}

// Finish is a compare-and-set on the worker generation. Even if its original
// locking session died, a returned worker can report quiescence, but it cannot
// clear a newer worker's active state.
func (l *PoolExecutionLock) Finish(ctx context.Context) error {
	defer l.Close()
	var generation int64
	err := l.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET active=false,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND generation=$4 AND phase='reserved' RETURNING generation`, l.org, l.plan, l.service, l.generation).Scan(&generation)
	if err != nil {
		return fmt.Errorf("%w: execution generation changed", ErrConflict)
	}
	return nil
}

// GuardPoolWrite validates generation in the caller's write transaction, so
// session loss cannot leave the publication running without its ownership lock.
func GuardPoolWrite(ctx context.Context, tx driver.Tx) error {
	scope, ok := ExecutionFrom(ctx)
	if !ok {
		return nil
	}
	var generation int64
	err := tx.QueryRow(ctx, `SELECT generation FROM loomplane_pool_execution WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND generation=$4 AND active AND phase='reserved' FOR UPDATE`, scope.Org, scope.Plan, scope.Service, scope.Generation).Scan(&generation)
	if err != nil {
		return fmt.Errorf("%w: stale worker cannot publish progress", ErrConflict)
	}
	return nil
}
