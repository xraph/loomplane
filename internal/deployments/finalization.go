package deployments

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/orchestration"
)

type finalizationStore interface {
	FinishedActivity(context.Context, string, string, string) (int, bool, error)
}

func (r *Runner) replayFinalization(ctx context.Context, d *Deployment, reason string) (bool, error) {
	key := orchestration.FinalizationKey(ctx)
	if key == "" {
		return false, nil
	}
	if stale, err := staleFinalization(ctx, d); stale || err != nil {
		return stale, err
	}
	store, ok := r.store.(finalizationStore)
	if !ok {
		return false, errors.New("durable deployment finalization is unavailable")
	}
	generation, found, err := store.FinishedActivity(ctx, d.OrgID, d.ID, key)
	if err != nil || !found {
		return false, err
	}
	// A successful finish can still owe repository bookkeeping after its commit.
	if generation == d.Retries && reason == "" && d.Status == StatusSucceeded && d.FinishedAt != nil {
		return true, r.recordDeployed(ctx, d, *d.FinishedAt)
	}
	return true, nil
}

func (s *PgStore) FinishedActivity(ctx context.Context, org, id, key string) (generation int, found bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT retries FROM loomplane_deployment_finishes WHERE org_id=$1 AND deployment_id=$2 AND activity_key=$3`, org, id, key).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return generation, err == nil, err
}

// finishActivity records the receipt and terminal step state in the same
// transaction that Reopen locks. Replaying the receipt cannot settle a retry.
func (s *PgStore) finishActivity(ctx context.Context, org, id, key, status, reason string, at time.Time) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	var current, plan string
	var generation int
	var raw []byte
	if scanErr := tx.QueryRow(ctx, `SELECT status,retries,steps,plan_id FROM loomplane_deployments WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&current, &generation, &raw, &plan); scanErr != nil {
		return scanErr
	}
	if stale, fenceErr := staleFinalization(ctx, &Deployment{ID: id, PlanID: plan, Retries: generation}); fenceErr != nil {
		return fenceErr
	} else if stale {
		return tx.Commit()
	}
	var seen bool
	if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployment_finishes WHERE org_id=$1 AND deployment_id=$2 AND activity_key=$3)`, org, id, key).Scan(&seen); scanErr != nil {
		return scanErr
	}
	if seen || current == StatusPaused || current == StatusCancelled {
		return tx.Commit()
	}
	if current == StatusRunning {
		steps, decodeErr := decodeSteps(raw)
		if decodeErr != nil {
			return decodeErr
		}
		if status == StatusFailed {
			for i := range steps {
				if steps[i].Status == StepPending {
					steps[i].Status, steps[i].Message = StepSkipped, "Skipped after an earlier step failed"
				}
			}
		}
		raw, err = encodeSteps(steps)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments SET status=$3,error=$4,finished_at=$5,steps=$6 WHERE org_id=$1 AND id=$2`, org, id, status, reason, at, raw); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_deployment_finishes(org_id,deployment_id,activity_key,retries,created_at) VALUES($1,$2,$3,$4,$5)`, org, id, key, generation, at); err != nil {
		return err
	}
	return tx.Commit()
}

// Standalone workflows encode their retry generation in their durable identity.
// This also fences a finish committed before receipt support was installed.
func staleFinalization(ctx context.Context, d *Deployment) (bool, error) {
	workflow := orchestration.FinalizationWorkflow(ctx)
	expected := 0
	if workflow != d.PlanID {
		suffix, retry := strings.CutPrefix(workflow, d.ID+".retry.")
		if !retry {
			return false, nil
		}
		var err error
		expected, err = strconv.Atoi(suffix)
		if err != nil || expected < 1 || strconv.Itoa(expected) != suffix {
			return false, errors.New("invalid deployment finalization retry identity")
		}
	}
	if expected > d.Retries {
		return false, errors.New("deployment finalization is ahead of the admitted retry")
	}
	return expected < d.Retries, nil
}
