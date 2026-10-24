package recovery

import (
	"context"
	"encoding/json"
	"time"
)

// RefreshNativePITR preserves the source epoch without changing snapshot scan
// completeness. A failed read never makes old window bounds current again.
func (s *PgStore) RefreshNativePITR(ctx context.Context, org, id string, fetch NativePITRFetch) (*NativePoint, error) {
	if fetch == nil {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	if source.Provider != "rds" && source.Provider != "cloud-sql" {
		return nil, ErrInvalid
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	observed, err := fetch(fetchCtx, source)
	cancel()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if observed == nil || observed.Kind != "pitr" || observed.PITR == nil || observed.PITR.Protocol != 1 || observed.RestoreTime != nil || observed.ExpiresAt != nil || observed.Status != "available" || observed.ProviderID == "" || observed.Name == "" || observed.Engine == "" || observed.Version == "" || observed.CreatedAt.IsZero() || observed.PITR.Earliest.IsZero() || observed.PITR.Earliest.Before(observed.CreatedAt) || !observed.PITR.Earliest.Before(observed.PITR.Latest) || observed.PITR.Latest.After(now) || observed.SourceID != "" && observed.SourceID != id {
		return nil, ErrNativePITRUnavailable
	}
	point := cloneNativePoint(*observed)
	point.ID, point.SourceID, point.Present, point.LastSeenAt = "", id, true, now
	point.CreatedAt = point.CreatedAt.UTC()
	point.PITR.Earliest, point.PITR.Latest = point.PITR.Earliest.UTC(), point.PITR.Latest.UTC()
	raw, err := json.Marshal(point)
	if err != nil {
		return nil, err
	}
	saved, err := scanNativePoint(tx.QueryRow(ctx, `INSERT INTO loomplane_native_points(org_id,source_id,provider_id,created_at,body,last_seen_at) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(source_id,provider_id,creation_identity) DO UPDATE SET body=EXCLUDED.body,present=true,last_seen_at=EXCLUDED.last_seen_at WHERE loomplane_native_points.org_id=EXCLUDED.org_id
 RETURNING id,source_id,body,present,last_seen_at`, org, id, point.ProviderID, point.CreatedAt, raw, now))
	if err != nil {
		return nil, err
	}
	return saved, tx.Commit()
}
