package recovery

import (
	"context"
	"time"
)

func (s *PgStore) CheckNativeLease(ctx context.Context, r *NativeRestore, status string) error {
	if status != "running" && status != "deleting" {
		return ErrInvalid
	}
	if r == nil || r.OrgID == "" || r.ID == "" || r.Token == "" {
		return ErrConflict
	}
	var live bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_native_restores WHERE org_id=$1 AND id=$2 AND token=$3 AND status=$4 AND lease_until>now() AND ($4='deleting' OR expires_at>now()))`, r.OrgID, r.ID, r.Token, status).Scan(&live); err != nil {
		return err
	}
	if !live {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) PendingNativeRestores(ctx context.Context, page NativeCopyPage) ([]NativeRestore, error) {
	if page.Through.IsZero() {
		page.Through = time.Now().UTC()
	}
	rows, err := s.db.Query(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE expires_at>now() AND (status='queued' OR status='running' AND lease_until<now()) AND created_at<=$1 AND (created_at,id)>($2,$3) ORDER BY created_at,id LIMIT $4`, page.Through, page.AfterCreated, page.AfterID, nativeCopyPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NativeRestore{}
	for rows.Next() {
		r, scanErr := scanNativeRestore(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *r)
	}
	return items, rows.Err()
}
