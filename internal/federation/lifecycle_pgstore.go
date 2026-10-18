package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// List reads only the selected tenant/project/environment using a stable ID cursor.
func (s *PgStore) List(ctx context.Context, scope domain.Scope, after string, limit int) ([]*domain.Destination, error) {
	if limit < 1 || limit > 101 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND id>$4 ORDER BY id LIMIT $5`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, after, limit)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*domain.Destination{}
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError(err)
	}
	return out, nil
}

// Rename increments the generation so stale management requests cannot overwrite it.
func (s *PgStore) Rename(ctx context.Context, orgID, id, name string, generation int64, now time.Time) (*domain.Destination, error) {
	d, err := scanDestination(s.db.QueryRow(ctx, `UPDATE loomplane_federation_destinations SET name=$3,generation=generation+1,updated_at=$4 WHERE org_id=$1 AND id=$2 AND generation=$5 RETURNING `+columns, orgID, id, name, now, generation))
	if errors.Is(err, ErrNotFound) {
		return nil, s.missingOrConflict(ctx, orgID, id)
	}
	return d, err
}

// Delete is fenced by generation and all restrictive destination references.
// Connection cleanup/trust references are independent and survive alias removal.
func (s *PgStore) Delete(ctx context.Context, orgID, id string, generation int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 AND generation=$3`, orgID, id, generation)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrInUse
		}
		return storeError(err)
	}
	count, err := tag.RowsAffected()
	if err != nil {
		return fmt.Errorf("unregister destination count: %w", err)
	}
	if count != 1 {
		return s.missingOrConflict(ctx, orgID, id)
	}
	return nil
}

func (s *PgStore) missingOrConflict(ctx context.Context, orgID, id string) error {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return err
	}
	return ErrConflict
}
