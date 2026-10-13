package realtime

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ProjectFor resolves a subscription resource through its organization.
func (s *PgStore) ProjectFor(ctx context.Context, org, kind, id string) (string, error) {
	query := `SELECT id FROM loomplane_projects WHERE org_id=$1 AND id=$2`
	if kind == "deployment" {
		query = `SELECT project_id FROM loomplane_deployments WHERE org_id=$1 AND id=$2`
	} else if kind != "project" {
		return "", ErrInvalid
	}
	var project string
	err := s.db.QueryRow(ctx, query, org, id).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return project, err
}
