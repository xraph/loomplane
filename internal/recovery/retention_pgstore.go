package recovery

import "context"

func (s *PgStore) RetentionInventory(ctx context.Context, anchor *Backup) ([]RetentionEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+`,EXISTS(
 SELECT 1 FROM loomplane_service_restores r WHERE r.org_id=b.org_id AND r.project_id=b.project_id AND r.backup_id=b.id AND r.status<>'deleted')
 FROM loomplane_service_backups b WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND name=$4
 AND connection_id=$5 AND source->>'id'=$6 AND source->>'external_id'=$7
 ORDER BY completed_at DESC NULLS LAST,created_at DESC,id DESC LIMIT $8`, anchor.OrgID, anchor.ProjectID, anchor.EnvironmentID, anchor.Name, anchor.Source.ConnectionID, anchor.Source.ID, anchor.Source.ExternalID, retentionInventoryLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []RetentionEntry{}
	for rows.Next() {
		var inUse bool
		b, scanErr := scan(rows, &inUse)
		if scanErr != nil {
			return nil, scanErr
		}
		points = append(points, RetentionEntry{Backup: *b, InUse: inUse})
	}
	return points, rows.Err()
}
