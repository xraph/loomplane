package cloudinfra

import (
	"context"
	"fmt"

	"github.com/xraph/grove/driver"
)

// These errors preserve the state observed under the first-effect gate even
// when a concurrent command changes deployment state before the caller reads it.
var (
	ErrWorkloadPaused    = fmt.Errorf("%w: deployment is paused before workload effects", ErrConflict)
	ErrWorkloadCancelled = fmt.Errorf("%w: deployment is cancelled before workload effects", ErrConflict)
)

// guardDeploymentEffects runs under the organization gate. Accepted effects
// can reconcile, but a paused or cancelled deployment cannot accept new work.
func guardDeploymentEffects(ctx context.Context, tx driver.Tx, org, plan string, accepted bool) error {
	if accepted {
		return nil
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT status FROM loomplane_deployments WHERE org_id=$1 AND plan_id=$2 AND status IN ('paused','cancelled')),'')`, org, plan).Scan(&status); err != nil {
		return err
	}
	switch status {
	case "paused":
		return ErrWorkloadPaused
	case "cancelled":
		return ErrWorkloadCancelled
	}
	return nil
}

// CancelUnstartedCapacity runs inside the cancellation transaction, after its
// organization gate. Durable acceptance receipts retain uncertain workloads;
// absence of a pool receipt never proves a reservation safe to release.
func CancelUnstartedCapacity(ctx context.Context, tx driver.Tx, org, plan, deployment, environment string) error {
	rows, err := tx.Query(ctx, `SELECT service FROM loomplane_pool_execution WHERE org_id=$1 AND plan_id=$2 UNION SELECT service FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND deployment_id=$3 ORDER BY service`, org, plan, deployment)
	if err != nil {
		return err
	}
	var services []string
	for rows.Next() {
		var service string
		if err = rows.Scan(&service); err != nil {
			rows.Close()
			return err
		}
		services = append(services, service)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, service := range services {
		if checkErr := LockRouteWorkload(ctx, tx, org, environment, service); checkErr != nil {
			return checkErr
		}
	}
	// Pool rows precede workload rows, matching GuardPoolWrite. The first-effect
	// markers also hold the organization gate, so none can cross this decision.
	_, err = tx.Exec(ctx, `WITH fenced AS (
 UPDATE loomplane_pool_execution x SET phase='released',active=false,generation=generation+1,revision=revision+1
 WHERE x.org_id=$1 AND x.plan_id=$2 AND x.phase='reserved' AND NOT x.started AND coalesce(octet_length(x.checkpoint),0)=0
 AND NOT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r WHERE r.org_id=x.org_id AND r.plan_id=x.plan_id AND r.service=x.service AND r.binding_id<>'')
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_attempts w WHERE w.org_id=x.org_id AND w.environment_id=$3 AND w.service=x.service AND (w.plan_id=x.plan_id OR NOT w.settled) AND (w.effects_started OR w.legacy_unfenced))
 RETURNING org_id,plan_id,service)
 DELETE FROM loomplane_capacity_reservations r USING fenced f WHERE r.org_id=f.org_id AND r.plan_id=f.plan_id AND r.service=f.service AND r.binding_id=''`, org, plan, environment)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_workload_attempts w SET active=false,settled=true,generation=generation+1
 WHERE w.org_id=$1 AND w.plan_id=$2 AND w.deployment_id=$3 AND NOT w.effects_started AND NOT w.legacy_unfenced
 AND NOT EXISTS(SELECT 1 FROM loomplane_pool_execution x WHERE x.org_id=w.org_id AND x.plan_id=w.plan_id AND x.service=w.service AND (x.started OR coalesce(octet_length(x.checkpoint),0)>0 OR x.phase NOT IN ('reserved','released')))
 AND NOT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r WHERE r.org_id=w.org_id AND r.plan_id=w.plan_id AND r.service=w.service AND r.binding_id<>'')`, org, plan, deployment)
	return err
}
