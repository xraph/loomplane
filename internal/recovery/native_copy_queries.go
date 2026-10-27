package recovery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) GetNativeRestoreByRequest(ctx context.Context, org, project, request string) (*NativeRestore, error) {
	return scanNativeRestore(s.db.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, org, project, request))
}

func (s *PgStore) ListNativeCopies(ctx context.Context, org, project string, page NativePageInput) (*NativeCopiesPage, error) {
	limit, err := page.pageSize()
	if err != nil {
		return nil, err
	}
	scope := ` FROM loomplane_native_restores WHERE org_id=$1 AND project_id=$2 AND ($3='' OR environment_id=$3) AND ($4='' OR source->>'name'=$4)`
	query := `SELECT ` + nativeRestoreColumns + scope
	args := []any{org, project, page.EnvironmentID, page.Resource}
	if page.Before != "" {
		var created time.Time
		if err = s.db.QueryRow(ctx, `SELECT created_at`+scope+` AND id=$5`, append(args, page.Before)...).Scan(&created); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		query += ` AND (created_at,id)<($5,$6)`
		args = append(args, created, page.Before)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ` + nativeLimitPlaceholder(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &NativeCopiesPage{Copies: []NativeRestore{}}
	for rows.Next() {
		r, readErr := scanNativeRestore(rows)
		if readErr != nil {
			return nil, readErr
		}
		out.Copies = append(out.Copies, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Copies) > limit {
		out.Copies = out.Copies[:limit]
		out.NextBefore = out.Copies[limit-1].ID
	}
	return out, nil
}
