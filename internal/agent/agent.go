package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type RuntimeTransport interface {
	Enroll(context.Context, string) error
	Publish(context.Context, domain.ObservationBatch) error
	Watch(context.Context, func(context.Context, domain.Snapshot) error) error
}

// ObserveFunc runs the local observer with durable sequence reservation and
// authenticated publication. It must stop when ctx is canceled.
type ObserveFunc func(context.Context, func(context.Context) (uint64, error), func(context.Context, domain.ObservationBatch) error) error

// Agent couples a single fresh enrollment to observation and snapshot reception.
// Receive must verify policy signatures and apply atomically before returning.
// Neither the coordinator nor its transport grants routing authority.
type Agent struct {
	State        *Store
	Transport    RuntimeTransport
	Identity     domain.AgentIdentity
	EnrollmentID string
	Observe      ObserveFunc
	Receive      func(context.Context, domain.Snapshot) error
	running      atomic.Bool
	retryMin     time.Duration
}

func (a *Agent) Run(ctx context.Context) error {
	if !a.running.CompareAndSwap(false, true) {
		return ErrClosed
	}
	defer a.running.Store(false)
	if a.State == nil {
		return ErrClosed
	}
	a.State.Deactivate()
	defer a.State.Deactivate()
	parsed, err := workloadidentity.ParseAgentURI(a.Identity.URI)
	ticket, ticketErr := uuid.Parse(a.EnrollmentID)
	if err != nil || parsed != a.Identity || ticketErr != nil || ticket.String() != a.EnrollmentID || a.Transport == nil || a.Observe == nil || a.Receive == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Transport.Enroll(ctx, a.EnrollmentID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.State.Activate(a.Identity); err != nil {
		return err
	}
	run, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	results := make(chan error, 2)
	publish := func(ctx context.Context, batch domain.ObservationBatch) error {
		if batch.Agent != a.Identity {
			return runtimecatalog.ErrUnauthorized
		}
		err := a.Transport.Publish(ctx, batch)
		if errors.Is(err, ErrClosed) {
			cancel(err)
			return runtimecatalog.ErrUnauthorized
		}
		if terminal(err) {
			cancel(err)
		}
		return err
	}
	go func() { results <- a.Observe(run, a.State.NextSequence, publish) }()
	go func() { results <- a.watch(run) }()
	first := <-results
	if first == nil {
		first = errors.New("agent component stopped")
	}
	cancel(first)
	<-results
	return context.Cause(run)
}
func terminal(err error) bool {
	return errors.Is(err, ErrClosed) || errors.Is(err, runtimecatalog.ErrUnauthorized) || errors.Is(err, runtimecatalog.ErrInvalid) || errors.Is(err, runtimecatalog.ErrLimit) || errors.Is(err, runtimecatalog.ErrStale)
}
func (a *Agent) watch(ctx context.Context) error {
	minimum := time.Second
	if a.retryMin > 0 {
		minimum = a.retryMin
	}
	delay := minimum
	for {
		var applyErr error
		received := false
		err := a.Transport.Watch(ctx, func(delivery context.Context, snapshot domain.Snapshot) error {
			applyErr = a.Receive(delivery, snapshot)
			if applyErr != nil {
				return fmt.Errorf("apply agent snapshot: %w", applyErr)
			}
			received = true
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if applyErr != nil {
			if terminal(applyErr) || (!errors.Is(applyErr, access.ErrSnapshot) && !errors.Is(applyErr, access.ErrPolicy) && !errors.Is(applyErr, access.ErrPolicyReplay)) {
				return applyErr
			}
			// Rejected policy cannot extend the installed lease. Reconnect for a
			// fresh snapshot while the receiver keeps its bounded previous state.
			err = ErrUnavailable
		}
		if terminal(err) {
			return err
		}
		var network net.Error
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &network) {
			return err
		}
		if received {
			delay = minimum
		}
		// Bound retries even if the peer closes every stream immediately.
		jitter, randomErr := rand.Int(rand.Reader, big.NewInt(int64(delay/4)+1))
		if randomErr != nil {
			return randomErr
		}
		timer := time.NewTimer(delay + time.Duration(jitter.Int64()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 10*time.Second)
	}
}
