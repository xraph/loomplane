package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

type logicalRecoveryScanFault struct {
	recovery.Store
	err error
}

func (s logicalRecoveryScanFault) Pending(context.Context) ([]recovery.Backup, error) {
	return nil, s.err
}

type nativeRecoveryCopyScan struct{ recovery.NativeCopyWorkStore }

func (nativeRecoveryCopyScan) PendingNativeRestores(context.Context, recovery.NativeCopyPage) ([]recovery.NativeRestore, error) {
	return []recovery.NativeRestore{{OrgID: "org", ID: "copy"}}, nil
}

type nativeRecoveryCleanupScan struct {
	recovery.NativeCleanupWorkStore
}

func (nativeRecoveryCleanupScan) PendingNativeCleanups(context.Context, recovery.NativeCopyPage) ([]recovery.NativeRestore, error) {
	return []recovery.NativeRestore{{OrgID: "org", ID: "expired-copy"}}, nil
}

func TestRecoveryReconciliationPreservesLogicalErrorsAndNativeProgress(t *testing.T) {
	logicalFailure, copyFailure := errors.New("logical store unavailable"), errors.New("copy queue unavailable")
	copyCalls, cleanupCalls := 0, 0
	e := &Extension{
		backups: &recovery.Service{Store: logicalRecoveryScanFault{err: logicalFailure}},
		nativeBackups: &recovery.NativeService{
			Copies: nativeRecoveryCopyScan{}, Cleanups: nativeRecoveryCleanupScan{},
			EnqueueCopy: func(_ context.Context, org, id string) error {
				copyCalls++
				if org != "org" || id != "copy" {
					t.Fatal("copy enqueue lost scope", org, id)
				}
				return copyFailure
			},
			EnqueueCleanup: func(_ context.Context, org, id string) error {
				cleanupCalls++
				if org != "org" || id != "expired-copy" {
					t.Fatal("cleanup enqueue lost scope", org, id)
				}
				return nil
			},
		},
	}
	worker, ok := any(e).(interface{ reconcileRecovery(context.Context) error })
	if !ok {
		t.Fatal("production recovery reconciliation has no native coordinator")
	}
	err := worker.reconcileRecovery(t.Context())
	if !errors.Is(err, logicalFailure) || !errors.Is(err, copyFailure) || copyCalls != 1 || cleanupCalls != 1 {
		t.Fatal("recovery scan hid a failure or starved another queue", err, copyCalls, cleanupCalls)
	}
}
