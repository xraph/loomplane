package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/orchestration"
)

func (s *Service) CloudSyncNeedsCapacity(ctx context.Context, org, project, id string) (bool, error) {
	st, err := s.syncStore()
	if err != nil {
		return false, err
	}
	_, snapshot, err := st.GetSync(ctx, org, project, id)
	if err != nil {
		return false, err
	}
	for _, env := range snapshot.Environments {
		var p Plan
		if decodeErr := json.Unmarshal(env.Plan, &p); decodeErr != nil {
			return false, decodeErr
		}
		for _, a := range p.Actions {
			if a.Service != nil && a.Service.CapacityIntent != nil {
				return true, nil
			}
		}
	}
	return false, nil
}

type capacitySyncRunner interface {
	StartCapacityCloudPlan(context.Context, string, string) (string, [][]string, error)
	CapacityCloudPlanStatus(context.Context, string, string) (string, error)
}

// PrepareCapacityCloudSync persists participants before any runtime effect.
func (s *Service) PrepareCapacityCloudSync(ctx context.Context, org, project, id string) ([]orchestration.CloudSyncDeployment, error) {
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	lock, ok := st.(interface {
		WithSyncLock(context.Context, string, func() error) error
	})
	if !ok {
		return nil, errors.New("cloud sync lock is unavailable")
	}
	var out []orchestration.CloudSyncDeployment
	err = lock.WithSyncLock(ctx, id, func() error {
		sync, snapshot, e := st.GetSync(ctx, org, project, id)
		if e != nil {
			return e
		}
		if sync.Status == cloudprofiles.SyncSucceeded || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncReverting || sync.Status == cloudprofiles.SyncFailed {
			return nil
		}
		if checkErr := s.authorizeStoredCloudSync(ctx, snapshot.Actor, sync, snapshot); checkErr != nil {
			return checkErr
		}
		if checkErr := s.validateSnapshotConnections(ctx, org, snapshot); checkErr != nil {
			return checkErr
		}
		if checkErr := validateSyncGrants(ctx, st, org, snapshot); checkErr != nil {
			return checkErr
		}
		runner, ok := s.cloudSyncRunner.(capacitySyncRunner)
		if !ok {
			return errors.New("capacity Cloud sync runner is unavailable")
		}
		plans := map[string]*Plan{}
		for _, env := range snapshot.Environments {
			plan, pe := s.acceptedCloudPlan(ctx, sync, env)
			if pe != nil {
				return pe
			}
			plans[env.EnvironmentID] = plan
		}
		if sync.Status == cloudprofiles.SyncRouting || sync.Status == cloudprofiles.SyncCleanup {
			return nil
		}
		sync.Status = cloudprofiles.SyncRunning
		if checkErr := st.SaveSync(ctx, sync, snapshot); checkErr != nil {
			return checkErr
		}
		for i := range sync.Environments {
			env := &sync.Environments[i]
			if env.Status == cloudprofiles.SyncSucceeded {
				continue
			}
			plan := plans[env.EnvironmentID]
			if len(env.Services) == 0 {
				env.Status = cloudprofiles.SyncSucceeded
				continue
			}
			dep, steps, se := runner.StartCapacityCloudPlan(ctx, org, plan.ID)
			if se != nil {
				return se
			}
			env.PlanID = plan.ID
			env.DeploymentID = dep
			if dep != "" {
				out = append(out, orchestration.CloudSyncDeployment{PlanID: plan.ID, DeploymentID: dep, Steps: steps})
			}
		}
		return st.SaveSync(ctx, sync, snapshot)
	})
	return out, err
}

// FinishCapacityCloudSync enters routing only after every participant settled.
func (s *Service) FinishCapacityCloudSync(ctx context.Context, org, project, id string, heartbeat func([]byte) error) error {
	st, err := s.syncStore()
	if err != nil {
		return err
	}
	sync, snapshot, err := st.GetSync(ctx, org, project, id)
	if err != nil {
		return err
	}
	runner, ok := s.cloudSyncRunner.(capacitySyncRunner)
	if !ok {
		return errors.New("capacity Cloud sync runner is unavailable")
	}
	for _, env := range sync.Environments {
		if env.PlanID == "" {
			continue
		}
		status, e := runner.CapacityCloudPlanStatus(ctx, org, env.PlanID)
		if e != nil {
			return e
		}
		if status != "succeeded" {
			sync.Status = cloudprofiles.SyncFailed
			sync.Error = "A Cloud sync deployment ended with status " + status
			return st.SaveSync(ctx, sync, snapshot)
		}
	}
	return s.ReconcileCloudSync(ctx, org, project, id, heartbeat)
}
