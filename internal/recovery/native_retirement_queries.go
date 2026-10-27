package recovery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) ListNativeRetirements(ctx context.Context, org, source string, page NativePageInput) (*NativeRetirementPage, error) {
	if _, err := s.GetNativeSource(ctx, org, source); err != nil {
		return nil, err
	}
	limit, err := page.pageSize()
	if err != nil {
		return nil, err
	}
	scope := ` FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2`
	query := `SELECT ` + nativeRetirementColumns + scope
	args := []any{org, source}
	if page.Before != "" {
		var at time.Time
		if err = s.db.QueryRow(ctx, `SELECT created_at`+scope+` AND id=$3`, org, source, page.Before).Scan(&at); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		query += ` AND (created_at,id)<($3,$4)`
		args = append(args, at, page.Before)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ` + nativeLimitPlaceholder(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &NativeRetirementPage{Retirements: []NativeRetirement{}}
	for rows.Next() {
		r, readErr := scanNativeRetirement(rows)
		if readErr != nil {
			return nil, readErr
		}
		out.Retirements = append(out.Retirements, *r)
	}
	if readErr := rows.Err(); readErr != nil {
		return nil, readErr
	}
	if len(out.Retirements) > limit {
		out.Retirements = out.Retirements[:limit]
		out.NextBefore = out.Retirements[limit-1].ID
	}
	return out, nil
}
