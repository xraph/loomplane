package recovery

import (
	"context"
	"time"
)

// NativeAccessStore holds a shared row lock while credentials are checked.
// Cleanup admission and worker claims need an exclusive lock on the same row.
type NativeAccessStore interface {
	WithNativeAccess(context.Context, string, string, func(context.Context, NativeRestore) error) error
}

func nativeAccessEligible(r NativeRestore, now time.Time) bool {
	return r.Status == "succeeded" && r.VerifiedAt != nil && !r.VerifiedAt.Before(r.CreatedAt) && !r.VerifiedAt.After(now) && r.ExpiresAt.After(now) && r.ExpiresAt.Sub(r.CreatedAt) <= 24*time.Hour && r.CleanupRequestedAt == nil && r.CleanupBy == "" && r.ExternalID != "" && r.PhysicalID != "" && nativeCopyScope(r) == nil
}

func (s *PgStore) WithNativeAccess(ctx context.Context, org, id string, inspect func(context.Context, NativeRestore) error) error {
	if org == "" || id == "" || inspect == nil {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	r, err := scanNativeRestore(tx.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND id=$2 FOR SHARE`, org, id))
	if err != nil {
		return err
	}
	if !nativeAccessEligible(*r, time.Now()) {
		return ErrConflict
	}
	if inspectionErr := inspect(ctx, copyNativeRestore(*r)); inspectionErr != nil {
		return inspectionErr
	}
	if !nativeAccessEligible(*r, time.Now()) {
		return ErrConflict
	}
	return tx.Commit()
}
