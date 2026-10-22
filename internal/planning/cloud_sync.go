package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// AppliedInputReader verifies access to encrypted variables and source artifacts.
// deployments.Runner implements it using its existing Vault and artifact adapters.
type AppliedInputReader interface {
	CheckAppliedInputs(context.Context, string, string, *ServiceConfig) error
}

// CloudSyncWorkflow schedules durable migration reconciliation.
type CloudSyncWorkflow interface {
	StartCloudSync(context.Context, string, string, string, int) error
}

// CloudSyncPlanStore persists a reviewed, narrowly authorized migration plan.
type CloudSyncPlanStore interface {
	PutCloudSyncPlan(context.Context, *Plan) error
}

func (s *Service) syncStore() (cloudprofiles.SyncStore, error) {
	st, ok := s.cloudProfiles.(cloudprofiles.SyncStore)
	if !ok {
		return nil, errors.New("the Cloud sync storage is unavailable")
	}
	return st, nil
}
func (s *Service) authorizeCloudSync(ctx context.Context, p security.Principal, project string) error {
	if project == "" {
		return s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	for _, permission := range []security.Permission{security.DeploymentApprove} {
		if err := s.authz.Require(ctx, p, permission, project); err != nil {
			return err
		}
	}
	_, err := s.projects.GetProject(ctx, p.OrgID, project)
	return err
}

// PreviewCloudChange freezes the serving release and validates the proposed placement.
func (s *Service) PreviewCloudChange(ctx context.Context, p security.Principal, project string, in cloudprofiles.ChangeInput) (*cloudprofiles.Preview, error) {
	return s.previewCloudChange(ctx, p, project, in, "")
}
func (s *Service) previewCloudChange(ctx context.Context, p security.Principal, project string, in cloudprofiles.ChangeInput, reverts string) (*cloudprofiles.Preview, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	selected := 0
	for _, set := range []bool{in.TargetID != "", in.EnvironmentID != "", in.Default} {
		if set {
			selected++
		}
	}
	if selected != 1 || in.Revision < 0 || in.Default && in.CloudID == "" {
		return nil, cloudprofiles.ErrInvalid
	}
	id, err := typeid.Generate("cpv")
	if err != nil {
		return nil, err
	}
	preview := &cloudprofiles.Preview{ID: id.String(), OrgID: p.OrgID, ProjectID: project, Input: in, Reverts: reverts, CreatedAt: s.now().UTC(), Environments: []cloudprofiles.EnvironmentChange{}, Blockers: []string{}}
	snapshot := &cloudprofiles.Snapshot{Actor: p, Environments: []cloudprofiles.EnvironmentSnapshot{}, ProfileRevisions: map[string]int64{}, TargetRevisions: map[string]int64{}}
	options, err := s.connectionOptions(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if in.Shared() {
		if authErr := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); authErr != nil {
			return nil, authErr
		}
		snapshot.Shared = true
	}
	var desired *cloudprofiles.Assignment
	switch {
	case in.TargetID != "":
		before, e := st.GetTarget(ctx, p.OrgID, "", in.CloudID, in.TargetID)
		if e != nil {
			return nil, e
		}
		if before.Revision != in.Revision {
			return nil, cloudprofiles.ErrConflict
		}
		after, targetErr := changedSyncTarget(*before, in)
		if targetErr != nil {
			return nil, targetErr
		}
		after.Revision++
		after.UpdatedAt = preview.CreatedAt
		preview.Before, preview.After = before, &after
		profile, e := st.GetProfile(ctx, p.OrgID, "", in.CloudID)
		if e != nil {
			return nil, e
		}
		snapshot.ProfileRevisions[profile.ID] = profile.Revision
		snapshot.TargetRevisions[before.ID] = before.Revision

		if err = s.validateSyncTarget(ctx, after, options); err != nil {
			preview.Blockers = append(preview.Blockers, err.Error())
		}
		snapshot.Connections = append(snapshot.Connections, before.ConnectionID, after.ConnectionID)
	case in.Default:
		if switchErr := s.previewDefaultSwitch(ctx, st, p, in, preview, snapshot, options); switchErr != nil {
			return nil, switchErr
		}
	case in.CloudID != "":
		profile, e := st.GetProfile(ctx, p.OrgID, project, in.CloudID)
		if e != nil {
			return nil, e
		}
		targets, e := st.ListTargets(ctx, p.OrgID, project, in.CloudID)
		if e != nil {
			return nil, e
		}
		desired = &cloudprofiles.Assignment{OrgID: p.OrgID, ProjectID: project, EnvironmentID: in.EnvironmentID, CloudID: profile.ID, CloudRevision: profile.Revision, Revision: in.Revision + 1, DefaultPlacement: profile.DefaultPlacement}
		for _, t := range targets {
			desired.Targets = append(desired.Targets, *t)
		}
	}
	var environments []*projects.Environment
	if snapshot.Shared {
		shared, ok := st.(interface {
			AssignedEnvironments(context.Context, string, string) ([]*projects.Environment, error)
		})
		if !ok {
			return nil, cloudprofiles.ErrInvalid
		}
		environments, err = shared.AssignedEnvironments(ctx, p.OrgID, in.CloudID)
	} else {
		environments, err = s.projects.ListEnvironments(ctx, p.OrgID, project)
	}
	if err != nil {
		return nil, err
	}
	if reverts != "" {
		original, retained, e := st.GetSync(ctx, p.OrgID, project, reverts)
		if e != nil {
			return nil, e
		}
		snapshot.Shared = retained.Shared
		allowed := map[string]bool{}
		for _, env := range retained.Environments {
			allowed[cloudprofiles.ParticipantProject(env.ProjectID, original.ProjectID)+"/"+env.EnvironmentID] = true
		}
		environments = slices.DeleteFunc(environments, func(e *projects.Environment) bool { return !allowed[e.ProjectID+"/"+e.ID] })
	}
	var planned []cloudinfra.PlannedPlacement
	found := in.EnvironmentID == ""
	for _, env := range environments {
		project := env.ProjectID
		assignment, e := st.Resolve(ctx, p.OrgID, project, env.ID)
		if e != nil && in.TargetID == "" {
			if retained, ok := st.(interface {
				RetainedAssignment(context.Context, string, string, string) (*cloudprofiles.Assignment, error)
			}); ok {
				assignment, e = retained.RetainedAssignment(ctx, p.OrgID, project, env.ID)
			}
		}
		if e != nil {
			return nil, e
		}
		if in.Shared() && (assignment == nil || assignment.CloudID != in.CloudID) {
			continue
		}
		if in.EnvironmentID != "" && env.ID != in.EnvironmentID {
			continue
		}
		found = true
		generation, e := st.AssignmentGeneration(ctx, p.OrgID, project, env.ID)
		if e != nil {
			return nil, e
		}
		after := desired
		switch {
		case in.TargetID != "":
			copyAssignment := *assignment
			copyAssignment.Targets = slices.Clone(assignment.Targets)
			for i, t := range copyAssignment.Targets {
				if t.ID == in.TargetID {
					copyAssignment.Targets[i] = *preview.After
				}
			}
			after = &copyAssignment
		case in.Default:
			// The grant check below drops the default for a target-limited grant.
			copyAssignment := *assignment
			copyAssignment.CloudRevision = in.Revision + 1
			copyAssignment.DefaultPlacement = nil
			if preview.AfterDefault != nil {
				proposed := *preview.AfterDefault
				copyAssignment.DefaultPlacement = &proposed
			}
			after = &copyAssignment
		default:
			if generation != in.Revision {
				return nil, cloudprofiles.ErrConflict
			}
			preview.BeforeAssignment, preview.AfterAssignment = assignment, after
		}
		if after != nil {
			if grants, ok := st.(cloudprofiles.GrantStore); ok {
				g, grantErr := grants.GetProjectGrant(ctx, p.OrgID, after.CloudID, project)
				if grantErr != nil {
					return nil, grantErr
				}
				if g.Revoked {
					return nil, cloudprofiles.ErrNotFound
				}
				if after.GrantRevision > 0 && after.GrantRevision != g.Revision {
					return nil, cloudprofiles.ErrConflict
				}
				for _, target := range after.Targets {
					if len(g.TargetIDs) > 0 && !slices.Contains(g.TargetIDs, target.ID) {
						return nil, cloudprofiles.ErrConflict
					}
				}
				// Like Resolve: only a grant covering the whole Cloud reaches its default.
				if len(g.TargetIDs) > 0 {
					after.DefaultPlacement = nil
				}

				after.GrantRevision = g.Revision
				snapshot.Grants = append(snapshot.Grants, *g)
			}
		}
		for _, pin := range []*cloudprofiles.Assignment{assignment, after} {
			if pin == nil {
				continue
			}
			snapshot.ProfileRevisions[pin.CloudID] = pin.CloudRevision
			for _, target := range pin.Targets {
				revision := target.Revision
				if preview.Before != nil && target.ID == preview.Before.ID {
					revision = preview.Before.Revision
				}
				snapshot.TargetRevisions[target.ID] = revision
			}
		}
		if in.Default {
			// Acceptance compares the Cloud as reviewed, before its default changes.
			snapshot.ProfileRevisions[in.CloudID] = in.Revision
		}
		raw, e := st.SnapshotBindings(ctx, p.OrgID, env.ID)
		if e != nil {
			return nil, e
		}
		// Decode the exact SQL snapshot used at acceptance, avoiding separate reads.
		var bindings []*Binding
		if bindings, e = decodeCloudBindings(raw); e != nil {
			return nil, e
		}
		plan := &Plan{ID: preview.ID + "/" + env.ID, OrgID: p.OrgID, ProjectID: project, EnvironmentID: env.ID, Environment: env.Name, CreatedBy: p.UserID, CreatedAt: preview.CreatedAt, Status: StatusApproved, DecidedBy: p.UserID}
		poolAssignment := after
		if in.Default {
			// A default is never a pool, and a default switch moves nothing pooled.
			poolAssignment = nil
		}
		poolPlacements, poolErr := s.syncPoolPlacements(ctx, plan, bindings, poolAssignment, preview.Before, options, planned)
		if poolErr != nil {
			preview.Blockers = append(preview.Blockers, env.Name+": "+poolErr.Error())
		}
		routes, routeErr := s.releaseRoutes(ctx, plan)
		if routeErr != nil {
			preview.Blockers = append(preview.Blockers, env.Name+": "+routeErr.Error())
		}
		selector := in.TargetID
		if in.Default {
			selector = defaultPlacementSelector
		}
		result := appliedCloudPlan(bindings, after, selector, options, s.providers, poolPlacements, routes)
		if routeErr = s.reserveRoutedPlacements(ctx, plan, result, bindings, routePlacementReview{Prior: planned, Before: preview.Before}); routeErr != nil {
			preview.Blockers = append(preview.Blockers, env.Name+": "+routeErr.Error())
		}
		for _, action := range result.Actions {
			if action.Service != nil && action.Service.CloudPlacement != nil && action.Service.CloudPlacement.PoolID != "" && action.Op != Delete && (action.Op != Keep || action.AdoptCloudPlacement) {
				planned = append(planned, cloudinfra.PlannedPlacement{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Service: action.Name, Placement: action.Service.CloudPlacement.PoolPlacement()})
			}
		}
		plan.setResult(result)
		for _, finding := range result.Problems {
			preview.Blockers = append(preview.Blockers, env.Name+": "+finding.Name+": "+finding.Message)
		}
		change := cloudprofiles.EnvironmentChange{ProjectID: project, EnvironmentID: env.ID, Environment: env.Name, Services: []string{}, Status: cloudprofiles.SyncPending}
		change.Summary = cloudprofiles.PlanSummary{Create: plan.Summary.Create, Update: plan.Summary.Update, Replace: plan.Summary.Replace, Delete: plan.Summary.Delete, Keep: plan.Summary.Keep}
		for _, a := range plan.Actions {
			if a.AdoptCloudPlacement {
				change.Summary.Adopt++
			}
			if a.Op == Keep && !a.AdoptCloudPlacement {
				continue
			}
			change.Services = append(change.Services, a.Name)
			if a.Op == Replace && a.Service.Public && len(a.Routes) == 0 {
				change.ExternalRouting = true
			}
			if plan.ManifestID == "" {
				for _, b := range bindings {
					if b.Name == a.Name {
						original, planErr := s.store.GetPlan(ctx, p.OrgID, b.PlanID)
						if planErr != nil {
							preview.Blockers = append(preview.Blockers, a.Name+": original applied plan is unavailable")
							break
						}
						plan.ManifestID, plan.ManifestRevision = original.ManifestID, original.ManifestRevision
						break
					}
				}
			}
			if a.Op != Keep && a.Service.Gateway == nil {
				if s.appliedInputs == nil {
					preview.Blockers = append(preview.Blockers, a.Name+": applied input verification is unavailable")
				} else if e = s.appliedInputs.CheckAppliedInputs(ctx, p.OrgID, env.ID, a.Service); e != nil {
					preview.Blockers = append(preview.Blockers, a.Name+": "+e.Error())
				}
			}
		}
		refs := snapshotConnections(bindings, plan)
		snapshot.Connections = append(snapshot.Connections, refs...)
		plan.Revision, e = Digest(plan.Actions)
		if e != nil {
			return nil, e
		}
		encoded, e := json.Marshal(plan)
		if e != nil {
			return nil, e
		}
		routeSnapshot, e := json.Marshal(routes)
		if e != nil {
			return nil, e
		}
		if string(routeSnapshot) == "null" {
			routeSnapshot = []byte("[]")
		}
		snapshot.Environments = append(snapshot.Environments, cloudprofiles.EnvironmentSnapshot{Routes: routeSnapshot, ProjectID: project, EnvironmentID: env.ID, AssignmentRevision: generation, Bindings: raw, Plan: encoded})
		preview.Environments = append(preview.Environments, change)
	}
	if !found {
		return nil, cloudprofiles.ErrNotFound
	}
	snapshot.ConnectionIdentities = map[string]string{}
	for _, id := range snapshot.Connections {
		for _, option := range options {
			if option.ID == id {
				snapshot.ConnectionIdentities[id] = connections.AccountIdentity(option.Cloud, option.Account, option.Details)
			}
		}
	}
	slices.Sort(snapshot.Connections)
	snapshot.Connections = slices.Compact(snapshot.Connections)
	if filterErr := s.filterCloudPreviewAssignments(ctx, p, preview); filterErr != nil {
		return nil, filterErr
	}
	preview.Revision, err = Digest(struct {
		Preview  *cloudprofiles.Preview
		Snapshot *cloudprofiles.Snapshot
	}{preview, snapshot})
	if err != nil {
		return nil, err
	}
	if err := st.SavePreview(ctx, preview, snapshot); err != nil {
		return nil, err
	}
	return preview, nil
}

func (s *Service) validateSyncTarget(ctx context.Context, t cloudprofiles.Target, options []ConnectionOption) error {
	if t.Runtime == "server" {
		if t.Provider != "server" || t.PoolID == "" || t.ConnectionID != "" {
			return cloudprofiles.ErrInvalid
		}
		reader, ok := s.store.(interface {
			ValidateSyncPool(context.Context, string, string, string) error
		})
		if !ok {
			return errors.New("pool inventory is unavailable")
		}
		return reader.ValidateSyncPool(ctx, t.OrgID, t.CloudID, t.PoolID)
	}
	prov, err := s.providers.Get(t.Provider)
	if err != nil {
		return err
	}
	if !prov.Available {
		return fmt.Errorf("provider %s is unavailable", t.Provider)
	}
	i := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == t.ConnectionID })
	if i < 0 || !options[i].Ready || options[i].Cloud != prov.Cloud {
		return errors.New("the selected connection is not ready for this provider")
	}
	if connections.AccountIdentity(options[i].Cloud, options[i].Account, options[i].Details) == "" {
		return errors.New("the selected account identity is unverified")
	}
	if t.Region != "" && prov.Placed() && !prov.HasRegion(t.Region) {
		return errors.New("the provider does not support this region")
	}
	return nil
}

// ApplyCloudChange atomically accepts only the immutable reviewed placement.
func (s *Service) ApplyCloudChange(ctx context.Context, p security.Principal, project, previewID, revision, key string) (*cloudprofiles.Sync, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	preview, snapshot, err := st.GetPreview(ctx, p.OrgID, project, previewID)
	if err != nil {
		return nil, err
	}
	if preview.Input.Shared() {
		if authErr := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); authErr != nil {
			return nil, authErr
		}
	}
	if preview.Revision != revision {
		return nil, cloudprofiles.ErrConflict
	}
	if len(preview.Blockers) > 0 {
		return nil, cloudprofiles.ErrInvalid
	}
	if replay, ok := st.(interface {
		FindAccepted(context.Context, string, string, string, string) (*cloudprofiles.Sync, error)
	}); ok {
		accepted, e := replay.FindAccepted(ctx, p.OrgID, project, key, previewID)
		if e == nil {
			return accepted, nil
		}
		if !errors.Is(e, cloudprofiles.ErrNotFound) {
			return nil, e
		}
	}
	// Revalidate account identity against every reviewed action, including adoption.
	if connErr := s.validateSnapshotConnections(ctx, p.OrgID, snapshot); connErr != nil {
		return nil, connErr
	}
	for _, env := range snapshot.Environments {
		var plan Plan
		if decodeErr := json.Unmarshal(env.Plan, &plan); decodeErr != nil {
			return nil, decodeErr
		}
		for _, action := range plan.Actions {
			if action.Op == Keep || action.Service == nil || action.Service.Gateway != nil {
				continue
			}
			if s.appliedInputs == nil {
				return nil, errors.New("applied input verification is unavailable")
			}
			if inputErr := s.appliedInputs.CheckAppliedInputs(ctx, p.OrgID, env.EnvironmentID, action.Service); inputErr != nil {
				return nil, inputErr
			}
		}
	}
	id, err := typeid.Generate("csync")
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	sync := &cloudprofiles.Sync{ID: id.String(), OrgID: p.OrgID, ProjectID: project, CloudID: preview.Input.CloudID, TargetID: preview.Input.TargetID, Default: preview.Input.Default, BeforeDefault: preview.BeforeDefault, AfterDefault: preview.AfterDefault, PreviewID: previewID, Status: cloudprofiles.SyncPending, CreatedBy: p.UserID, Actor: p, Before: preview.Before, After: preview.After, Environments: slices.Clone(preview.Environments), Attempt: 1, CreatedAt: now, UpdatedAt: now, Reverts: preview.Reverts}
	snapshot.Actor = p
	if planErr := prepareAcceptedPlans(sync, snapshot); planErr != nil {
		return nil, planErr
	}
	sync, err = st.Accept(ctx, preview, snapshot, sync, key)
	if err != nil {
		return nil, err
	}
	if s.cloudSyncWorkflow != nil {
		if e := s.cloudSyncWorkflow.StartCloudSync(ctx, sync.OrgID, sync.ProjectID, sync.ID, sync.Attempt); e != nil {
			s.warn("Cloud sync is pending recovery", e)
		}
	}
	s.recordCloudSync(ctx, p, sync, "cloud_sync.apply")
	return sync, nil
}

func (s *Service) validateSnapshotConnections(ctx context.Context, org string, snapshot *cloudprofiles.Snapshot) error {
	options, err := s.connectionOptions(ctx, org)
	if err != nil {
		return err
	}
	for id, expected := range snapshot.ConnectionIdentities {
		found := false
		for _, option := range options {
			if option.ID == id {
				found = true
				if expected != "" && connections.AccountIdentity(option.Cloud, option.Account, option.Details) != expected {
					return fmt.Errorf("reviewed connection %s changed its account", id)
				}
			}
		}
		if !found {
			return fmt.Errorf("reviewed connection %s is unavailable", id)
		}
	}
	for _, env := range snapshot.Environments {
		var plan Plan
		if err := json.Unmarshal(env.Plan, &plan); err != nil {
			return err
		}
		for _, a := range plan.Actions {
			if a.Op == Keep && !a.AdoptCloudPlacement || a.Service == nil || a.Service.CloudPlacement == nil {
				continue
			}
			pin := a.Service.CloudPlacement
			i := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == pin.ConnectionID })
			if i < 0 || !options[i].Ready || connections.AccountIdentity(options[i].Cloud, options[i].Account, options[i].Details) != pin.AccountIdentity {
				return fmt.Errorf("reviewed account for %s is unavailable or changed", a.Name)
			}
		}
	}
	return nil
}

// CloudSync reads one project's operation.
func (s *Service) CloudSync(ctx context.Context, p security.Principal, project, id string) (*cloudprofiles.Sync, error) {
	if err := s.authorizeCloudRead(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	sync, err := st.GetSyncView(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if filterErr := s.filterCloudSync(ctx, p, sync, project); filterErr != nil {
		return nil, filterErr
	}
	return sync, nil
}

// CloudSyncs reads project operation history.
func (s *Service) CloudSyncs(ctx context.Context, p security.Principal, project string) ([]*cloudprofiles.Sync, error) {
	if err := s.authorizeCloudRead(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	syncs, err := st.ListSyncs(ctx, p.OrgID, project)
	for _, sync := range syncs {
		if filterErr := s.filterCloudSync(ctx, p, sync, project); filterErr != nil {
			return nil, filterErr
		}
	}
	return syncs, err
}

// RetryCloudSync authorizes remaining work with the current caller.
func (s *Service) RetryCloudSync(ctx context.Context, p security.Principal, project, id string) (*cloudprofiles.Sync, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	sync, snapshot, err := st.GetSync(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if authErr := s.authorizeStoredCloudSync(ctx, p, sync, snapshot); authErr != nil {
		return nil, authErr
	}
	if sync.Status != cloudprofiles.SyncFailed && sync.Status != cloudprofiles.SyncCleanup {
		return nil, cloudprofiles.ErrConflict
	}
	snapshot.Actor = p
	sync.Actor = p
	sync.Attempt++
	sync.Status = cloudprofiles.SyncPending
	sync.Error = ""
	if err := st.SaveSync(ctx, sync, snapshot); err != nil {
		return nil, err
	}
	if s.cloudSyncWorkflow != nil {
		if e := s.cloudSyncWorkflow.StartCloudSync(ctx, sync.OrgID, project, id, sync.Attempt); e != nil {
			s.warn("Cloud sync retry is pending recovery", e)
		}
	}
	s.recordCloudSync(ctx, p, sync, "cloud_sync.retry")
	return sync, nil
}

// CompleteCloudRouting records the operator's explicit external-route evidence.
func (s *Service) CompleteCloudRouting(ctx context.Context, p security.Principal, project, id, environment, evidence string) (*cloudprofiles.Sync, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	if strings.TrimSpace(evidence) == "" || len(evidence) > 2000 {
		return nil, cloudprofiles.ErrInvalid
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	sync, snapshot, err := st.GetSync(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if authErr := s.authorizeStoredCloudSync(ctx, p, sync, snapshot); authErr != nil {
		return nil, authErr
	}
	if sync.Status != cloudprofiles.SyncRouting {
		return nil, cloudprofiles.ErrConflict
	}
	i := slices.IndexFunc(sync.Environments, func(e cloudprofiles.EnvironmentChange) bool { return e.EnvironmentID == environment })
	if i < 0 || !sync.Environments[i].ExternalRouting {
		return nil, cloudprofiles.ErrNotFound
	}
	sync.Environments[i].RoutingEvidence = strings.TrimSpace(evidence)
	snapshot.Actor = p
	sync.Actor = p
	sync.Status = cloudprofiles.SyncPending
	sync.Attempt++
	if err := st.SaveSync(ctx, sync, snapshot); err != nil {
		return nil, err
	}
	if s.cloudSyncWorkflow != nil {
		if e := s.cloudSyncWorkflow.StartCloudSync(ctx, sync.OrgID, project, id, sync.Attempt); e != nil {
			s.warn("Cloud cleanup is pending recovery", e)
		}
	}
	s.recordCloudSync(ctx, p, sync, "cloud_sync.routing")
	return sync, nil
}

func decodeCloudBindings(raw []byte) ([]*Binding, error) {
	var rows []struct {
		Binding
		Replaced []Binding `json:"replaced"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]*Binding, 0, len(rows))
	for _, row := range rows {
		binding := row.Binding
		binding.Replaced = row.Replaced
		out = append(out, &binding)
	}
	return out, nil
}

// CloudSyncPage returns persisted operations after enforcing the full project scope.
func (s *Service) CloudSyncPage(ctx context.Context, p security.Principal, project string, before page.Cursor, limit int) ([]*cloudprofiles.Sync, page.Cursor, error) {
	if err := s.authorizeCloudRead(ctx, p, project); err != nil {
		return nil, page.Cursor{}, err
	}
	if project != "" {
		if _, err := s.projects.GetProject(ctx, p.OrgID, project); err != nil {
			return nil, page.Cursor{}, err
		}
	}
	st, ok := s.cloudProfiles.(interface {
		ListSyncPage(context.Context, string, string, page.Cursor, int) ([]*cloudprofiles.Sync, page.Cursor, error)
	})
	if !ok {
		return nil, page.Cursor{}, errors.New("cloud sync pagination unavailable")
	}
	syncs, next, err := st.ListSyncPage(ctx, p.OrgID, project, before, limit)
	for _, sync := range syncs {
		if filterErr := s.filterCloudSync(ctx, p, sync, project); filterErr != nil {
			return nil, page.Cursor{}, filterErr
		}
	}
	return syncs, next, err
}

func (s *Service) recordCloudSync(ctx context.Context, p security.Principal, sync *cloudprofiles.Sync, action string) {
	if s.audit != nil {
		attributes := map[string]any{"sync_id": sync.ID, "status": sync.Status, "cloud_id": sync.CloudID, "target_id": sync.TargetID, "attempt": sync.Attempt, "reverts": sync.Reverts}
		if sync.Default {
			attributes["default"] = true
			attributes["before_default"], attributes["after_default"] = defaultAttributes(sync.BeforeDefault), defaultAttributes(sync.AfterDefault)
		}
		s.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: sync.ProjectID, Principal: p, Attributes: attributes})
	}
}

// CloudPreview reads an immutable review without returning its private snapshot.
func (s *Service) CloudPreview(ctx context.Context, p security.Principal, project, id string) (*cloudprofiles.Preview, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	preview, _, err := st.GetPreview(ctx, p.OrgID, project, id)
	if err == nil && preview.Input.Shared() {
		err = s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	if err == nil {
		err = s.filterCloudPreviewAssignments(ctx, p, preview)
	}
	return preview, err
}

// Assignment references survive revocation for removal and compensation. Target
// configuration is visible only through the current grant; retained full state
// stays in the private comparison snapshot and cleanup resolver.
func (s *Service) filterCloudPreviewAssignments(ctx context.Context, p security.Principal, preview *cloudprofiles.Preview) error {
	if s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID) == nil {
		return nil
	}
	for _, field := range []**cloudprofiles.Assignment{&preview.BeforeAssignment, &preview.AfterAssignment} {
		if *field == nil {
			continue
		}
		assignment := **field
		assignment.Targets = []cloudprofiles.Target{}
		if grants, ok := s.cloudProfiles.(cloudprofiles.GrantStore); ok {
			grant, err := grants.GetProjectGrant(ctx, p.OrgID, assignment.CloudID, preview.ProjectID)
			if err != nil && !errors.Is(err, cloudprofiles.ErrNotFound) {
				return err
			}
			if err == nil && !grant.Revoked {
				for _, target := range (*field).Targets {
					if len(grant.TargetIDs) == 0 || slices.Contains(grant.TargetIDs, target.ID) {
						assignment.Targets = append(assignment.Targets, target)
					}
				}
			}
		}
		*field = &assignment
	}
	return nil
}

func (s *Service) authorizeCloudRead(ctx context.Context, p security.Principal, project string) error {
	if project == "" {
		return s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	return s.authz.Require(ctx, p, security.DeploymentRead, project)
}
func (s *Service) filterCloudSync(ctx context.Context, p security.Principal, sync *cloudprofiles.Sync, project string) error {
	if project == "" || s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID) == nil {
		return nil
	}
	sync.Environments = slices.DeleteFunc(sync.Environments, func(e cloudprofiles.EnvironmentChange) bool {
		return cloudprofiles.ParticipantProject(e.ProjectID, sync.ProjectID) != project
	})
	// Shared failures can contain another project's service error.
	if sync.TargetID != "" || sync.Default {
		sync.Error = ""
	}
	if sync.Default {
		// Like Resolve: a project sees the default only through a whole-Cloud grant.
		grant, err := s.projectGrant(ctx, p.OrgID, sync.CloudID, project)
		if err != nil {
			return err
		}
		if grant == nil || grant.Revoked || len(grant.TargetIDs) > 0 {
			sync.BeforeDefault, sync.AfterDefault = nil, nil
		}
		return nil
	}
	if sync.TargetID == "" && sync.Before == nil && sync.After == nil {
		return nil
	}
	var grant *cloudprofiles.ProjectGrant
	if grants, ok := s.cloudProfiles.(cloudprofiles.GrantStore); ok {
		var err error
		grant, err = grants.GetProjectGrant(ctx, p.OrgID, sync.CloudID, project)
		if err != nil && !errors.Is(err, cloudprofiles.ErrNotFound) {
			return err
		}
		if err != nil {
			grant = nil
		}
	}
	allowed := func(id string) bool {
		return grant != nil && !grant.Revoked && id != "" && (len(grant.TargetIDs) == 0 || slices.Contains(grant.TargetIDs, id))
	}
	if !allowed(sync.TargetID) {
		sync.TargetID = ""
		sync.Before, sync.After = nil, nil
	} else {
		if sync.Before != nil && !allowed(sync.Before.ID) {
			sync.Before = nil
		}
		if sync.After != nil && !allowed(sync.After.ID) {
			sync.After = nil
		}
	}
	return nil
}
