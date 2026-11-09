package nodes

import "context"

var _ WorkloadReader = (*PgStore)(nil)

// BindingsOn lists the org's bindings deployed through one of
// connectionIDs, or pinned by their Cloud placement to the server serverID,
// with their environment's name.
func (s *PgStore) BindingsOn(ctx context.Context, orgID string, connectionIDs []string, serverID string) ([]PlacedBinding, error) {
	if connectionIDs == nil {
		connectionIDs = []string{}
	}
	rows, err := s.db.Query(ctx, `SELECT b.id,b.project_id,b.environment_id,e.name,b.subject,b.name,b.config FROM loomplane_bindings b JOIN loomplane_environments e ON e.id=b.environment_id AND e.org_id=b.org_id WHERE b.org_id=$1 AND (b.connection_id=ANY($2) OR ($3<>'' AND b.config->'cloud_placement'->>'server_id'=$3)) ORDER BY b.project_id,e.name,b.subject,b.name`,
		orgID, connectionIDs, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlacedBinding
	for rows.Next() {
		var b PlacedBinding
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.EnvironmentID, &b.Environment, &b.Subject, &b.Name, &b.Config); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BackupPolicies lists the org's enabled backup policies in
// environmentIDs.
func (s *PgStore) BackupPolicies(ctx context.Context, orgID string, environmentIDs []string) ([]BackupPolicyRef, error) {
	if len(environmentIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT environment_id,resource FROM loomplane_backup_policies WHERE org_id=$1 AND environment_id=ANY($2) AND enabled`, orgID, environmentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupPolicyRef
	for rows.Next() {
		var p BackupPolicyRef
		if err := rows.Scan(&p.EnvironmentID, &p.Resource); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
