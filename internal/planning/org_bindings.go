package planning

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// OrgServiceBindings is every service an organization runs, across its
// projects and environments, by ID: what its metrics are read for.
func (s *PgStore) OrgServiceBindings(ctx context.Context, orgID string) ([]*Binding, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings
		WHERE org_id = $1 AND subject = $2 ORDER BY id`, orgID, ServiceSubject)
	if err != nil {
		return nil, fmt.Errorf("list the organization's services: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the organization's services: %w", err)
	}
	return out, nil
}

// OrgResource is a resource binding with the names of the environment and
// project it runs in.
type OrgResource struct {
	*Binding
	Environment, Project string
}

// OrgResourceBindings is every resource an organization runs, across its
// projects and environments, by ID, with where it runs: what planning
// checks a Postgres server another Fabriq already uses against.
func (s *PgStore) OrgResourceBindings(ctx context.Context, orgID string) ([]*OrgResource, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bindingColumns+`, environment_name, project_name FROM (
			SELECT b.*, e.name AS environment_name, p.name AS project_name FROM loomplane_bindings b
			JOIN loomplane_environments e ON e.id = b.environment_id AND e.org_id = b.org_id
			JOIN loomplane_projects p ON p.id = b.project_id AND p.org_id = b.org_id
			WHERE b.org_id = $1 AND b.subject = $2) AS r
		ORDER BY id`, orgID, ResourceSubject)
	if err != nil {
		return nil, fmt.Errorf("list the organization's resources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*OrgResource{}
	for rows.Next() {
		r := &OrgResource{}
		if r.Binding, err = scanBinding(withNames{rows, &r.Environment, &r.Project}); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the organization's resources: %w", err)
	}
	return out, nil
}

// withNames scans a binding's row followed by the environment and project
// names.
type withNames struct {
	row                  pgx.Row
	environment, project *string
}

func (w withNames) Scan(dest ...any) error {
	return w.row.Scan(append(dest, w.environment, w.project)...)
}
