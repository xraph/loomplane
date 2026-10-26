package planning

import (
	"context"
	"fmt"
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

// OrgResourceBindings is every resource an organization runs, across its
// projects and environments, by ID: what planning checks a database
// another Fabriq already uses against.
func (s *PgStore) OrgResourceBindings(ctx context.Context, orgID string) ([]*Binding, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings
		WHERE org_id = $1 AND subject = $2 ORDER BY id`, orgID, ResourceSubject)
	if err != nil {
		return nil, fmt.Errorf("list the organization's resources: %w", err)
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
		return nil, fmt.Errorf("list the organization's resources: %w", err)
	}
	return out, nil
}
