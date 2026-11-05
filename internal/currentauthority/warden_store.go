package currentauthority

import (
	"context"
	"errors"

	"github.com/xraph/warden/store"
)

// NewStoreRouter preserves ordinary Store calls and owns only the coordinator's
// resources. Construction performs no SQL and does not enable current authority.
func NewStoreRouter(owner *Coordinator, ordinary store.Store) store.Store {
	return &storeRouter{owner: owner, ordinary: ordinary}
}

type storeRouter struct {
	owner    *Coordinator
	ordinary store.Store
}

var _ store.Store = (*storeRouter)(nil)

// Close never closes the shared main database behind the ordinary Store.
func (r *storeRouter) Close() error { return r.owner.Close() }

func routeStore[T any](ctx context.Context, r *storeRouter, read bool, absence error, invoke func(context.Context, store.Store) (T, error)) (value T, err error) {
	a, scoped, err := r.owner.readerScope(ctx)
	if !scoped {
		return invoke(ctx, r.ordinary)
	}
	if err != nil {
		return value, err
	}
	if !read {
		return value, a.fail(ErrReadOnly)
	}
	call, reader, err := a.beginReader(ctx)
	if err != nil {
		return value, err
	}
	completed := false
	defer func() {
		failure := err
		if !completed {
			failure = ErrPoisoned
		}
		if expectedAbsence(failure, absence) {
			failure = nil
		}
		call.finishReader(failure)
	}()
	value, err = invoke(call.ctx, reader)
	completed = true
	return value, err
}

var ErrReadOnly = errors.New("current authority Store is read-only")

// Only a single wrapping chain ending at this method's exact sentinel is an
// absence. Joined failures and custom Is methods cannot hide a backend error.
func expectedAbsence(err, sentinel error) bool {
	if sentinel == nil {
		return false
	}
	for range 64 {
		if err == nil {
			return false
		}
		if err == sentinel { //nolint:errorlint // Only exact identity may end this single unwrap chain.
			return true
		}
		next, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = next.Unwrap()
	}
	return false
}
