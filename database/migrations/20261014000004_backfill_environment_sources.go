package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// An environment deployed before sources existed has no rows saying what's
// deployed there, so its first scoped plan would call every held source
// never deployed. Each (environment, source) gets the commit the newest
// successful deployment moved it to, which is exactly what a deployment
// records now. A failed deployment doesn't count, the same as it doesn't
// write a row today. A row that's already there was written by a real
// deployment and stays.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `INSERT INTO loomplane_environment_sources (environment_id, source_id, org_id, analysis_id, commit_sha, plan_id, deployed_at)
				SELECT DISTINCT ON (d.environment_id, ps.source_id)
					d.environment_id, ps.source_id, d.org_id, ps.analysis_id, ps.commit_sha, d.plan_id, d.finished_at
				FROM loomplane_deployments d
				JOIN loomplane_plan_sources ps ON ps.plan_id = d.plan_id AND ps.in_scope
				WHERE d.status = 'succeeded' AND d.finished_at IS NOT NULL
				ORDER BY d.environment_id, ps.source_id, d.finished_at DESC
				ON CONFLICT (environment_id, source_id) DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill environment sources: %w", err)
			}
			return nil
		},
		// Nothing to undo: the backfilled rows can't be told apart from ones a
		// deployment wrote, and create_plan_sources' down drops the table anyway.
		nil,
	)
}
