package cloudinfra

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/currentauthority"
)

type namespaceReceiptLifecycle struct {
	mu           sync.Mutex
	stopping     bool
	inflight     int
	changed      chan struct{}
	cleanupError error
	owned        map[*namespaceOwnedWork]context.CancelFunc
}

func (s *InstanceNamespaceTransactions) beginReceipt() error {
	s.receipts.mu.Lock()
	defer s.receipts.mu.Unlock()
	if s.receipts.stopping {
		return currentauthority.ErrUnavailable
	}
	if s.receipts.changed == nil {
		s.receipts.changed = make(chan struct{})
	}
	s.receipts.inflight++
	return nil
}
func (s *InstanceNamespaceTransactions) endReceipt(err error) {
	s.receipts.mu.Lock()
	defer s.receipts.mu.Unlock()
	s.receipts.inflight--
	if s.receipts.cleanupError == nil {
		s.receipts.cleanupError = err
	}
	close(s.receipts.changed)
	s.receipts.changed = make(chan struct{})
}

// Shutdown closes admission, cancels owned work and waits for connections and
// finalizers. A caller timeout retains ownership until those resources release.
func (s *InstanceNamespaceTransactions) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.receipts.mu.Lock()
	s.receipts.stopping = true
	cancels := make([]context.CancelFunc, 0, len(s.receipts.owned))
	for _, cancel := range s.receipts.owned {
		cancels = append(cancels, cancel)
	}
	s.receipts.mu.Unlock()
	// Cancellation callbacks may finish work and acquire the lifecycle lock.
	for _, cancel := range cancels {
		cancel()
	}
	for {
		s.receipts.mu.Lock()
		if s.receipts.inflight == 0 {
			err := s.receipts.cleanupError
			s.receipts.mu.Unlock()
			return err
		}
		changed := s.receipts.changed
		s.receipts.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return errors.Join(currentauthority.ErrBusy, ctx.Err())
		}
	}
}

// Drained reports released ownership after admission closes. A retained cleanup
// diagnostic does not mean the adapter still owns a connection.
func (s *InstanceNamespaceTransactions) Drained() bool {
	if s == nil {
		return true
	}
	s.receipts.mu.Lock()
	defer s.receipts.mu.Unlock()
	return s.receipts.stopping && s.receipts.inflight == 0
}

type namespaceOwnedWork byte

// A verification read can outlive its caller, but never the adapter that owns it.
func (s *InstanceNamespaceTransactions) beginNamespaceWork(ctx context.Context) (context.Context, func(), error) {
	owned, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.receipts.mu.Lock()
	if s.receipts.stopping {
		s.receipts.mu.Unlock()
		cancel()
		return nil, nil, currentauthority.ErrUnavailable
	}
	if s.receipts.changed == nil {
		s.receipts.changed = make(chan struct{})
	}
	if s.receipts.owned == nil {
		s.receipts.owned = make(map[*namespaceOwnedWork]context.CancelFunc)
	}
	work := new(namespaceOwnedWork)
	s.receipts.owned[work] = cancel
	s.receipts.inflight++
	s.receipts.mu.Unlock()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			cancel()
			s.receipts.mu.Lock()
			delete(s.receipts.owned, work)
			s.receipts.mu.Unlock()
			s.endReceipt(nil)
		})
	}
	return owned, finish, nil
}

// beginNamespaceCall retains the whole public call while preserving its caller's
// deadline, values and cancellation. Adapter shutdown cancels the same call.
func (s *InstanceNamespaceTransactions) beginNamespaceCall(ctx context.Context) (context.Context, func(), error) {
	if s == nil {
		return nil, nil, currentauthority.ErrUnavailable
	}
	owned, finish, err := s.beginNamespaceWork(ctx)
	if err != nil {
		return nil, nil, err
	}
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owned, cancel)
	return call, func() { stop(); cancel(); finish() }, nil
}

func (s *InstanceNamespaceTransactions) cleanupReceipt(conn driver.DedicatedConn) error {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), currentauthority.CleanupLimit)
		_, err := conn.Exec(ctx, "ROLLBACK")
		if errors.Is(err, pgconn.ErrConnClosed) {
			err = nil
		}
		cancel()
		conn.Release()
		s.endReceipt(err)
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), currentauthority.CleanupLimit)
	defer cancel()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.Join(currentauthority.ErrBusy, ctx.Err())
	}
}

// retainReceipt keeps cleanup ownership separate from the registered operation,
// which also owns any primary reconciliation after the connection is released.
func (s *InstanceNamespaceTransactions) retainReceipt() {
	s.receipts.mu.Lock()
	defer s.receipts.mu.Unlock()
	s.receipts.inflight++
}
