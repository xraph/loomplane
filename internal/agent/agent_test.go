package agent

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type sessionClient struct {
	enrollError     error
	enrolls         atomic.Int32
	watches         atomic.Int32
	acknowledge     func(context.Context, domain.Acknowledgment) error
	acknowledgments atomic.Int32
	watch           func(context.Context, func(context.Context, domain.Snapshot) error) error
}

func (c *sessionClient) Enroll(context.Context, string) error                   { c.enrolls.Add(1); return c.enrollError }
func (c *sessionClient) Publish(context.Context, domain.ObservationBatch) error { return nil }
func (c *sessionClient) Acknowledge(ctx context.Context, ack domain.Acknowledgment) error {
	c.acknowledgments.Add(1)
	if c.acknowledge != nil {
		return c.acknowledge(ctx, ack)
	}
	return nil
}
func (c *sessionClient) Watch(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
	c.watches.Add(1)
	if c.watch != nil {
		return c.watch(ctx, receive)
	}
	<-ctx.Done()
	return ctx.Err()
}
func sessionStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "agent", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
func TestAgentRequiresFreshEnrollmentBeforeObservation(t *testing.T) {
	_, _, identity := transportFixture(t)
	for _, denied := range []bool{true, false} {
		t.Run(map[bool]string{true: "denied", false: "accepted"}[denied], func(t *testing.T) {
			store := sessionStore(t)
			client := &sessionClient{}
			if denied {
				client.enrollError = ErrClosed
			}
			stop := errors.New("observer stopped")
			observations := 0
			a := &Agent{State: store, Transport: client, Identity: identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Receive: func(context.Context, domain.Snapshot) (domain.Acknowledgment, error) {
				return domain.Acknowledgment{}, nil
			}, Observe: func(ctx context.Context, next func(context.Context) (uint64, error), publish func(context.Context, domain.ObservationBatch) error) error {
				observations++
				sequence, err := next(ctx)
				if err != nil || sequence != 1 {
					t.Fatalf("sequence before authenticated activation: %d %v", sequence, err)
				}
				if err := publish(ctx, domain.ObservationBatch{Agent: identity, Sequence: sequence}); err != nil {
					return err
				}
				return stop
			}}
			err := a.Run(t.Context())
			if denied && (!errors.Is(err, ErrClosed) || observations != 0) {
				t.Fatalf("observation before enrollment: %d %v", observations, err)
			}
			if !denied && (!errors.Is(err, stop) || observations != 1) {
				t.Fatalf("enrolled observation: %d %v", observations, err)
			}
			if client.enrolls.Load() != 1 {
				t.Fatal("bootstrap replayed")
			}
			if _, err := store.NextSequence(t.Context()); !errors.Is(err, ErrClosed) {
				t.Fatalf("stopped process retained authority: %v", err)
			}
		})
	}
}
func TestAgentReconnectDoesNotReplayEnrollment(t *testing.T) {
	_, _, identity := transportFixture(t)
	store := sessionStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := &sessionClient{}
	client.watch = func(context.Context, func(context.Context, domain.Snapshot) error) error {
		if client.watches.Load() == 1 {
			return io.EOF
		}
		cancel()
		return context.Canceled
	}
	a := &Agent{State: store, Transport: client, Identity: identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", retryMin: time.Millisecond, Receive: func(context.Context, domain.Snapshot) (domain.Acknowledgment, error) {
		return domain.Acknowledgment{}, nil
	}, Observe: func(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
	if client.enrolls.Load() != 1 || client.watches.Load() != 2 {
		t.Fatalf("reconnect reenrolled: enroll=%d watch=%d", client.enrolls.Load(), client.watches.Load())
	}
}

func TestAgentApplicationFailureStopsObservation(t *testing.T) {
	_, _, identity := transportFixture(t)
	state := sessionStore(t)
	stop := errors.New("snapshot signature rejected")
	client := &sessionClient{watch: func(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
		return receive(ctx, domain.Snapshot{})
	}}
	observerStopped := make(chan struct{})
	a := &Agent{State: state, Transport: client, Identity: identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Receive: func(context.Context, domain.Snapshot) (domain.Acknowledgment, error) {
		return domain.Acknowledgment{}, stop
	}, Observe: func(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
		<-ctx.Done()
		close(observerStopped)
		return ctx.Err()
	}}
	if err := a.Run(t.Context()); !errors.Is(err, stop) {
		t.Fatalf("apply error swallowed: %v", err)
	}
	select {
	case <-observerStopped:
	default:
		t.Fatal("observer leaked after watch failure")
	}
	if client.watches.Load() != 1 {
		t.Fatal("invalid snapshot retried")
	}
	if _, err := state.NextSequence(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal("application failure left agent active")
	}
}
