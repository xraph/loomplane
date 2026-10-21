package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func waitObserver(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestAgentAcknowledgesOnlyAppliedSnapshot(t *testing.T) {
	h := newReceiverHarness(t)
	good := h.snapshot(t, 2)
	bad := h.snapshot(t, 1)
	bad.Signature[0] ^= 1
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client := &sessionClient{}
	client.watch = func(delivery context.Context, receive func(context.Context, domain.Snapshot) error) error {
		if client.watches.Load() == 1 {
			return receive(delivery, bad)
		}
		return receive(delivery, good)
	}
	client.acknowledge = func(delivery context.Context, ack domain.Acknowledgment) error {
		if ack.Agent != h.identity || ack.Sequence != good.Sequence || ack.Revision != good.Revision {
			t.Fatal("ack did not identify installed snapshot")
		}
		session, err := h.receiver.Open(delivery, h.request)
		if err != nil {
			t.Fatal("ack preceded application", err)
		}
		session.Close()
		cancel()
		return nil
	}
	a := &Agent{State: h.store, Identity: h.identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Transport: client, Receive: h.receiver.Apply, Observe: waitObserver, retryMin: time.Millisecond}
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if client.acknowledgments.Load() != 1 || client.watches.Load() != 2 || client.enrolls.Load() != 1 {
		t.Fatal("invalid policy was acknowledged or enrollment replayed")
	}
}

func TestAgentAcknowledgmentFailurePreservesOnlyCurrentLease(t *testing.T) {
	for _, failure := range []error{ErrUnavailable, ErrAcknowledgmentStale, ErrClosed} {
		t.Run(failure.Error(), func(t *testing.T) {
			h := newReceiverHarness(t)
			first, second := h.snapshot(t, 1), h.snapshot(t, 2)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			client := &sessionClient{}
			var session *access.Session
			client.watch = func(delivery context.Context, receive func(context.Context, domain.Snapshot) error) error {
				if client.watches.Load() == 1 {
					return receive(delivery, first)
				}
				if session == nil || session.Context().Err() != nil {
					t.Fatal("receipt outage discarded unexpired policy")
				}
				return receive(delivery, second)
			}
			client.acknowledge = func(_ context.Context, _ domain.Acknowledgment) error {
				if client.acknowledgments.Load() == 1 {
					var err error
					session, err = h.receiver.Open(ctx, h.request)
					if err != nil {
						return err
					}
					return failure
				}
				cancel()
				return nil
			}
			a := &Agent{State: h.store, Identity: h.identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Transport: client, Receive: h.receiver.Apply, Observe: waitObserver, retryMin: time.Millisecond}
			err := a.Run(ctx)
			if errors.Is(failure, ErrClosed) {
				if !errors.Is(err, ErrClosed) || client.watches.Load() != 1 {
					t.Fatal("revoked acknowledgment retried", err)
				}
			} else if !errors.Is(err, context.Canceled) || client.watches.Load() != 2 {
				t.Fatal("receipt outage stopped enrollment", err)
			}
			if session == nil || session.Context().Err() == nil {
				t.Fatal("agent exit retained session")
			}
		})
	}
}

func TestAgentRejectsMismatchedApplicationReceipt(t *testing.T) {
	h := newReceiverHarness(t)
	snapshot := h.snapshot(t, 1)
	client := &sessionClient{watch: func(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
		return receive(ctx, snapshot)
	}}
	a := &Agent{State: h.store, Identity: h.identity, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Transport: client, Receive: func(ctx context.Context, s domain.Snapshot) (domain.Acknowledgment, error) {
		ack, err := h.receiver.Apply(ctx, s)
		ack.Sequence++
		return ack, err
	}, Observe: waitObserver}
	if err := a.Run(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal("mismatched apply receipt accepted", err)
	}
	if client.acknowledgments.Load() != 0 {
		t.Fatal("unrelated receipt sent")
	}
}
