package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
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
		if p.RequiredRuntimeProtocol != "" {
			return true, nil
		}
		for _, a := range p.Actions {
			if HasCapacityIntent(a) {
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
func (s *Service) PrepareCapacityCloudSync(ctx context.Context, org, project, id string) (orchestration.CloudSyncPreparation, error) {
	st, err := s.syncStore()
	if err != nil {
		return orchestration.CloudSyncPreparation{}, err
	}
	lock, ok := st.(interface {
		WithSyncLock(context.Context, string, func() error) error
	})
	if !ok {
		return orchestration.CloudSyncPreparation{}, errors.New("cloud sync lock is unavailable")
	}
	var out orchestration.CloudSyncPreparation
	err = lock.WithSyncLock(ctx, id, func() error {
		sync, snapshot, e := st.GetSync(ctx, org, project, id)
		if e != nil {
			return e
		}
		if attempt, bound := orchestration.CloudSyncAttempt(ctx); bound && attempt != sync.Attempt {
			out.Stopped = true
			return nil
		}
		if sync.Status == cloudprofiles.SyncSucceeded || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncReverting || sync.Status == cloudprofiles.SyncFailed {
			out.Stopped = true
			return nil
		}
		fail := func(cause error) error {
			if ctx.Err() != nil || !terminalCapacitySyncError(cause) {
				return cause
			}
			sync.Status = cloudprofiles.SyncFailed
			sync.Error = cause.Error()
			if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
				return saveErr
			}
			out = orchestration.CloudSyncPreparation{Stopped: true}
			return nil
		}
		if checkErr := s.authorizeStoredCloudSync(ctx, snapshot.Actor, sync, snapshot); checkErr != nil {
			return fail(checkErr)
		}
		if checkErr := s.validateSnapshotConnections(ctx, org, snapshot); checkErr != nil {
			return fail(checkErr)
		}
		if checkErr := validateSyncGrants(ctx, st, org, snapshot); checkErr != nil {
			return fail(checkErr)
		}
		runner, ok := s.cloudSyncRunner.(capacitySyncRunner)
		if !ok {
			return errors.New("capacity Cloud sync runner is unavailable")
		}
		plans := map[string]*Plan{}
		for _, env := range snapshot.Environments {
			plan, pe := s.acceptedCloudPlan(ctx, sync, env)
			if pe != nil {
				return fail(pe)
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
				return fail(se)
			}
			env.PlanID = plan.ID
			env.DeploymentID = dep
			if dep != "" {
				out.Deployments = append(out.Deployments, orchestration.CloudSyncDeployment{PlanID: plan.ID, DeploymentID: dep, Steps: steps})
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
	lock, ok := st.(interface {
		WithSyncLock(context.Context, string, func() error) error
	})
	if !ok {
		return errors.New("cloud sync lock is unavailable")
	}
	return lock.WithSyncLock(ctx, id, func() error { return s.finishCapacityCloudSync(ctx, st, org, project, id, heartbeat) })
}
func (s *Service) finishCapacityCloudSync(ctx context.Context, st cloudprofiles.SyncStore, org, project, id string, heartbeat func([]byte) error) error {
	sync, snapshot, err := st.GetSync(ctx, org, project, id)
	if err != nil {
		return err
	}
	if attempt, bound := orchestration.CloudSyncAttempt(ctx); bound && attempt != sync.Attempt {
		return nil
	}
	switch sync.Status {
	case cloudprofiles.SyncSucceeded, cloudprofiles.SyncReverted, cloudprofiles.SyncReverting, cloudprofiles.SyncFailed:
		return nil
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
	return s.reconcileCloudSync(ctx, st, org, project, id, heartbeat)
}

// Only known authority/review rejections settle the business operation. Store,
// lock and unexpected adapter errors remain retryable without changing status.
func terminalCapacitySyncError(err error) bool {
	for _, terminal := range []error{security.ErrForbidden, security.ErrUnauthenticated, security.ErrNoOrganization, security.ErrOrgMismatch, security.ErrNotMember, apikeys.ErrRejected, cloudprofiles.ErrConflict, cloudprofiles.ErrInvalid, cloudprofiles.ErrNotFound, connections.ErrNotFound, connections.ErrInvalid, ErrNotFound} {
		if errors.Is(err, terminal) {
			return true
		}
	}
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &shape)
}
