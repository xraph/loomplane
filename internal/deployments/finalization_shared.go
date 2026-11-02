package deployments

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/orchestration"
)

type finishQuery interface {
	QueryRow(context.Context, string, ...any) driver.Row
}

// StaleFinishActivity checks the accepted shared attempt before runner effects.
func (s *PgStore) StaleFinishActivity(ctx context.Context, org, id string) (bool, error) {
	return staleSharedFinalization(ctx, s.db, org, id, false)
}

// A shared attempt can advance without reopening every participant. Its owner,
// rather than the participant retry count, identifies the admitted workflow.
func staleSharedFinalization(ctx context.Context, query finishQuery, org, id string, lock bool) (bool, error) {
	var syncID string
	if err := query.QueryRow(ctx, `SELECT coalesce(p.result->>'cloud_sync_id','') FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id WHERE d.org_id=$1 AND d.id=$2`, org, id).Scan(&syncID); err != nil {
		return false, err
	}
	if syncID == "" {
		return false, nil
	}
	suffix, ok := strings.CutPrefix(orchestration.FinalizationWorkflow(ctx), syncID+".attempt.")
	attempt, err := strconv.Atoi(suffix)
	if !ok || err != nil || attempt < 1 || strconv.Itoa(attempt) != suffix {
		return false, errors.New("deployment finalization does not belong to its Cloud sync attempt")
	}
	sql := `SELECT (body->>'attempt')::integer FROM loomplane_cloud_syncs WHERE org_id=$1 AND id=$2`
	if lock {
		sql += ` FOR SHARE`
	}
	var current int
	if err := query.QueryRow(ctx, sql, org, syncID).Scan(&current); err != nil {
		return false, err
	}
	if current < 1 || attempt > current {
		return false, errors.New("deployment finalization is ahead of the admitted Cloud sync attempt")
	}
	if attempt < current {
		return true, nil
	}
	return false, nil
}
