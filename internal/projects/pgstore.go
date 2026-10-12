package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
)

// pgUniqueViolation is Postgres's SQLSTATE for a unique constraint failure.
const pgUniqueViolation = "23505"

// PgStore is the Postgres Store, over the grove pgdriver pool the grove
// extension owns. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const projectColumns = `id, org_id, name, slug, description, repository_url, status, created_by, created_at, updated_at`

// CreateProject inserts p. A slug already used in the organization is
// ErrSlugTaken.
func (s *PgStore) CreateProject(ctx context.Context, p *Project) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_projects (`+projectColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.ID, p.OrgID, p.Name, p.Slug, p.Description, p.RepositoryURL, p.Status, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

// GetProject returns the project, or ErrNotFound.
func (s *PgStore) GetProject(ctx context.Context, orgID, id string) (*Project, error) {
	var p Project
	err := s.db.QueryRow(ctx, `SELECT `+projectColumns+` FROM loomplane_projects WHERE org_id = $1 AND id = $2`, orgID, id).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description, &p.RepositoryURL, &p.Status, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	p.normalize()
	return &p, nil
}

// ListProjects returns the organization's projects, newest first.
func (s *PgStore) ListProjects(ctx context.Context, orgID string) ([]*Project, error) {
	rows, err := s.db.Query(ctx, `SELECT `+projectColumns+` FROM loomplane_projects
		WHERE org_id = $1 ORDER BY created_at DESC, id DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description, &p.RepositoryURL, &p.Status, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.normalize()
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// UpdateProject writes p's mutable fields: name, description, repository,
// status and updated_at.
func (s *PgStore) UpdateProject(ctx context.Context, p *Project) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_projects
		SET name = $3, description = $4, repository_url = $5, status = $6, updated_at = $7
		WHERE org_id = $1 AND id = $2`,
		p.OrgID, p.ID, p.Name, p.Description, p.RepositoryURL, p.Status, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return requireOneRow(res)
}

// DeleteProject removes the project; its environments go with it.
func (s *PgStore) DeleteProject(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_projects WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return requireOneRow(res)
}

// EnsureEnvironment inserts e unless the project already has an
// environment with that name.
func (s *PgStore) EnsureEnvironment(ctx context.Context, e *Environment) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM loomplane_projects WHERE org_id = $2 AND id = $3)
		ON CONFLICT (project_id, name) DO NOTHING`,
		e.ID, e.OrgID, e.ProjectID, e.Name, e.Kind, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert environment: %w", err)
	}
	return nil
}

// ListEnvironments returns the project's environments, production first.
func (s *PgStore) ListEnvironments(ctx context.Context, orgID, projectID string) ([]*Environment, error) {
	rows, err := s.db.Query(ctx, `SELECT id, org_id, project_id, name, kind, created_at, updated_at
		FROM loomplane_environments WHERE org_id = $1 AND project_id = $2
		ORDER BY (kind = 'production') DESC, name`, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Environment{}
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.Name, &e.Kind, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return out, nil
}

// normalize reports times in UTC: pgx scans TIMESTAMPTZ into the process's
// local zone, and the API should answer the same however the server's
// clock is set.
func (p *Project) normalize() {
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
}

func requireOneRow(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
