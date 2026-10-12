package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
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

// CreateProject inserts p and its environments in one transaction. A slug
// already used in the organization is ErrSlugTaken.
func (s *PgStore) CreateProject(ctx context.Context, p *Project, envs []*Environment) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_projects (`+projectColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.ID, p.OrgID, p.Name, p.Slug, p.Description, p.RepositoryURL, p.Status, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	for _, e := range envs {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_environments (`+environmentColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.ID, e.OrgID, e.ProjectID, e.Name, e.Kind, e.CreatedAt, e.UpdatedAt); err != nil {
			return fmt.Errorf("insert environment %s: %w", e.Name, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rollback undoes tx when err says the transaction failed, and returns err
// with anything the rollback itself reports.
func rollback(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) {
		return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
	}
	return err
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

const environmentColumns = `id, org_id, project_id, name, kind, created_at, updated_at`

// CreateEnvironment implements Store.
func (s *PgStore) CreateEnvironment(ctx context.Context, e *Environment) error {
	res, err := s.db.Exec(ctx, `INSERT INTO loomplane_environments (`+environmentColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM loomplane_projects WHERE org_id = $2 AND id = $3)`,
		e.ID, e.OrgID, e.ProjectID, e.Name, e.Kind, e.CreatedAt, e.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrEnvironmentExists
	}
	if err != nil {
		return fmt.Errorf("insert environment: %w", err)
	}
	return requireOneRow(res)
}

// GetEnvironment returns one environment, or ErrEnvironmentNotFound.
func (s *PgStore) GetEnvironment(ctx context.Context, orgID, projectID, id string) (*Environment, error) {
	var e Environment
	err := s.db.QueryRow(ctx, `SELECT `+environmentColumns+` FROM loomplane_environments
		WHERE org_id = $1 AND project_id = $2 AND id = $3`, orgID, projectID, id).
		Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.Name, &e.Kind, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get environment: %w", err)
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return &e, nil
}

// DeleteEnvironment implements Store. It locks the project's row first, so
// two deletes can't each see the other's environment and leave none.
func (s *PgStore) DeleteEnvironment(ctx context.Context, orgID, projectID, id string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM loomplane_projects WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, projectID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock project: %w", err)
	}
	var total int
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT count(*), bool_or(id = $2) FROM loomplane_environments WHERE project_id = $1`,
		projectID, id).Scan(&total, &exists); err != nil {
		return fmt.Errorf("count environments: %w", err)
	}
	switch {
	case !exists:
		return ErrEnvironmentNotFound
	case total <= 1:
		return ErrLastEnvironment
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_environments WHERE project_id = $1 AND id = $2`, projectID, id); err != nil {
		return fmt.Errorf("delete environment: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ListEnvironments returns the project's environments, production first.
func (s *PgStore) ListEnvironments(ctx context.Context, orgID, projectID string) ([]*Environment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+environmentColumns+`
		FROM loomplane_environments WHERE org_id = $1 AND project_id = $2
		ORDER BY array_position(ARRAY['production', 'staging', 'preview', 'development'], kind), name`, orgID, projectID)
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
