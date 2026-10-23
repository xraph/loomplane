package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const nativeSourceColumns = `id,org_id,project_id,environment_id,name,provider,connection_id,region,physical_id,captured_at,state,complete,checked_at,observed_at,error,source`

func scanNativeSource(row pgx.Row) (*NativeSource, error) {
	var source NativeSource
	var raw []byte
	err := row.Scan(&source.ID, &source.OrgID, &source.ProjectID, &source.EnvironmentID, &source.Name, &source.Provider, &source.ConnectionID, &source.Region, &source.PhysicalID, &source.CapturedAt, &source.State, &source.Complete, &source.CheckedAt, &source.ObservedAt, &source.Error, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &source.Binding); err != nil {
		return nil, err
	}
	b := source.Binding
	if b.OrgID != source.OrgID || b.ProjectID != source.ProjectID || b.EnvironmentID != source.EnvironmentID || b.Name != source.Name || b.Provider != source.Provider || b.ConnectionID != source.ConnectionID || b.Region != source.Region || b.PhysicalID == "" || b.PhysicalID != source.PhysicalID {
		return nil, ErrInvalid
	}
	return &source, nil
}
func (s *PgStore) GetNativeSource(ctx context.Context, org, id string) (*NativeSource, error) {
	return scanNativeSource(s.db.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2`, org, id))
}

func scanNativePoint(row pgx.Row) (*NativePoint, error) {
	var point NativePoint
	var raw []byte
	var id, source string
	var present bool
	var seen time.Time
	if err := row.Scan(&id, &source, &raw, &present, &seen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &point); err != nil {
		return nil, err
	}
	point.ID, point.SourceID, point.Present, point.LastSeenAt = id, source, present, seen.UTC()
	return &point, nil
}

func (s *PgStore) GetNativePoint(ctx context.Context, org, source, id string) (*NativePoint, error) {
	return scanNativePoint(s.db.QueryRow(ctx, `SELECT id,source_id,body,present,last_seen_at FROM loomplane_native_points WHERE org_id=$1 AND source_id=$2 AND id=$3`, org, source, id))
}
func (s *PgStore) ListNativeSources(ctx context.Context, org, project string, page NativePageInput) (*NativeSourcesPage, error) {
	limit, err := page.pageSize()
	if err != nil {
		return nil, err
	}
	scope := ` FROM loomplane_native_sources WHERE org_id=$1 AND project_id=$2 AND ($3='' OR environment_id=$3) AND ($4='' OR name=$4)`
	query := `SELECT ` + nativeSourceColumns + scope
	args := []any{org, project, page.EnvironmentID, page.Resource}
	if page.Before != "" {
		var captured time.Time
		if pageErr := s.db.QueryRow(ctx, `SELECT captured_at`+scope+` AND id=$5`, append(args, page.Before)...).Scan(&captured); pageErr != nil {
			if errors.Is(pageErr, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, pageErr
		}
		query += ` AND (captured_at,id)<($5,$6)`
		args = append(args, captured, page.Before)
	}
	// Limits have already been validated and remain parameters.
	query += ` ORDER BY captured_at DESC,id DESC LIMIT ` + nativeLimitPlaceholder(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &NativeSourcesPage{Sources: []NativeSource{}}
	for rows.Next() {
		source, err := scanNativeSource(rows)
		if err != nil {
			return nil, err
		}
		out.Sources = append(out.Sources, *source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Sources) > limit {
		out.Sources = out.Sources[:limit]
		out.NextBefore = out.Sources[limit-1].ID
	}
	return out, nil
}

func (s *PgStore) ReadNativeInventory(ctx context.Context, org, id string, page NativePageInput) (*NativeInventory, error) {
	limit, err := page.pageSize()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 AND ($3='' OR environment_id=$3) AND ($4='' OR name=$4) FOR SHARE`, org, id, page.EnvironmentID, page.Resource))
	if err != nil {
		return nil, err
	}
	query := `SELECT id,source_id,body,present,last_seen_at FROM loomplane_native_points WHERE org_id=$1 AND source_id=$2`
	args := []any{org, id}
	if page.Before != "" {
		var created string
		if pageErr := tx.QueryRow(ctx, `SELECT creation_identity FROM loomplane_native_points WHERE org_id=$1 AND source_id=$2 AND id=$3`, org, id, page.Before).Scan(&created); pageErr != nil {
			if errors.Is(pageErr, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, pageErr
		}
		query += ` AND (creation_identity,id)<($3,$4)`
		args = append(args, created, page.Before)
	}
	query += ` ORDER BY creation_identity DESC,id DESC LIMIT ` + nativeLimitPlaceholder(len(args)+1)
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &NativeInventory{Source: *source, Points: []NativePoint{}}
	for rows.Next() {
		point, err := scanNativePoint(rows)
		if err != nil {
			return nil, err
		}
		out.Points = append(out.Points, *point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Points) > limit {
		out.Points = out.Points[:limit]
		out.NextBefore = out.Points[limit-1].ID
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// RefreshNativeInventory serializes refreshes per source. Failed reads retain
// previous points. Only a complete scan can mark an earlier point absent.
func (s *PgStore) RefreshNativeInventory(ctx context.Context, org, id string, fetch NativeFetch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	points, complete, fetchErr := fetch(fetchCtx, source)
	cancel()
	seen := map[string]bool{}
	for _, point := range points {
		identity := point.ProviderID + "\x00" + point.CreatedAt.UTC().Format(time.RFC3339Nano)
		if point.ProviderID == "" || point.CreatedAt.IsZero() || point.Name == "" || point.Status == "" || seen[identity] {
			fetchErr = ErrNativeUnavailable
		}
		if point.ExpiresAt != nil && (point.ExpiresAt.IsZero() || !point.ExpiresAt.After(point.CreatedAt)) {
			fetchErr = ErrNativeUnavailable
		}
		seen[identity] = true
	}
	if len(points) > 1000 {
		fetchErr = ErrNativeUnavailable
	}
	now := time.Now().UTC()
	if fetchErr != nil {
		if _, err := tx.Exec(ctx, `UPDATE loomplane_native_sources SET state='failed',complete=false,checked_at=$3,error=$4 WHERE org_id=$1 AND id=$2`, org, id, now, ErrNativeUnavailable.Error()); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrNativeUnavailable
	}
	if complete {
		if _, err := tx.Exec(ctx, `UPDATE loomplane_native_points SET present=false WHERE org_id=$1 AND source_id=$2`, org, id); err != nil {
			return err
		}
	}
	for _, point := range points {
		point.ID, point.SourceID, point.Present, point.LastSeenAt = "", id, true, now
		point.CreatedAt = point.CreatedAt.UTC()
		if point.ExpiresAt != nil {
			utc := point.ExpiresAt.UTC()
			point.ExpiresAt = &utc
		}
		raw, err := json.Marshal(point)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_native_points(org_id,source_id,provider_id,created_at,body,last_seen_at) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(source_id,provider_id,creation_identity) DO UPDATE SET body=EXCLUDED.body,present=true,last_seen_at=EXCLUDED.last_seen_at WHERE loomplane_native_points.org_id=EXCLUDED.org_id`, org, id, point.ProviderID, point.CreatedAt, raw, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE loomplane_native_sources SET state='ready',complete=$3,checked_at=$4,observed_at=$4,error='' WHERE org_id=$1 AND id=$2`, org, id, complete, now); err != nil {
		return err
	}
	return tx.Commit()
}

func nativeLimitPlaceholder(index int) string { return "$" + strconv.Itoa(index) }
