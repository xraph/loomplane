package cloudinfra

import (
	"context"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// TransferWorkloadAttempt follows physical quiescence of the original attempt.
// Its generation and the accepted compensation identity must still match.
func (s *PgStore) TransferWorkloadAttempt(ctx context.Context, previous, next WorkloadAttempt) (*WorkloadAttempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	result, err := TransferWorkloadAttempt(ctx, tx, previous, next)
	if err != nil {
		return nil, err
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return result, nil
}

func TransferWorkloadAttempt(ctx context.Context, tx driver.Tx, previous, next WorkloadAttempt) (*WorkloadAttempt, error) {
	if previous.Org != next.Org || previous.Project != next.Project || previous.Environment != next.Environment || previous.Service != next.Service || previous.Plan == next.Plan {
		return nil, ErrInvalid
	}
	if err := cloudprofiles.LockOrganization(ctx, tx, next.Org); err != nil {
		return nil, err
	}
	if err := LockRouteWorkload(ctx, tx, next.Org, next.Environment, next.Service); err != nil {
		return nil, err
	}
	if err := GuardPoolWrite(previous.Context(ctx), tx); err != nil {
		return nil, err
	}
	var err error
	var authorized bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_plans n JOIN loomplane_cloud_syncs c ON c.org_id=n.org_id AND c.id=n.result->>'cloud_sync_id' JOIN loomplane_plans p ON p.org_id=n.org_id AND p.id=$5 JOIN loomplane_cloud_syncs o ON o.org_id=p.org_id AND o.id=p.result->>'cloud_sync_id' WHERE n.org_id=$1 AND n.project_id=$2 AND n.environment_id=$3 AND n.id=$4 AND c.status='running' AND loomplane_accepted_pool_sync(n) AND o.status='reverting' AND c.body->>'reverts'=o.id AND EXISTS(SELECT 1 FROM jsonb_array_elements(n.result->'actions') a WHERE a->>'name'=$6 AND a->'compensation'->>'plan_id'=p.id))`, next.Org, next.Project, next.Environment, next.Plan, previous.Plan, next.Service).Scan(&authorized)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, ErrConflict
	}
	err = tx.QueryRow(ctx, `UPDATE loomplane_workload_attempts SET plan_id=$8,deployment_id=$9,generation=generation+1,active=true,settled=false WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND deployment_id=$6 AND generation=$7 AND active AND NOT legacy_unfenced RETURNING generation`, previous.Org, previous.Project, previous.Environment, previous.Service, previous.Plan, previous.Deployment, previous.Generation, next.Plan, next.Deployment).Scan(&next.Generation)
	if err != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	// Pool generations also exclude old workers that retained a detached session.
	if _, err = tx.Exec(ctx, `UPDATE loomplane_pool_execution SET phase='compensating',active=false,generation=generation+1,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3`, previous.Org, previous.Plan, previous.Service); err != nil {
		return nil, err
	}
	return &next, nil
}
