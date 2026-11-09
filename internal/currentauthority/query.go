package currentauthority

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/observability"
)

type queryTx struct{ attempt *Attempt }

var _ driver.Tx = (*queryTx)(nil)

func (t *queryTx) Commit() error   { return t.attempt.fail(ErrLifecycle) }
func (t *queryTx) Rollback() error { return t.attempt.fail(ErrLifecycle) }

type call struct {
	attempt *Attempt
	ctx     context.Context
	scope   context.Context
	cancel  context.CancelFunc
	stop    func() bool
	op      *observability.Op
}

func (a *Attempt) beginCall(ctx context.Context, query string) (*call, error) {
	c := a.owner
	c.mu.Lock()
	err := a.validLocked(ctx)
	if err == nil && a.pending != 0 {
		err = ErrPending
		a.poison = errors.Join(a.poison, err)
	}
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	select {
	case <-a.gate:
	default:
		a.poison = errors.Join(a.poison, ErrPending)
		c.mu.Unlock()
		return nil, ErrPending
	}
	a.pending++
	c.mu.Unlock()
	scope := ctx
	deadline, _ := a.ctx.Deadline()
	ctx, cancel := context.WithDeadline(ctx, deadline)
	stop := context.AfterFunc(a.ctx, cancel)
	ctx, op := c.telemetry.StartSQL(ctx, query)
	return &call{attempt: a, ctx: ctx, scope: scope, cancel: cancel, stop: stop, op: op}, nil
}

func (c *call) finish(err error) {
	c.op.End(err)
	c.stop()
	c.cancel()
	a := c.attempt
	a.owner.mu.Lock()
	a.pending--
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.poison = errors.Join(a.poison, err)
	}
	a.owner.mu.Unlock()
}

func (t *queryTx) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	call, err := t.attempt.beginCall(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { t.attempt.gate <- struct{}{} }()
	tag, err := t.attempt.tx.Exec(call.ctx, query, args...)
	call.finish(err)
	if err != nil {
		return nil, err
	}
	return result{tag}, nil
}

func (t *queryTx) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	call, err := t.attempt.beginCall(ctx, query)
	if err != nil {
		return nil, err
	}
	a := t.attempt
	defer func() { a.gate <- struct{}{} }()
	rows, err := a.tx.Query(call.ctx, query, args...)
	if err != nil {
		call.finish(err)
		return nil, err
	}
	r := &scopedRows{call: call, rows: rows}
	a.owner.mu.Lock()
	a.rows[r] = struct{}{}
	a.owner.mu.Unlock()
	return r, nil
}

func (t *queryTx) QueryRow(ctx context.Context, query string, args ...any) driver.Row {
	rows, err := t.Query(ctx, query, args...)
	if err != nil {
		return errorRow{err}
	}
	scoped, ok := rows.(*scopedRows)
	if !ok {
		return errorRow{t.attempt.fail(ErrUnavailable)}
	}
	return &scopedRow{rows: scoped}
}

type result struct{ tag pgconn.CommandTag }

func (r result) RowsAffected() (int64, error) { return r.tag.RowsAffected(), nil }
func (r result) LastInsertId() (int64, error) {
	return 0, errors.New("PostgreSQL requires INSERT RETURNING")
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

type scopedRows struct {
	call   *call
	rows   pgx.Rows
	closed bool
	err    error
}

var _ driver.Rows = (*scopedRows)(nil)

// acquire serializes access to pgx, including deferred scans and cleanup. The
// scope check happens after acquiring the gate and before any native row call.
func (r *scopedRows) acquire() error {
	a := r.call.attempt
	select {
	case <-a.gate:
	default:
		return a.fail(ErrPending)
	}
	a.owner.mu.Lock()
	var err error
	// Completed rows retain their result after the query context is canceled.
	// Err and Close may inspect it even when a backend error poisoned the
	// attempt. They still expire with the owning scope and never restart SQL.
	completedInScope := r.closed && a.phase == evaluating &&
		a.ctx.Err() == nil && r.call.scope.Err() == nil &&
		r.call.scope.Value(scopeKey{}) == a && a.profile == a.owner.profile &&
		!a.owner.stopping
	if !completedInScope {
		err = a.validLocked(r.call.scope)
	}
	a.owner.mu.Unlock()
	if err != nil {
		a.gate <- struct{}{}
	}
	return err
}
func (r *scopedRows) release() { r.call.attempt.gate <- struct{}{} }

func (r *scopedRows) closeNative() {
	if r.closed {
		return
	}
	r.rows.Close()
	r.err = errors.Join(r.err, r.rows.Err())
	r.closed = true
	a := r.call.attempt
	a.owner.mu.Lock()
	delete(a.rows, r)
	a.owner.mu.Unlock()
	r.call.finish(r.err)
}

func (r *scopedRows) Next() bool {
	if err := r.acquire(); err != nil {
		return false
	}
	defer r.release()
	if r.closed {
		return false
	}
	if r.rows.Next() {
		return true
	}
	r.closeNative()
	return false
}
func (r *scopedRows) Scan(dest ...any) error {
	if err := r.acquire(); err != nil {
		return err
	}
	defer r.release()
	if r.closed {
		return ErrExpired
	}
	err := r.rows.Scan(dest...)
	if err != nil {
		r.err = errors.Join(r.err, err)
		r.closeNative()
	}
	return err
}
func (r *scopedRows) Columns() ([]string, error) {
	if err := r.acquire(); err != nil {
		return nil, err
	}
	defer r.release()
	if r.closed {
		return nil, ErrExpired
	}
	fields := r.rows.FieldDescriptions()
	cols := make([]string, len(fields))
	for i, field := range fields {
		cols[i] = field.Name
	}
	return cols, nil
}
func (r *scopedRows) Close() error {
	if err := r.acquire(); err != nil {
		return err
	}
	defer r.release()
	r.closeNative()
	return r.err
}
func (r *scopedRows) Err() error {
	if err := r.acquire(); err != nil {
		return err
	}
	defer r.release()
	if !r.closed {
		if err := r.rows.Err(); err != nil {
			r.err = errors.Join(r.err, err)
			r.closeNative()
		}
	}
	return r.err
}

type scopedRow struct{ rows *scopedRows }

func (r *scopedRow) Scan(dest ...any) error {
	rows := r.rows
	if err := rows.acquire(); err != nil {
		return err
	}
	defer rows.release()
	if rows.closed {
		return ErrExpired
	}
	defer rows.closeNative()
	if !rows.rows.Next() {
		if err := rows.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	err := rows.rows.Scan(dest...)
	if err != nil {
		rows.err = errors.Join(rows.err, err)
	}
	return err
}
