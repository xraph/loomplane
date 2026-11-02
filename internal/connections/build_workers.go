package connections

import (
	"context"
	"errors"
)

// BuildWorkerShared rechecks pool membership when a build starts. Another
// connection to the same Docker daemon cannot bypass a pool's build policy.
func (s *Service) BuildWorkerShared(ctx context.Context, org, connection string) (bool, error) {
	reader, ok := s.store.(interface {
		BuildWorkerShared(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, errors.New("build worker inventory is unavailable")
	}
	return reader.BuildWorkerShared(ctx, org, connection)
}

func (s *PgStore) BuildWorkerShared(ctx context.Context, org, connection string) (bool, error) {
	var shared bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM loomplane_cloud_connections c
		JOIN loomplane_cloud_servers s ON (s.runtime_connection_id=c.id OR
			(coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id'))
		JOIN loomplane_server_pool_members m ON m.server_id=s.id
		WHERE c.org_id=$1 AND c.id=$2 AND c.cloud='server' AND s.status NOT IN ('deleted','disconnected')
	)`, org, connection).Scan(&shared)
	return shared, err
}
