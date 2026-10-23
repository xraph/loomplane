package recovery

import (
	"context"
	"errors"
)

const nativeCleanupDue = `status='cleanup_queued' OR status IN ('queued','succeeded','failed') AND expires_at<=now() OR status='cleanup_failed' AND (lease_until IS NULL OR lease_until<=now()) OR status='deleting' AND lease_until<=now() OR status='running' AND expires_at<=now() AND lease_until<=now()`

func (s *PgStore) RequestNativeCleanup(ctx context.Context, org, id, actor string, guard func(context.Context) error) (*NativeRestore, error) {
	if org == "" || id == "" || actor == "" || guard == nil {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	r, err := scanNativeRestore(tx.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if r.Status == "deleted" || r.Status == "cleanup_queued" && r.CleanupBy == actor {
		return r, tx.Commit()
	}
	r, err = scanNativeRestore(tx.QueryRow(ctx, `UPDATE loomplane_native_restores SET status='cleanup_queued',cleanup_by=$3,cleanup_requested_at=now(),lease_until=NULL
 WHERE org_id=$1 AND id=$2 AND (status IN ('queued','succeeded','failed','cleanup_failed','cleanup_queued') OR status IN ('running','deleting') AND lease_until<=now()) RETURNING `+nativeRestoreColumns, org, id, actor))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return r, tx.Commit()
}

func (s *PgStore) PendingNativeCleanups(ctx context.Context, page NativeCopyPage) ([]NativeRestore, error) {
	return s.pendingNativeCopies(ctx, nativeCleanupDue, page)
}
