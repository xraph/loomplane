package manifests

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/pkg/manifest"
)

const pgUniqueViolation = "23505"

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const revisionColumns = `id, org_id, project_id, revision, body, digest, created_by, created_at`

func scanRevision(row pgx.Row) (*Revision, error) {
	var r Revision
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.Revision, &r.YAML, &r.Digest, &r.CreatedBy, &r.CreatedAt); err != nil {
		return nil, err
	}
	// Every stored body was parsed before it was saved. One that no longer
	// parses (a later Loomplane tightening a rule, say) still reads, with
	// no parsed manifest, so its history stays visible.
	if m, err := manifest.Parse([]byte(r.YAML)); err == nil {
		r.Manifest = m
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

// CreateRevision implements Store. The insert numbers the revision itself,
// and the unique (project, revision) constraint turns a race into
// ErrConflict.
func (s *PgStore) CreateRevision(ctx context.Context, r *Revision) error {
	err := s.db.QueryRow(ctx, `INSERT INTO loomplane_manifests (`+revisionColumns+`)
		SELECT $1, $2, $3, COALESCE(MAX(revision), 0) + 1, $4, $5, $6, $7
		FROM loomplane_manifests WHERE project_id = $3
		RETURNING revision`,
		r.ID, r.OrgID, r.ProjectID, r.YAML, r.Digest, r.CreatedBy, r.CreatedAt).Scan(&r.Revision)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("insert manifest: %w", err)
	}
	return nil
}

// CurrentRevision implements Store.
func (s *PgStore) CurrentRevision(ctx context.Context, orgID, projectID string) (*Revision, error) {
	r, err := scanRevision(s.db.QueryRow(ctx, `SELECT `+revisionColumns+` FROM loomplane_manifests
		WHERE org_id = $1 AND project_id = $2 ORDER BY revision DESC LIMIT 1`, orgID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get manifest: %w", err)
	}
	return r, nil
}

// GetRevision implements Store.
func (s *PgStore) GetRevision(ctx context.Context, orgID, id string) (*Revision, error) {
	r, err := scanRevision(s.db.QueryRow(ctx, `SELECT `+revisionColumns+` FROM loomplane_manifests
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get manifest: %w", err)
	}
	return r, nil
}

// ListRevisions implements Store.
func (s *PgStore) ListRevisions(ctx context.Context, orgID, projectID string, limit int) ([]*Revision, error) {
	rows, err := s.db.Query(ctx, `SELECT `+revisionColumns+` FROM loomplane_manifests
		WHERE org_id = $1 AND project_id = $2 ORDER BY revision DESC LIMIT $3`, orgID, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list manifests: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan manifest: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list manifests: %w", err)
	}
	return out, nil
}
