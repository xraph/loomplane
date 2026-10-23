package planning

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// RestoreCloudCompensation publishes the retained source and transfers its
// reservation in the same transaction as the service attempt ownership.
func (s *PgStore) RestoreCloudCompensation(ctx context.Context, previous, next cloudinfra.WorkloadAttempt, plan *Plan, action Action) (*cloudinfra.WorkloadAttempt, error) {
	c := action.Compensation
	if c == nil || !c.Retained || c.PlanID != previous.Plan || plan.ID != next.Plan {
		return nil, cloudprofiles.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback after commit is harmless
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, next.Org); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := cloudinfra.LockRouteWorkload(ctx, tx, next.Org, next.Environment, next.Service); checkErr != nil {
		return nil, checkErr
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	current, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings WHERE org_id=$1 AND id=$2 FOR UPDATE`, next.Org, c.Candidate.ID))
	if err != nil {
		return nil, err
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, `SELECT loomplane_retained_compensation(p,$3::jsonb) AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE a=$3::jsonb) FROM loomplane_plans p WHERE p.org_id=$1 AND p.id=$2`, next.Org, plan.ID, raw).Scan(&valid); checkErr != nil {
		return nil, checkErr
	}
	if !valid {
		return nil, cloudprofiles.ErrConflict
	}
	transferred, err := cloudinfra.TransferWorkloadAttempt(ctx, tx, previous, next)
	if err != nil {
		return nil, err
	}
	source := c.Source
	source.PlanID = plan.ID
	source.UpdatedAt = time.Now().UTC()
	source.CreatedAt = current.CreatedAt
	source.Replaced = nil
	for _, retained := range current.Replaced {
		if !sameCompensationResource(retained, c.Source) {
			source.Replaced = append(source.Replaced, retained)
		}
	}
	if !sameCompensationResource(*current, c.Source) {
		candidate := *current
		candidate.Replaced = nil
		source.Replaced = append(source.Replaced, candidate)
	}
	var cfg ServiceConfig
	if checkErr := json.Unmarshal(c.Source.Config, &cfg); checkErr != nil {
		return nil, checkErr
	}
	if cfg.CloudPlacement != nil && cfg.CloudPlacement.PoolID != "" {
		body, encodeErr := json.Marshal(cfg.CloudPlacement)
		if encodeErr != nil {
			return nil, encodeErr
		}
		var id string
		err = tx.QueryRow(ctx, `UPDATE loomplane_capacity_reservations SET plan_id=$7 WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND body=$6::jsonb AND binding_id=$8 RETURNING plan_id`, next.Org, next.Project, next.Environment, next.Service, c.Source.PlanID, body, next.Plan, source.ID).Scan(&id)
		if err != nil {
			return nil, errors.Join(cloudprofiles.ErrConflict, err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_pool_execution(org_id,plan_id,service) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=$1 AND plan_id=$2 AND service=$3) ON CONFLICT DO NOTHING`, next.Org, next.Plan, next.Service); err != nil {
		return nil, err
	}
	if checkErr := putBinding(ctx, tx, &source); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.checkpointCloudBinding(ctx, tx, plan.CloudSyncID, plan.OrgID, source.ID); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return transferred, nil
}

func (s *PgStore) CompensationRoutes(ctx context.Context, org, project, environment string) ([]cloudinfra.Route, error) {
	routes, err := cloudinfra.NewPgStore(s.db).Routes(ctx, org, project)
	if err != nil {
		return nil, err
	}
	var result []cloudinfra.Route
	for _, route := range routes {
		if route.Input.EnvironmentID == environment && route.Status != "removed" {
			result = append(result, *route)
		}
	}
	return result, nil
}

func (s *Service) unstartedCloudCompensation(ctx context.Context, org, syncID, environment string, snapshot *cloudprofiles.Snapshot) (bool, error) {
	var legacyPool bool
	for _, env := range snapshot.Environments {
		if env.EnvironmentID != environment || env.AcceptedPlan != nil {
			continue
		}
		var original Plan
		if err := json.Unmarshal(env.Plan, &original); err != nil {
			return false, err
		}
		for _, action := range original.Actions {
			if action.Provider == "server" && action.Op != Keep && action.Service != nil && action.Service.CloudPlacement != nil && action.Service.CloudPlacement.PoolID != "" {
				legacyPool = true
			}
		}
	}
	if !legacyPool {
		return false, nil
	}
	if _, err := s.store.GetPlan(ctx, org, migrationPlanID(syncID, environment)); !errors.Is(err, ErrNotFound) {
		return false, err
	}
	reader, ok := s.store.(interface {
		UnstartedCloudCompensation(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return false, errors.New("legacy compensation proof is unavailable")
	}
	safe, err := reader.UnstartedCloudCompensation(ctx, org, syncID, environment)
	if err != nil {
		return false, err
	}
	if !safe {
		return false, errors.New("reconcile the original legacy attempt before reviewing compensation")
	}
	return true, nil
}
func (s *PgStore) UnstartedCloudCompensation(ctx context.Context, org, syncID, environment string) (bool, error) {
	var safe bool
	err := s.db.QueryRow(ctx, `SELECT loomplane_unstarted_cloud_compensation($1,$2,$3,'')`, org, syncID, environment).Scan(&safe)
	return safe, err
}
