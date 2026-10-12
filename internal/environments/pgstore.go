package environments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations): a project or environment deleted takes its
// variables with it.
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const variableColumns = `id, org_id, project_id, environment_id, environment, service, key, value, secret,
	created_by, updated_by, created_at, updated_at`

func scanVariable(row pgx.Row) (*Variable, error) {
	var v Variable
	var projectID, envID *string
	if err := row.Scan(&v.ID, &v.OrgID, &projectID, &envID, &v.Environment, &v.Service, &v.Key, &v.Value, &v.Secret,
		&v.CreatedBy, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	switch {
	case projectID == nil:
		v.Scope = ScopeOrganization
	case v.Service != "":
		v.Scope = ScopeService
	default:
		v.Scope = ScopeProject
	}
	if projectID != nil {
		v.ProjectID = *projectID
	}
	if envID != nil {
		v.environmentID = *envID
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return &v, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateVariable implements Store.
func (s *PgStore) CreateVariable(ctx context.Context, v *Variable) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_variables (`+variableColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		v.ID, v.OrgID, nullable(v.ProjectID), nullable(v.environmentID), v.Environment, v.Service, v.Key, v.Value, v.Secret,
		v.CreatedBy, v.UpdatedBy, v.CreatedAt, v.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("insert variable: %w", err)
	}
	return nil
}

// GetVariable returns the variable, or ErrNotFound.
func (s *PgStore) GetVariable(ctx context.Context, orgID, id string) (*Variable, error) {
	v, err := scanVariable(s.db.QueryRow(ctx, `SELECT `+variableColumns+` FROM loomplane_variables
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get variable: %w", err)
	}
	return v, nil
}

// UpdateVariable implements Store.
func (s *PgStore) UpdateVariable(ctx context.Context, v *Variable) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_variables SET value = $3, secret = $4, updated_by = $5, updated_at = $6
		WHERE org_id = $1 AND id = $2`, v.OrgID, v.ID, v.Value, v.Secret, v.UpdatedBy, v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update variable: %w", err)
	}
	return oneRow(res)
}

// DeleteVariable implements Store.
func (s *PgStore) DeleteVariable(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_variables WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete variable: %w", err)
	}
	return oneRow(res)
}

// ListOrganization implements Store.
func (s *PgStore) ListOrganization(ctx context.Context, orgID string) ([]*Variable, error) {
	return s.list(ctx, `SELECT `+variableColumns+` FROM loomplane_variables
		WHERE org_id = $1 AND project_id IS NULL ORDER BY key, environment`, orgID)
}

// ListProject implements Store.
func (s *PgStore) ListProject(ctx context.Context, orgID, projectID string) ([]*Variable, error) {
	return s.list(ctx, `SELECT `+variableColumns+` FROM loomplane_variables
		WHERE org_id = $1 AND project_id = $2 ORDER BY service, key, environment`, orgID, projectID)
}

// ForEnvironment implements Store.
func (s *PgStore) ForEnvironment(ctx context.Context, orgID, projectID, envID, envName string) ([]*Variable, error) {
	return s.list(ctx, `SELECT `+variableColumns+` FROM loomplane_variables
		WHERE org_id = $1 AND (
			(project_id IS NULL AND environment IN ('', $4))
			OR (project_id = $2 AND (environment_id IS NULL OR environment_id = $3))
		)`, orgID, projectID, envID, envName)
}

// SecretIDs implements Store.
func (s *PgStore) SecretIDs(ctx context.Context, orgID string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM loomplane_variables WHERE org_id = $1 AND secret`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list secret variables: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan variable id: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list secret variables: %w", err)
	}
	return out, nil
}

func (s *PgStore) list(ctx context.Context, query string, args ...any) ([]*Variable, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Variable{}
	for rows.Next() {
		v, err := scanVariable(rows)
		if err != nil {
			return nil, fmt.Errorf("scan variable: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	return out, nil
}

func oneRow(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
