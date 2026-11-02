package recovery

import (
	"context"
	"errors"
	"testing"
)

type nativeReconcileCopyFault struct {
	NativeCopyWorkStore
	err error
}

func (s nativeReconcileCopyFault) PendingNativeRestores(context.Context, NativeCopyPage) ([]NativeRestore, error) {
	return nil, s.err
}

type nativeReconcileCleanupPage struct {
	NativeCleanupWorkStore
}

func (nativeReconcileCleanupPage) PendingNativeCleanups(context.Context, NativeCopyPage) ([]NativeRestore, error) {
	return []NativeRestore{{OrgID: "org", ID: "expired-copy"}}, nil
}

func TestNativeReconciliationKeepsCleanupMovingWhenCopyScanFails(t *testing.T) {
	copyFailure, cleanupFailure := errors.New("copy scan unavailable"), errors.New("cleanup queue unavailable")
	cleanupCalls := 0
	s := &NativeService{
		Copies: nativeReconcileCopyFault{err: copyFailure}, Cleanups: nativeReconcileCleanupPage{},
		EnqueueCopy: func(context.Context, string, string) error {
			t.Fatal("failed scan produced a copy job")
			return nil
		},
		EnqueueCleanup: func(_ context.Context, org, id string) error {
			cleanupCalls++
			if org != "org" || id != "expired-copy" {
				t.Fatal("cleanup lost its scope", org, id)
			}
			return cleanupFailure
		},
	}
	worker, ok := any(s).(interface{ Reconcile(context.Context) error })
	if !ok {
		t.Fatal("native reconciliation has no production entry point")
	}
	err := worker.Reconcile(t.Context())
	if !errors.Is(err, copyFailure) || !errors.Is(err, cleanupFailure) || cleanupCalls != 1 {
		t.Fatal("copy scan failure hid or starved cleanup", err, cleanupCalls)
	}
}
