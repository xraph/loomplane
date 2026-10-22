package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// WorkloadAttempt survives a lost locking session. Only its current generation
// can publish progress or settle the service after external effects return.
type WorkloadAttempt struct {
	Org, Project, Environment, Service, Plan, Deployment string
	Generation                                           int64
}
type workloadAttemptKey struct{}

func WorkloadAttemptFrom(ctx context.Context) (WorkloadAttempt, bool) {
	a, ok := ctx.Value(workloadAttemptKey{}).(WorkloadAttempt)
	return a, ok
}
func (a WorkloadAttempt) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, workloadAttemptKey{}, a)
}

// LockRouteWorkload is always acquired after the organization lock. It protects
// absent route rows as well as existing routes, and is never held over effects.
func LockRouteWorkload(ctx context.Context, tx driver.Tx, org, environment, service string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "route-workload/"+org+"/"+environment+"/"+service)
	return err
}

func (s *PgStore) BeginWorkloadAttempt(ctx context.Context, a WorkloadAttempt, routes []Route, finalizing bool) (*WorkloadAttempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, a.Org); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := LockRouteWorkload(ctx, tx, a.Org, a.Environment, a.Service); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := GuardPoolWrite(ctx, tx); checkErr != nil {
		return nil, checkErr
	}
	var legacy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE org_id=$1 AND environment_id=$2 AND service=$3 AND legacy_unfenced)`, a.Org, a.Environment, a.Service).Scan(&legacy); checkErr != nil {
		return nil, checkErr
	}
	if legacy {
		return nil, fmt.Errorf("%w: this service has an unfinished pre-upgrade unfenced worker; stop the old worker and reconcile its runtime before enabling new releases", ErrConflict)
	}
	if !finalizing {
		raw, encodeErr := json.Marshal(routes)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if len(routes) == 0 {
			raw = []byte("[]")
		}
		var equal bool
		err = tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(body),'[]'::jsonb) @> $5::jsonb AND coalesce(jsonb_agg(body),'[]'::jsonb) <@ $5::jsonb FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND body->'input'->>'service'=$4 AND status<>'removed'`, a.Org, a.Project, a.Environment, a.Service, raw).Scan(&equal)
		if err != nil {
			return nil, err
		}
		if !equal {
			return nil, fmt.Errorf("%w: service routes changed since review; plan again", ErrConflict)
		}
	}
	// A different plan needs a settled predecessor. Same-plan takeover must
	// advance the existing physical fence before its first external mutation.
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_workload_attempts(org_id,project_id,environment_id,service,plan_id,deployment_id,effects_started) VALUES($1,$2,$3,$4,$5,$6,false)
	ON CONFLICT(org_id,environment_id,service) DO UPDATE SET plan_id=EXCLUDED.plan_id,deployment_id=EXCLUDED.deployment_id,generation=loomplane_workload_attempts.generation+1,active=true,settled=false,effects_started=CASE WHEN loomplane_workload_attempts.settled THEN false ELSE loomplane_workload_attempts.effects_started END
	WHERE NOT loomplane_workload_attempts.legacy_unfenced AND loomplane_workload_attempts.project_id=EXCLUDED.project_id AND ((loomplane_workload_attempts.plan_id=EXCLUDED.plan_id AND loomplane_workload_attempts.deployment_id=EXCLUDED.deployment_id) OR (loomplane_workload_attempts.settled AND NOT $7)) RETURNING generation`, a.Org, a.Project, a.Environment, a.Service, a.Plan, a.Deployment, finalizing).Scan(&a.Generation)
	if err != nil {
		return nil, fmt.Errorf("%w: recover the prior service attempt before starting another release", ErrConflict)
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return &a, nil
}

func GuardWorkloadWrite(ctx context.Context, tx driver.Tx) error {
	a, ok := WorkloadAttemptFrom(ctx)
	if !ok {
		return nil
	}
	var generation int64
	err := tx.QueryRow(ctx, `SELECT generation FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND deployment_id=$6 AND generation=$7 AND active AND NOT settled FOR UPDATE`, a.Org, a.Project, a.Environment, a.Service, a.Plan, a.Deployment, a.Generation).Scan(&generation)
	if err != nil {
		return fmt.Errorf("%w: stale service attempt cannot publish progress", ErrConflict)
	}
	return nil
}

func (s *PgStore) FinishWorkloadAttempt(ctx context.Context, a WorkloadAttempt, settled bool) error {
	var generation int64
	err := s.db.QueryRow(ctx, `UPDATE loomplane_workload_attempts SET active=false,settled=($8 OR NOT effects_started) WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND deployment_id=$6 AND generation=$7 AND active AND NOT legacy_unfenced RETURNING generation`, a.Org, a.Project, a.Environment, a.Service, a.Plan, a.Deployment, a.Generation, settled).Scan(&generation)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func guardRouteAttachment(ctx context.Context, tx driver.Tx, route *Route, kind string) error {
	if err := LockRouteWorkload(ctx, tx, route.OrgID, route.Input.EnvironmentID, route.Input.Service); err != nil {
		return err
	}
	if a, ok := WorkloadAttemptFrom(ctx); ok && kind != "apply" && a.Org == route.OrgID && a.Project == route.ProjectID && a.Environment == route.Input.EnvironmentID && a.Service == route.Input.Service {
		return GuardWorkloadWrite(ctx, tx)
	}
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE org_id=$1 AND environment_id=$2 AND service=$3 AND NOT settled) OR EXISTS(SELECT 1 FROM loomplane_pool_execution x JOIN loomplane_plans p ON p.org_id=x.org_id AND p.id=x.plan_id WHERE x.org_id=$1 AND p.environment_id=$2 AND x.service=$3 AND (x.active OR x.phase IN ('cleanup','blocked')))`, route.OrgID, route.Input.EnvironmentID, route.Input.Service).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: finish or recover the service deployment before changing its routes", ErrBusy)
	}
	return nil
}

// MarkWorkloadEffects persists uncertainty before entering a remote mutation.
func (s *PgStore) MarkWorkloadEffects(ctx context.Context, a WorkloadAttempt) error {
	var generation int64
	err := s.db.QueryRow(ctx, `UPDATE loomplane_workload_attempts SET effects_started=true WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND deployment_id=$6 AND generation=$7 AND active AND NOT settled AND NOT legacy_unfenced RETURNING generation`, a.Org, a.Project, a.Environment, a.Service, a.Plan, a.Deployment, a.Generation).Scan(&generation)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
