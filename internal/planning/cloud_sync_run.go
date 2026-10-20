package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// CloudSyncDeployer uses the existing runner for both apply and cleanup.
type CloudSyncDeployer interface {
	ReconcileCloudPlan(context.Context, string, string, func([]byte) error) (string, error)
	CleanupCloudPlan(context.Context, string, string, func([]byte) error) error
}

// BindCloudSyncRunner resolves the planning/runner dependency after construction.
func (s *Service) BindCloudSyncRunner(runner CloudSyncDeployer, inputs AppliedInputReader) {
	s.cloudSyncRunner = runner
	s.appliedInputs = inputs
}

// ValidateCloudSync checks the current actor and the exact stored plan before
// every runner mutation, including cleanup and metadata-only adoption.
func (s *Service) ValidateCloudSync(ctx context.Context, plan *Plan) error {
	st, err := s.syncStore()
	if err != nil {
		return err
	}
	sync, snapshot, err := st.GetSync(ctx, plan.OrgID, plan.ProjectID, plan.CloudSyncID)
	if err != nil {
		return err
	}
	if sync.Status != cloudprofiles.SyncRunning && sync.Status != cloudprofiles.SyncCleanup {
		return cloudprofiles.ErrConflict
	}
	if err := s.authorizeCloudSync(ctx, snapshot.Actor, plan.ProjectID); err != nil {
		return err
	}
	for _, env := range snapshot.Environments {
		if env.EnvironmentID != plan.EnvironmentID {
			continue
		}
		var expected Plan
		if err := json.Unmarshal(env.Plan, &expected); err != nil {
			return err
		}
		digest, e := Digest(expected.Actions)
		if e != nil {
			return e
		}
		actual, e := Digest(plan.Actions)
		if e != nil {
			return e
		}
		if digest != actual || plan.ID != migrationPlanID(sync.ID, env.EnvironmentID) {
			return cloudprofiles.ErrConflict
		}
		return s.validateSnapshotConnections(ctx, plan.OrgID, snapshot)
	}
	return cloudprofiles.ErrNotFound
}

// ReconcileCloudSync is a durable activity. Stored plans, deployment IDs and
// runner checkpoints make commit/enqueue and provider-operation crashes resumable.
func (s *Service) ReconcileCloudSync(ctx context.Context, org, project, id string, heartbeat func([]byte) error) error {
	st, err := s.syncStore()
	if err != nil {
		return err
	}
	exclusive, ok := st.(interface {
		WithSyncLock(context.Context, string, func() error) error
	})
	if !ok {
		return errors.New("the Cloud sync storage requires an execution lock")
	}
	return exclusive.WithSyncLock(ctx, id, func() error { return s.reconcileCloudSync(ctx, st, org, project, id, heartbeat) })
}
func (s *Service) reconcileCloudSync(ctx context.Context, st cloudprofiles.SyncStore, org, project, id string, heartbeat func([]byte) error) error {
	sync, snapshot, err := st.GetSync(ctx, org, project, id)
	if err != nil {
		return err
	}
	if sync.Status == cloudprofiles.SyncSucceeded || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncRouting || sync.Status == cloudprofiles.SyncFailed {
		return nil
	}
	fail := func(cause error) error {
		if ctx.Err() != nil {
			return cause
		}
		sync.Status = cloudprofiles.SyncFailed
		sync.Error = cause.Error()
		return errors.Join(cause, st.SaveSync(context.WithoutCancel(ctx), sync, snapshot))
	}
	if err = s.authorizeCloudSync(ctx, snapshot.Actor, project); err != nil {
		return fail(err)
	}
	if err = s.validateSnapshotConnections(ctx, org, snapshot); err != nil {
		return fail(err)
	}
	if s.cloudSyncRunner == nil {
		return fail(errors.New("the Cloud sync runner is unavailable"))
	}
	plans, ok := s.store.(CloudSyncPlanStore)
	if !ok {
		return fail(errors.New("the Cloud sync plan storage is unavailable"))
	}
	sync.Status = cloudprofiles.SyncRunning
	if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
		return saveErr
	}
	for i := range sync.Environments {
		env := &sync.Environments[i]
		if env.Status == cloudprofiles.SyncSucceeded {
			continue
		}
		var pinned cloudprofiles.EnvironmentSnapshot
		for _, candidate := range snapshot.Environments {
			if candidate.EnvironmentID == env.EnvironmentID {
				pinned = candidate
			}
		}
		var plan Plan
		if err = json.Unmarshal(pinned.Plan, &plan); err != nil {
			return fail(err)
		}
		if len(env.Services) == 0 {
			env.Status = cloudprofiles.SyncSucceeded
			if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
				return saveErr
			}
			continue
		}
		plan.CloudSyncID = sync.ID
		plan.ID = migrationPlanID(sync.ID, env.EnvironmentID)
		plan.DecidedBy = snapshot.Actor.UserID
		plan.Revision, err = Digest(plan.Actions)
		if err != nil {
			return fail(err)
		}
		if err = plans.PutCloudSyncPlan(ctx, &plan); err != nil {
			return fail(err)
		}
		env.PlanID = plan.ID
		if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
			return saveErr
		}
		env.DeploymentID, err = s.cloudSyncRunner.ReconcileCloudPlan(ctx, org, plan.ID, heartbeat)
		if err != nil {
			env.Status = cloudprofiles.SyncFailed
			env.Error = err.Error()
			return fail(err)
		}
		env.Status = cloudprofiles.SyncSucceeded
		env.Error = ""
		if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
			return saveErr
		}
	}
	for _, env := range sync.Environments {
		if env.ExternalRouting && env.RoutingEvidence == "" {
			sync.Status = cloudprofiles.SyncRouting
			return st.SaveSync(ctx, sync, snapshot)
		}
	}
	sync.Status = cloudprofiles.SyncCleanup
	if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
		return saveErr
	}
	for _, env := range sync.Environments {
		if env.PlanID == "" {
			continue
		}
		if err = s.cloudSyncRunner.CleanupCloudPlan(ctx, org, env.PlanID, heartbeat); err != nil {
			sync.Error = fmt.Sprintf("%s: %v", env.Environment, err)
			return errors.Join(err, st.SaveSync(context.WithoutCancel(ctx), sync, snapshot))
		}
	}
	sync.Status = cloudprofiles.SyncSucceeded
	sync.Error = ""
	return st.SaveSync(ctx, sync, snapshot)
}

// RecoverCloudSyncs re-enqueues committed outbox entries with deterministic IDs.
func (s *Service) RecoverCloudSyncs(ctx context.Context) error {
	st, err := s.syncStore()
	if err != nil {
		return err
	}
	if s.cloudSyncWorkflow == nil {
		return errors.New("the Cloud sync workflow is unavailable")
	}
	pending, err := st.PendingSyncs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, sync := range pending {
		if err = s.cloudSyncWorkflow.StartCloudSync(ctx, sync.OrgID, sync.ProjectID, sync.ID, sync.Attempt); err != nil {
			errs = append(errs, err)
		}
	}
	if releases, ok := st.(interface {
		PendingReleasePlans(context.Context) ([][2]string, error)
	}); ok {
		queued, e := releases.PendingReleasePlans(ctx)
		if e != nil {
			errs = append(errs, e)
		} else if resume, ok := s.cloudSyncWorkflow.(interface {
			ResumeRelease(context.Context, string, string) error
		}); ok {
			for _, ids := range queued {
				if e = resume.ResumeRelease(ctx, ids[0], ids[1]); e != nil {
					errs = append(errs, e)
				}
			}
		}
	}

	return errors.Join(errs...)
}
