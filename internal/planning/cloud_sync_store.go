package planning

import (
	"context"
	"encoding/json"
)

// PutCloudSyncPlan records the approved snapshot without consulting current drafts.
// A deterministic plan ID makes recovery after insertion idempotent.
func (s *PgStore) PutCloudSyncPlan(ctx context.Context, p *Plan) error {
	raw, err := json.Marshal(Result{CloudSyncID: p.CloudSyncID, Actions: p.Actions, Problems: p.Problems, Warnings: p.Warnings, Summary: p.Summary, Executable: p.Executable, Connections: p.Connections})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at,planned_at,decided_by,decided_at) VALUES($1,$2,$3,$4,$5,'approved',$6,$7,$8,$9,$10,$11,$12,$12,$11,$12) ON CONFLICT(id) DO NOTHING`, p.ID, p.OrgID, p.ProjectID, p.EnvironmentID, p.Environment, p.Revision, p.ManifestID, p.ManifestRevision, raw, p.Executable, p.CreatedBy, p.CreatedAt)
	return err
}
