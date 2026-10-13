package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const sourceColumns = `id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at, removed_at`

func scanSource(row pgx.Row) (*Source, error) {
	var s Source
	if err := row.Scan(&s.ID, &s.OrgID, &s.ProjectID, &s.Alias, &s.Repository, &s.Root, &s.Ref,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.RemovedAt); err != nil {
		return nil, err
	}
	s.normalize()
	return &s, nil
}

// CreateSource implements Store. The unique constraint on (project_id,
// alias) covers removed rows too, which is what keeps an alias from being
// reused.
func (s *PgStore) CreateSource(ctx context.Context, src *Source) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_project_sources (`+sourceColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		src.ID, src.OrgID, src.ProjectID, src.Alias, src.Repository, src.Root, src.Ref,
		src.CreatedBy, src.CreatedAt, src.UpdatedAt, src.RemovedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrAliasTaken
	}
	if err != nil {
		return fmt.Errorf("insert source: %w", err)
	}
	return nil
}

// GetSource implements Store.
func (s *PgStore) GetSource(ctx context.Context, orgID, projectID, alias string) (*Source, error) {
	src, err := scanSource(s.db.QueryRow(ctx, `SELECT `+sourceColumns+` FROM loomplane_project_sources
		WHERE org_id = $1 AND project_id = $2 AND alias = $3`, orgID, projectID, alias))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get source: %w", err)
	}
	return src, nil
}

// ListSources implements Store.
func (s *PgStore) ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sourceColumns+` FROM loomplane_project_sources
		WHERE org_id = $1 AND project_id = $2 AND ($3 OR removed_at IS NULL) ORDER BY alias`, orgID, projectID, withRemoved)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

// SourcesByRepository lists the live sources in any organization that
// build from repository ("owner/name"), whatever its case, for a push
// webhook. It's the one query that crosses organizations, and only the
// webhook, which GitHub signs, uses it; the webhook narrows it to the
// organizations the push may reach.
func (s *PgStore) SourcesByRepository(ctx context.Context, repository string) ([]*Source, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sourceColumns+` FROM loomplane_project_sources
		WHERE lower(repository) = lower($1) AND removed_at IS NULL ORDER BY created_at, id`, repository)
	if err != nil {
		return nil, fmt.Errorf("find sources by repository: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find sources by repository: %w", err)
	}
	return out, nil
}

// UpdateSource implements Store.
func (s *PgStore) UpdateSource(ctx context.Context, src *Source) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_project_sources SET root = $4, ref = $5, updated_at = $6
		WHERE org_id = $1 AND project_id = $2 AND alias = $3 AND removed_at IS NULL`,
		src.OrgID, src.ProjectID, src.Alias, src.Root, src.Ref, src.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	return oneSource(res)
}

// RemoveSource implements Store.
func (s *PgStore) RemoveSource(ctx context.Context, orgID, projectID, alias string, now time.Time) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_project_sources SET removed_at = $4, updated_at = $4
		WHERE org_id = $1 AND project_id = $2 AND alias = $3 AND removed_at IS NULL`, orgID, projectID, alias, now)
	if err != nil {
		return fmt.Errorf("remove source: %w", err)
	}
	return oneSource(res)
}

func oneSource(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrSourceNotFound
	}
	return nil
}
