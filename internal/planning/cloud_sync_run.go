package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// CloudSyncDeployer uses the existing runner for both apply and cleanup.
type CloudSyncDeployer interface {
	ReconcileCloudPlan(context.Context, string, string, func([]byte) error) (string, error)
	CleanupCloudPlan(context.Context, string, string, func([]byte) error) error
	CleanupRevertedCloudPlan(context.Context, string, string, string, func([]byte) error) error
}

// BindCloudSyncRunner resolves the planning/runner dependency after construction.
func (s *Service) BindCloudSyncRunner(runner CloudSyncDeployer, inputs AppliedInputReader) {
	s.cloudSyncRunner = runner
	s.appliedInputs = inputs
}

// ValidateCloudSyncExecution returns the current actor for an exact accepted plan.
func (s *Service) ValidateCloudSyncExecution(ctx context.Context, plan *Plan) (security.Principal, error) {
	return s.validateCloudSync(ctx, plan, cloudprofiles.SyncRunning)
}

// ValidateCloudSync checks the current actor and exact stored plan before
// candidate effects and metadata-only adoption.
func (s *Service) ValidateCloudSync(ctx context.Context, plan *Plan) error {
	_, err := s.validateCloudSync(ctx, plan, cloudprofiles.SyncRunning)
	return err
}

// ValidateCloudSyncRoutes returns the current actor for accepted handoffs. Route
// admission and workers check current grants before publishing a destination.
func (s *Service) ValidateCloudSyncRoutes(ctx context.Context, plan *Plan) (security.Principal, error) {
	return s.validateCloudSync(ctx, plan, cloudprofiles.SyncRouting)
}

// ValidateCloudSyncSourceCleanup retains authority over accepted old resources.
// It cannot authorize candidate creation or binding publication.
func (s *Service) ValidateCloudSyncSourceCleanup(ctx context.Context, plan *Plan) (security.Principal, error) {
	return s.validateCloudSync(ctx, plan, cloudprofiles.SyncCleanup)
}

func (s *Service) validateCloudSync(ctx context.Context, plan *Plan, phase string) (security.Principal, error) {
	st, err := s.syncStore()
	if err != nil {
		return security.Principal{}, err
	}
	sync, snapshot, err := syncForPlan(ctx, st, plan, plan.CloudSyncID)
	if err != nil {
		return security.Principal{}, err
	}
	if sync.Status != phase {
		return security.Principal{}, cloudprofiles.ErrConflict
	}
	if authErr := s.authorizeStoredCloudSync(ctx, snapshot.Actor, sync, snapshot); authErr != nil {
		return security.Principal{}, authErr
	}
	if sync.Status == cloudprofiles.SyncRunning {
		if grantErr := validateSyncGrants(ctx, st, plan.OrgID, snapshot); grantErr != nil {
			return security.Principal{}, grantErr
		}
	}
	for _, env := range snapshot.Environments {
		if env.EnvironmentID != plan.EnvironmentID || cloudprofiles.ParticipantProject(env.ProjectID, sync.ProjectID) != plan.ProjectID {
			continue
		}
		var expected Plan
		if err := json.Unmarshal(env.Plan, &expected); err != nil {
			return security.Principal{}, err
		}
		digest, e := Digest(expected.Actions)
		if e != nil {
			return security.Principal{}, e
		}
		actual, e := Digest(plan.Actions)
		if e != nil {
			return security.Principal{}, e
		}
		if digest != actual || plan.ID != migrationPlanID(sync.ID, env.EnvironmentID) {
			return security.Principal{}, cloudprofiles.ErrConflict
		}
		if err := s.validateSnapshotConnections(ctx, plan.OrgID, snapshot); err != nil {
			return security.Principal{}, err
		}
		return snapshot.Actor, nil
	}
	return security.Principal{}, cloudprofiles.ErrNotFound
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
	if sync.Status == cloudprofiles.SyncSucceeded || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncReverting || sync.Status == cloudprofiles.SyncFailed {
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
	if err = s.authorizeStoredCloudSync(ctx, snapshot.Actor, sync, snapshot); err != nil {
		return fail(err)
	}
	if err = s.validateSnapshotConnections(ctx, org, snapshot); err != nil {
		return fail(err)
	}
	if slices.ContainsFunc(sync.Environments, func(env cloudprofiles.EnvironmentChange) bool { return env.Status != cloudprofiles.SyncSucceeded }) {
		if err = validateSyncGrants(ctx, st, org, snapshot); err != nil {
			return fail(err)
		}
	}
	if s.cloudSyncRunner == nil {
		return fail(errors.New("the Cloud sync runner is unavailable"))
	}
	// Resolve every participant before the first runtime effect. Legacy pooled
	// reviews must be reverted and reviewed again if no accepted plan exists.
	accepted := map[string]*Plan{}
	for _, env := range snapshot.Environments {
		plan, planErr := s.acceptedCloudPlan(ctx, sync, env)
		if planErr != nil {
			return fail(planErr)
		}
		accepted[env.EnvironmentID] = plan
	}
	applying := slices.ContainsFunc(sync.Environments, func(env cloudprofiles.EnvironmentChange) bool { return env.Status != cloudprofiles.SyncSucceeded })
	if applying {
		sync.Status = cloudprofiles.SyncRunning
	}
	if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
		return saveErr
	}
	for i := range sync.Environments {
		env := &sync.Environments[i]
		if env.Status == cloudprofiles.SyncSucceeded {
			continue
		}
		plan := accepted[env.EnvironmentID]
		if len(env.Services) == 0 {
			env.Status = cloudprofiles.SyncSucceeded
			if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
				return saveErr
			}
			continue
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
	// Runtime success and route readiness are independent. Existing handoffs
	// retain cleanup authority; their workers recheck grants for new effects.
	var routingErr error
	for i := range sync.Environments {
		env := &sync.Environments[i]
		plan := accepted[env.EnvironmentID]
		if !slices.ContainsFunc(plan.Actions, func(a Action) bool { return len(a.Routes) > 0 }) {
			continue
		}
		router, ok := s.cloudSyncRunner.(interface {
			ReconcileCloudPlanRoutes(context.Context, string, string, func(cloudprofiles.SyncRoute) error, func([]byte) error) error
		})
		if !ok {
			return fail(errors.New("the Cloud sync route coordinator is unavailable"))
		}
		sync.Status = cloudprofiles.SyncRouting
		env.RoutingStatus = cloudprofiles.SyncRouting
		if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
			return saveErr
		}
		checkpoint := func(route cloudprofiles.SyncRoute) error {
			index := slices.IndexFunc(env.Routes, func(saved cloudprofiles.SyncRoute) bool { return saved.RouteID == route.RouteID })
			if index < 0 {
				env.Routes = append(env.Routes, route)
			} else {
				env.Routes[index] = route
			}
			return st.SaveSync(ctx, sync, snapshot)
		}
		routeErr := router.ReconcileCloudPlanRoutes(ctx, org, plan.ID, checkpoint, heartbeat)
		if routeErr == nil {
			env.RoutingStatus, env.Error = cloudprofiles.SyncSucceeded, ""
		} else {
			env.Error = routeErr.Error()
			routingErr = errors.Join(routingErr, routeErr)
		}
		if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
			return saveErr
		}
	}
	if routingErr != nil {
		sync.Error = routingErr.Error()
		return errors.Join(routingErr, st.SaveSync(context.WithoutCancel(ctx), sync, snapshot))
	}
	for _, env := range sync.Environments {
		if env.ExternalRouting && env.RoutingEvidence == "" {
			sync.Status = cloudprofiles.SyncRouting
			sync.Error = ""
			return st.SaveSync(ctx, sync, snapshot)
		}
	}
	sync.Status = cloudprofiles.SyncCleanup
	sync.Error = ""
	if saveErr := st.SaveSync(ctx, sync, snapshot); saveErr != nil {
		return saveErr
	}
	for i := range sync.Environments {
		env := &sync.Environments[i]
		if env.PlanID == "" {
			continue
		}
		if err = s.cloudSyncRunner.CleanupCloudPlan(ctx, org, env.PlanID, heartbeat); err != nil {
			if errors.Is(err, cloudinfra.ErrPending) {
				sync.Status = cloudprofiles.SyncRouting
				env.RoutingStatus = cloudprofiles.SyncRouting
			}
			env.Error = err.Error()
			sync.Error = fmt.Sprintf("%s: %v", env.Environment, err)
			return errors.Join(err, st.SaveSync(context.WithoutCancel(ctx), sync, snapshot))
		}
		env.Error = ""
	}
	if sync.Reverts != "" {
		original, _, readErr := st.GetSync(ctx, org, project, sync.Reverts)
		if readErr != nil {
			return readErr
		}
		for _, env := range original.Environments {
			if env.PlanID == "" {
				continue
			}
			if cleanupErr := s.cloudSyncRunner.CleanupRevertedCloudPlan(ctx, org, env.PlanID, sync.ID, heartbeat); cleanupErr != nil {
				sync.Error = fmt.Sprintf("%s unfinished forward operation: %v", env.Environment, cleanupErr)
				return errors.Join(cleanupErr, st.SaveSync(context.WithoutCancel(ctx), sync, snapshot))
			}
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

// ValidateCloudSyncCleanup grants only cleanup of a reverted operation's exact
// stored plans, under the current compensation actor and retained account pins.
func (s *Service) ValidateCloudSyncCleanup(ctx context.Context, plan *Plan, compensationID string) error {
	st, err := s.syncStore()
	if err != nil {
		return err
	}
	compensation, snapshot, err := syncForPlan(ctx, st, plan, compensationID)
	if err != nil {
		return err
	}
	if (compensation.Status != cloudprofiles.SyncCleanup && compensation.Status != cloudprofiles.SyncRunning) || compensation.Reverts != plan.CloudSyncID {
		return cloudprofiles.ErrConflict
	}
	if authErr := s.authorizeStoredCloudSync(ctx, snapshot.Actor, compensation, snapshot); authErr != nil {
		return authErr
	}
	included := false
	for _, env := range snapshot.Environments {
		if env.EnvironmentID == plan.EnvironmentID {
			included = true
		}
	}
	if !included {
		return cloudprofiles.ErrNotFound
	}
	original, retained, err := syncForPlan(ctx, st, plan, plan.CloudSyncID)
	if err != nil {
		return err
	}
	if original.Status != cloudprofiles.SyncReverting {
		return cloudprofiles.ErrConflict
	}
	for _, env := range retained.Environments {
		if env.EnvironmentID != plan.EnvironmentID {
			continue
		}
		var expected Plan
		if decodeErr := json.Unmarshal(env.Plan, &expected); decodeErr != nil {
			return decodeErr
		}
		want, e := Digest(expected.Actions)
		if e != nil {
			return e
		}
		got, e := Digest(plan.Actions)
		if e != nil {
			return e
		}
		if want != got || plan.ID != migrationPlanID(original.ID, env.EnvironmentID) {
			return cloudprofiles.ErrConflict
		}
		return s.validateSnapshotConnections(ctx, plan.OrgID, retained)
	}
	return cloudprofiles.ErrNotFound
}

func syncForPlan(ctx context.Context, st cloudprofiles.SyncStore, plan *Plan, id string) (*cloudprofiles.Sync, *cloudprofiles.Snapshot, error) {
	if reader, ok := st.(interface {
		SyncForParticipant(context.Context, string, string, string, string) (*cloudprofiles.Sync, *cloudprofiles.Snapshot, error)
	}); ok {
		return reader.SyncForParticipant(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID, id)
	}
	return st.GetSync(ctx, plan.OrgID, plan.ProjectID, id)
}
func validateSyncGrants(ctx context.Context, st cloudprofiles.SyncStore, org string, snapshot *cloudprofiles.Snapshot) error {
	if reader, ok := st.(interface {
		ValidateGrants(context.Context, string, []cloudprofiles.ProjectGrant) error
	}); ok {
		return reader.ValidateGrants(ctx, org, snapshot.Grants)
	}
	if len(snapshot.Grants) > 0 {
		return cloudprofiles.ErrInvalid
	}
	return nil
}

func (s *Service) authorizeStoredCloudSync(ctx context.Context, p security.Principal, sync *cloudprofiles.Sync, snapshot *cloudprofiles.Snapshot) error {
	if p.KeyID != "" {
		if s.capacityPrincipal == nil {
			return security.ErrForbidden
		}
		if err := s.capacityPrincipal.ValidatePrincipal(ctx, p); err != nil {
			return err
		}
	}

	if sync.TargetID != "" || snapshot.Shared {
		return s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	return s.authorizeCloudSync(ctx, p, sync.ProjectID)
}

// acceptedCloudPlan reuses admission's exact saved plan. Old managed reviews
// retain their supported recovery path; absent pooled plans need a new review.
func (s *Service) acceptedCloudPlan(ctx context.Context, sync *cloudprofiles.Sync, env cloudprofiles.EnvironmentSnapshot) (*Plan, error) {
	var expected Plan
	if err := json.Unmarshal(env.Plan, &expected); err != nil {
		return nil, err
	}
	if expected.OrgID != sync.OrgID || expected.ProjectID != cloudprofiles.ParticipantProject(env.ProjectID, sync.ProjectID) || expected.EnvironmentID != env.EnvironmentID {
		return nil, cloudprofiles.ErrConflict
	}
	if !slices.ContainsFunc(expected.Actions, func(a Action) bool { return a.Op != Keep || a.AdoptCloudPlacement }) {
		return &expected, nil
	}
	expected.ID = migrationPlanID(sync.ID, env.EnvironmentID)
	expected.CloudSyncID = sync.ID
	revision, err := Digest(expected.Actions)
	if err != nil {
		return nil, err
	}
	expected.Revision = revision
	saved, err := s.store.GetPlan(ctx, sync.OrgID, expected.ID)
	if errors.Is(err, ErrNotFound) && env.AcceptedPlan == nil {
		if slices.ContainsFunc(expected.Actions, func(a Action) bool {
			return a.Service != nil && (a.Service.CapacityIntent != nil || a.Service.CloudPlacement != nil && a.Service.CloudPlacement.PoolID != "")
		}) {
			return nil, fmt.Errorf("%w: this legacy Cloud sync has no accepted pool plan; review and accept a revert before reviewing the target change again", cloudprofiles.ErrConflict)
		}
		plans, ok := s.store.(CloudSyncPlanStore)
		if !ok {
			return nil, errors.New("the Cloud sync plan storage is unavailable")
		}
		if planErr := plans.PutCloudSyncPlan(ctx, &expected); planErr != nil {
			return nil, planErr
		}
		saved, err = s.store.GetPlan(ctx, sync.OrgID, expected.ID)
	}
	if err != nil {
		return nil, err
	}
	actual, err := Digest(saved.Actions)
	if err != nil {
		return nil, err
	}
	if saved.OrgID != sync.OrgID || saved.ProjectID != cloudprofiles.ParticipantProject(env.ProjectID, sync.ProjectID) || saved.EnvironmentID != env.EnvironmentID || saved.ID != expected.ID || saved.CloudSyncID != sync.ID || saved.Revision != revision || actual != revision || saved.ManifestID != expected.ManifestID || saved.ManifestRevision != expected.ManifestRevision || saved.Status != StatusApproved || saved.Executable != expected.Executable {
		return nil, cloudprofiles.ErrConflict
	}
	return saved, nil
}
