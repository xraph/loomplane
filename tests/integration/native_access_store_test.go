//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recovery"
)

type nativeAccessRecords interface {
	WithNativeAccess(context.Context, string, string, func(context.Context, recovery.NativeRestore) error) error
}

func TestNativeCredentialAccessFencesCleanupAndRequiresCurrentProof(t *testing.T) {
	f, admission, request, digest := nativeAdmissionFixture(t)
	access, ok := any(f.store).(nativeAccessRecords)
	if !ok {
		t.Fatal("native credentials have no scoped access fence against cleanup")
	}
	if _, err := admission.CreateReviewedNativeRestore(t.Context(), request, digest); err != nil {
		t.Fatal(err)
	}
	calls := 0
	inspect := func(ctx context.Context, r recovery.NativeRestore) error {
		calls++
		if r.OrgID != "org" || r.ID != request.ID || r.PhysicalID != "copy-physical" || r.VerifiedAt == nil {
			t.Fatal("access lost its retained SQL proof")
		}
		return ctx.Err()
	}
	if err := access.WithNativeAccess(t.Context(), "org", request.ID, inspect); !errors.Is(err, recovery.ErrConflict) || calls != 0 {
		t.Fatal("queued copy exposed credential access", err)
	}
	claimed, err := f.store.ClaimNativeRestore(t.Context(), "org", request.ID, "copy-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishNativeRestore(t.Context(), claimed, recovery.NativeRestoreResult{ExternalID: "target-project:us-central1:copy", PhysicalID: "copy-physical", SQLVerified: true}, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if err = access.WithNativeAccess(t.Context(), "foreign", request.ID, inspect); !errors.Is(err, recovery.ErrNotFound) || calls != 0 {
		t.Fatal("foreign organization entered access fence", err)
	}
	for _, query := range []string{
		`UPDATE loomplane_native_restores SET verified_at=NULL WHERE id=$1`,
		`UPDATE loomplane_native_restores SET verified_at=now()+interval '1 minute' WHERE id=$1`,
		`UPDATE loomplane_native_restores SET created_at=now()-interval '2 hours',verified_at=now()-interval '1 minute',expires_at=now()-interval '1 second' WHERE id=$1`,
		`UPDATE loomplane_native_restores SET expires_at=now()+interval '1 hour',cleanup_by='administrator',cleanup_requested_at=now() WHERE id=$1`,
	} {
		if _, err = f.sql.Exec(t.Context(), query, request.ID); err != nil {
			t.Fatal(err)
		}
		if err = access.WithNativeAccess(t.Context(), "org", request.ID, inspect); !errors.Is(err, recovery.ErrConflict) || calls != 0 {
			t.Fatal("missing proof, expiry or cleanup allowed access", err)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET verified_at=now(),expires_at=$2,cleanup_by='',cleanup_requested_at=NULL WHERE id=$1`, request.ID, request.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- access.WithNativeAccess(t.Context(), "org", request.ID, func(ctx context.Context, r recovery.NativeRestore) error {
			close(entered)
			select {
			case <-release:
				return inspect(ctx, r)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	defer close(release)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("access fence never entered")
	}
	cleanupCtx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	_, err = f.store.RequestNativeCleanup(cleanupCtx, "org", request.ID, "administrator", func(context.Context) error { return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cleanup crossed active credential access fence", err)
	}
	release <- struct{}{}
	if err = <-finished; err != nil || calls != 1 {
		t.Fatal("valid access did not complete", err)
	}
	if _, err = f.store.RequestNativeCleanup(t.Context(), "org", request.ID, "administrator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = access.WithNativeAccess(t.Context(), "org", request.ID, inspect); !errors.Is(err, recovery.ErrConflict) || calls != 1 {
		t.Fatal("cleanup admission retained credential access", err)
	}
}
