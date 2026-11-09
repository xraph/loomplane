package currentauthority

import (
	"context"
	"errors"

	"github.com/xraph/warden/store"
)

// A private marker must never fall through to ordinary authority, even when it
// belongs to another coordinator or was retained beyond its attempt.
func (c *Coordinator) readerScope(ctx context.Context) (*Attempt, bool, error) {
	marker := ctx.Value(scopeKey{})
	if marker == nil {
		return nil, false, nil
	}
	a, ok := marker.(*Attempt)
	if !ok || a == nil || a.owner == nil {
		return nil, true, ErrBinding
	}
	if a.owner != c {
		return nil, true, a.fail(ErrBinding)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := a.validLocked(ctx); err != nil {
		return nil, true, err
	}
	if !a.fenced {
		a.poison = errors.Join(a.poison, ErrUnavailable)
		return nil, true, ErrUnavailable
	}
	if err := a.profile.verify(); err != nil {
		a.poison = errors.Join(a.poison, err)
		return nil, true, err
	}
	return a, true, nil
}

func (a *Attempt) beginReader(ctx context.Context) (*readerCall, store.Store, error) {
	c := a.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := a.validLocked(ctx); err != nil {
		return nil, nil, err
	}
	if !a.fenced || c.reader == nil {
		a.poison = errors.Join(a.poison, ErrUnavailable)
		return nil, nil, ErrUnavailable
	}
	if a.pending != 0 {
		a.poison = errors.Join(a.poison, ErrPending)
		return nil, nil, ErrPending
	}
	select {
	case <-a.gate:
	default:
		a.poison = errors.Join(a.poison, ErrPending)
		return nil, nil, ErrPending
	}
	a.pending++
	deadline, _ := a.ctx.Deadline()
	ctx, cancel := context.WithDeadline(ctx, deadline)
	stop := context.AfterFunc(a.ctx, cancel)
	return &readerCall{attempt: a, ctx: ctx, cancel: cancel, stop: stop}, c.reader, nil
}

type readerCall struct {
	attempt *Attempt
	ctx     context.Context
	cancel  context.CancelFunc
	stop    func() bool
}

func (r *readerCall) finishReader(err error) {
	// Preserve cancellation even if a backend incorrectly returns success.
	err = errors.Join(err, r.ctx.Err())
	r.stop()
	r.cancel()
	a := r.attempt
	a.owner.mu.Lock()
	a.pending--
	if err != nil && a.phase == evaluating {
		a.poison = errors.Join(a.poison, err)
	}
	a.owner.mu.Unlock()
	a.gate <- struct{}{}
}
