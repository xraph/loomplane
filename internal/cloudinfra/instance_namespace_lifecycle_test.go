package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/currentauthority"
)

type namespaceDelayedCleanup struct {
	driver.DedicatedConn
	started  chan struct{}
	finish   chan struct{}
	released chan struct{}
	failure  error
}

func (c *namespaceDelayedCleanup) Exec(context.Context, string, ...any) (driver.Result, error) {
	close(c.started)
	<-c.finish
	return nil, c.failure
}
func (c *namespaceDelayedCleanup) Release() { close(c.released) }

func TestNamespaceReceiptDrainRetainsLateCleanup(t *testing.T) {
	s := &InstanceNamespaceTransactions{}
	if err := s.beginReceipt(); err != nil {
		t.Fatal(err)
	}
	c := &namespaceDelayedCleanup{started: make(chan struct{}), finish: make(chan struct{}), released: make(chan struct{})}
	cleanup := make(chan error, 1)
	go func() { cleanup <- s.cleanupReceipt(c) }()
	<-c.started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	err := s.Shutdown(ctx)
	cancel()
	if !errors.Is(err, currentauthority.ErrBusy) {
		t.Fatal(err)
	}
	if err = s.beginReceipt(); !errors.Is(err, currentauthority.ErrUnavailable) {
		t.Fatal("shutdown admitted another receipt")
	}
	select {
	case <-c.released:
		t.Fatal("released connection before cleanup finished")
	default:
	}
	close(c.finish)
	if err = <-cleanup; err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceReceiptDrainRetainsCleanupError(t *testing.T) {
	s := &InstanceNamespaceTransactions{}
	if err := s.beginReceipt(); err != nil {
		t.Fatal(err)
	}
	expected := errors.New("cleanup failed")
	c := &namespaceDelayedCleanup{started: make(chan struct{}), finish: make(chan struct{}), released: make(chan struct{}), failure: expected}
	close(c.finish)
	if err := s.cleanupReceipt(c); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Shutdown(t.Context()); !errors.Is(err, expected) {
			t.Fatal(err)
		}
	}
}

func TestNamespaceReceiptDrainWaitsForReconciliationAfterCleanup(t *testing.T) {
	s := &InstanceNamespaceTransactions{}
	if err := s.beginReceipt(); err != nil {
		t.Fatal(err)
	}
	s.retainReceipt()
	c := &namespaceDelayedCleanup{started: make(chan struct{}), finish: make(chan struct{}), released: make(chan struct{})}
	close(c.finish)
	if err := s.cleanupReceipt(c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, currentauthority.ErrBusy) {
		t.Fatalf("shutdown released the in-flight reconciliation: %v", err)
	}
	s.endReceipt(nil)
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceOwnedWorkCancellationAndDrain(t *testing.T) {
	s := &InstanceNamespaceTransactions{}
	caller, cancelCaller := context.WithCancel(t.Context())
	owned, finish, err := s.beginNamespaceWork(caller)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	cancelCaller()
	if owned.Err() != nil {
		t.Fatal("caller cancellation abandoned commit verification")
	}
	if s.Drained() {
		t.Fatal("open adapter reported drained")
	}
	closing, cancel := context.WithCancel(t.Context())
	cancel()
	if err = s.Shutdown(closing); !errors.Is(err, currentauthority.ErrBusy) {
		t.Fatal(err)
	}
	if !errors.Is(owned.Err(), context.Canceled) {
		t.Fatal("expired shutdown did not cancel owned work")
	}
	if s.Drained() {
		t.Fatal("cancellation released ownership before work finished")
	}
	if _, _, err = s.beginNamespaceWork(t.Context()); !errors.Is(err, currentauthority.ErrUnavailable) {
		t.Fatal("closing adapter admitted work")
	}
	s.retainReceipt()
	finish()
	finish()
	if s.Drained() {
		t.Fatal("outer completion lost retained finalizer")
	}
	cleanupErr := errors.New("late cleanup failure")
	s.endReceipt(cleanupErr)
	for range 2 {
		if !s.Drained() {
			t.Fatal("released finalizer still owns resources")
		}
		if err = s.Shutdown(t.Context()); !errors.Is(err, cleanupErr) {
			t.Fatal(err)
		}
	}
}

func TestNamespaceOwnedWorkZeroWorkDrain(t *testing.T) {
	var absent *InstanceNamespaceTransactions
	if !absent.Drained() {
		t.Fatal("nil adapter owns resources")
	}
	s := &InstanceNamespaceTransactions{}
	if s.Drained() {
		t.Fatal("admission is still open")
	}
	if err := s.Shutdown(t.Context()); err != nil || !s.Drained() {
		t.Fatalf("empty drain: %v", err)
	}
	if _, _, err := s.beginNamespaceWork(t.Context()); !errors.Is(err, currentauthority.ErrUnavailable) {
		t.Fatal(err)
	}
}
