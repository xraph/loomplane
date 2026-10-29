package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// rollbackScan is how many of an environment's releases a rollback looks
// back through for one that wasn't rolled back itself.
const rollbackScan = 100

// ReleaseRef names one deployment of an environment.
type ReleaseRef struct {
	DeploymentID string    `json:"deployment_id"`
	PlanID       string    `json:"plan_id"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	FinishedAt   time.Time `json:"finished_at"`
}

// RollbackCommit is what a rollback does to one repository: From is the
// commit the target deployed, "" when it didn't know the repository, and
// To the commit the rollback restores.
type RollbackCommit struct {
	Alias string `json:"alias"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// RollbackTarget is what rolling an environment back would undo, the
// release it would restore, and each repository's commits.
type RollbackTarget struct {
	Environment string           `json:"environment"`
	Target      ReleaseRef       `json:"target"`
	Restore     ReleaseRef       `json:"restore"`
	Commits     []RollbackCommit `json:"commits"`
}

// canaryRevertedError is ErrCanaryReverted, saying what the canary did.
type canaryRevertedError struct{ message string }

func (e *canaryRevertedError) Error() string        { return e.message }
func (e *canaryRevertedError) Is(target error) bool { return target == ErrCanaryReverted }

// canarySettled says why a deployment whose canary ended at status can't
// be rolled back, or returns nil. The statuses are 2a's
// deployments.CanaryRolledBack, CanaryRevertFailed and
// CanaryPromoteFailed, by value, since planning can't import deployments.
func canarySettled(status, environment string) error {
	switch status {
	case "rolled_back":
		return &canaryRevertedError{fmt.Sprintf("the canary already returned %s to the baseline; there is nothing to roll back", environment)}
	case "revert_failed":
		return &canaryRevertedError{"the canary's revert failed; fix it from the deployment before rolling back"}
	case "promote_failed":
		return &canaryRevertedError{"the canary's promotion failed and traffic may still be split; fix it from the deployment before rolling back"}
	}
	return nil
}

// RollbackTarget says what rolling an environment back would do. The
// target is the environment's newest deployment, whatever its status;
// deploymentID, when set, must name it. The release it restores is the
// newest one that finished before the target started and that no
// rollback undid. A rollback's own deployment counts as a release, since
// it restored a state that was approved. Nothing is rolled back while a
// deployment runs, after a canary dealt with the target itself, or when
// the target is a rollback.
func (s *Service) RollbackTarget(ctx context.Context, p security.Principal, projectID, environment, deploymentID string) (*RollbackTarget, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(environment))
	if err != nil {
		return nil, err
	}
	return s.rollbackTarget(ctx, p.OrgID, env, strings.TrimSpace(deploymentID))
}

// rollbackTarget is RollbackTarget without the caller's checks.
func (s *Service) rollbackTarget(ctx context.Context, orgID string, env *projects.Environment, deploymentID string) (*RollbackTarget, error) {
	if s.history == nil {
		return nil, errors.New("rolling back needs the release history, which this server doesn't read")
	}
	target, err := s.history.Latest(ctx, orgID, env.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoPreviousRelease
	}
	if err != nil {
		return nil, err
	}
	// Naming an older deployment would also undo every release after it.
	switch {
	case target.Status == "running" || target.Status == "paused":
		return nil, ErrReleaseRunning
	case deploymentID != "" && deploymentID != target.DeploymentID:
		return nil, fmt.Errorf("%w: deployment %s isn't the newest in %s; roll back the newest release first", ErrInvalid, deploymentID, env.Name)
	}
	if refused := canarySettled(target.CanaryStatus, env.Name); refused != nil {
		return nil, refused
	}
	// Rolling a rollback back would only bring back the release it undid.
	// A failed one is retried: that finishes the move it was approved for.
	switch {
	case target.Kind == KindRollback && target.Status == "failed":
		return nil, fmt.Errorf("%w: the current release is a rollback that failed; retry its deployment %s to finish it, or deploy or promote to move forward", ErrInvalid, target.DeploymentID)
	case target.Kind == KindRollback:
		return nil, fmt.Errorf("%w: the current release is a rollback; deploy or promote to move forward", ErrInvalid)
	}
	undone, err := s.store.RolledBackPlans(ctx, orgID, env.ID)
	if err != nil {
		return nil, err
	}
	releases, err := s.history.Releases(ctx, orgID, env.ID, target.StartedAt, rollbackScan)
	if err != nil {
		return nil, err
	}
	var restore *ReleaseRecord
	for i := range releases {
		if !undone[releases[i].PlanID] {
			restore = &releases[i]
			break
		}
	}
	if restore == nil {
		return nil, ErrNoPreviousRelease
	}
	commits, err := s.rollbackCommits(ctx, orgID, target.PlanID, restore.PlanID)
	if err != nil {
		return nil, err
	}
	return &RollbackTarget{Environment: env.Name, Target: releaseRef(target), Restore: releaseRef(restore), Commits: commits}, nil
}

// rollbackCommits lists, for each repository of the restored plan, the
// commit the target's plan deployed and the one the restored plan did.
// A rollback moves every repository the restored plan pinned.
func (s *Service) rollbackCommits(ctx context.Context, orgID, targetPlanID, restorePlanID string) ([]RollbackCommit, error) {
	target, err := s.store.GetPlan(ctx, orgID, targetPlanID)
	if err != nil {
		return nil, fmt.Errorf("read the plan of the release to roll back: %w", err)
	}
	restore, err := s.store.GetPlan(ctx, orgID, restorePlanID)
	if err != nil {
		return nil, fmt.Errorf("read the plan of the release to restore: %w", err)
	}
	from := map[string]string{}
	for _, src := range target.Sources {
		from[src.SourceID] = src.CommitSHA
	}
	out := make([]RollbackCommit, 0, len(restore.Sources))
	for _, src := range restore.Sources {
		out = append(out, RollbackCommit{Alias: src.Alias, From: from[src.SourceID], To: src.CommitSHA})
	}
	return out, nil
}

func releaseRef(r *ReleaseRecord) ReleaseRef {
	return ReleaseRef{DeploymentID: r.DeploymentID, PlanID: r.PlanID, Kind: r.Kind, Status: r.Status, FinishedAt: r.FinishedAt}
}

// RollbackInput asks to roll an environment back to its previous release.
type RollbackInput struct {
	Environment string `json:"environment"`
	// Reason says why, for the audit trail and whoever approves it. It's
	// required, and at most 2000 characters.
	Reason string `json:"reason"`
	// DeploymentID is the deployment to undo. It must be the
	// environment's newest, which is the default.
	DeploymentID string `json:"deployment_id,omitempty"`
}

// AutomaticRollbackInput is why Loomplane automation rolls a deployment
// back: a post-deploy watch (TriggerWatch) or a canary (TriggerCanary)
// judged it, for Reason.
type AutomaticRollbackInput struct {
	Trigger string
	Reason  string
	WatchID string
}

// Finding codes for rollbacks.
const (
	// codeRollbackNeedsResource blocks a rollback that restores a service
	// needing a resource the release it undoes removed.
	codeRollbackNeedsResource = "rollback-needs-resource"
	// codeRollbackUnavailable blocks a rollback of a service that can't
	// return to what the restored release ran.
	codeRollbackUnavailable = "rollback-unavailable"
)

// automaticTrigger reports whether a rollback trigger is Loomplane's own.
func automaticTrigger(trigger string) bool {
	return trigger == TriggerWatch || trigger == TriggerCanary
}

// automaticRollback reports whether Loomplane automation may approve p: a
// rollback a watch or a canary started.
func automaticRollback(p *Plan) bool {
	return p.Kind == KindRollback && p.Rollback != nil && automaticTrigger(p.Rollback.Trigger)
}

func rollbackReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return "", fmt.Errorf("%w: say why you're rolling back", ErrInvalid)
	case utf8.RuneCountInString(reason) > maxNote:
		return "", fmt.Errorf("%w: the reason can be at most %d characters", ErrInvalid, maxNote)
	}
	return reason, nil
}

// CreateRollback plans an environment back to its previous release: the
// commits and manifest revision that release deployed, with every
// repository moving. It's a plan like any other, approved by a person,
// that changes only services' code and settings (see rollbackFilter).
func (s *Service) CreateRollback(ctx context.Context, p security.Principal, projectID string, in RollbackInput) (*Plan, error) {
	if err := refuseAutomation(p); err != nil {
		return nil, err
	}
	if err := s.authz.Require(ctx, p, security.DeploymentCreate, projectID); err != nil {
		return nil, err
	}
	reason, err := rollbackReason(in.Reason)
	if err != nil {
		return nil, err
	}
	if _, err = s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(in.Environment))
	if err != nil {
		return nil, err
	}
	plan, err := s.rollbackPlan(ctx, p.OrgID, projectID, env, strings.TrimSpace(in.DeploymentID), Rollback{Reason: reason, Trigger: TriggerManual})
	if err != nil {
		return nil, err
	}
	return s.start(ctx, p, plan)
}

// CreateAutomaticRollback is CreateRollback by Loomplane automation, for a
// post-deploy watch or a canary that judged deploymentID bad. It acts as
// security.SystemPrincipal(orgID), whoever approved that deployment, and
// needs deployment:create for it. deploymentID must still be its
// environment's newest deployment.
func (s *Service) CreateAutomaticRollback(ctx context.Context, orgID, deploymentID string, in AutomaticRollbackInput) (*Plan, error) {
	if !automaticTrigger(in.Trigger) {
		return nil, fmt.Errorf("%w: an automatic rollback is triggered by a watch or a canary, not %q", ErrInvalid, in.Trigger)
	}
	reason, err := rollbackReason(in.Reason)
	if err != nil {
		return nil, err
	}
	if s.history == nil {
		return nil, errors.New("rolling back needs the release history, which this server doesn't read")
	}
	deployment, err := s.history.Deployment(ctx, orgID, strings.TrimSpace(deploymentID))
	if err != nil {
		return nil, err
	}
	target, err := s.store.GetPlan(ctx, orgID, deployment.PlanID)
	if err != nil {
		return nil, fmt.Errorf("read the plan of deployment %s: %w", deployment.DeploymentID, err)
	}
	p := security.SystemPrincipal(orgID)
	if authErr := s.authz.Require(ctx, p, security.DeploymentCreate, target.ProjectID); authErr != nil {
		return nil, authErr
	}
	envs, err := s.projects.ListEnvironments(ctx, orgID, target.ProjectID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(envs, func(e *projects.Environment) bool { return e.ID == target.EnvironmentID })
	if i < 0 {
		return nil, fmt.Errorf("%w: deployment %s's environment is gone", projects.ErrEnvironmentNotFound, deployment.DeploymentID)
	}
	plan, err := s.rollbackPlan(ctx, orgID, target.ProjectID, envs[i], deployment.DeploymentID,
		Rollback{Reason: reason, Trigger: in.Trigger, WatchID: strings.TrimSpace(in.WatchID)})
	if err != nil {
		return nil, err
	}
	return s.start(ctx, p, plan)
}

// rollbackPlan is the plan that rolls env back from deploymentID, its
// newest deployment ("" for whichever that is): pinned to what the
// restored release's plan pinned, every repository moving, with that
// plan's manifest revision.
func (s *Service) rollbackPlan(ctx context.Context, orgID, projectID string, env *projects.Environment, deploymentID string, rb Rollback) (*Plan, error) {
	target, err := s.rollbackTarget(ctx, orgID, env, deploymentID)
	if err != nil {
		return nil, err
	}
	restore, err := s.store.GetPlan(ctx, orgID, target.Restore.PlanID)
	if err != nil {
		return nil, fmt.Errorf("read the plan of the release to restore: %w", err)
	}
	if restore.ProjectID != projectID || restore.EnvironmentID != env.ID {
		return nil, fmt.Errorf("%w: plan %s, which the restored release deployed, isn't %s's", ErrInvalid, restore.ID, env.Name)
	}
	sources := slices.Clone(restore.Sources)
	for i := range sources {
		sources[i].InScope = true
	}
	rb.TargetDeploymentID, rb.TargetPlanID = target.Target.DeploymentID, target.Target.PlanID
	rb.RestoreDeploymentID, rb.RestorePlanID, rb.RestoreFinishedAt = target.Restore.DeploymentID, target.Restore.PlanID, target.Restore.FinishedAt
	return &Plan{
		OrgID: orgID, ProjectID: projectID, EnvironmentID: env.ID, Environment: env.Name,
		Kind: KindRollback, Rollback: &rb, Sources: sources,
		ManifestID: restore.ManifestID, ManifestRevision: restore.ManifestRevision,
	}, nil
}

// automaticNotes say, by trigger, why Loomplane automation approved.
var automaticNotes = map[string]string{
	TriggerWatch:  "Automatic rollback after a post-deploy watch breach: ",
	TriggerCanary: "Automatic rollback after a canary breach: ",
}

// ApproveAutomatic approves a ready rollback that a watch or a canary
// started, as Loomplane automation, which needs deployment:approve. It
// signals the workflow as a person's approval does. Any other plan waits
// for a person.
func (s *Service) ApproveAutomatic(ctx context.Context, orgID, planID string) (*Plan, error) {
	plan, err := s.store.GetPlan(ctx, orgID, planID)
	if err != nil {
		return nil, err
	}
	if !automaticRollback(plan) {
		return nil, fmt.Errorf("%w: %s approves only the rollbacks it started; a person approves this plan", ErrInvalid, security.SystemActorName)
	}
	p := security.SystemPrincipal(orgID)
	if authErr := s.authz.Require(ctx, p, security.DeploymentApprove, plan.ProjectID); authErr != nil {
		return nil, authErr
	}
	switch {
	case plan.Status == StatusSuperseded:
		return nil, ErrSuperseded
	case plan.Status != StatusReady:
		return nil, ErrNotReady
	}
	if backupErr := RequireBackupPolicyPermission(ctx, s.authz, p, plan.ProjectID, plan.Actions); backupErr != nil {
		return nil, backupErr
	}
	reqID, err := typeid.Generate("dec")
	if err != nil {
		return nil, fmt.Errorf("generate decision id: %w", err)
	}
	dec := orchestration.Decision{
		Actor: &p, Approve: true, Revision: plan.Revision, By: p.UserID,
		Note: automaticNotes[plan.Rollback.Trigger] + plan.Rollback.Reason,
	}
	err = s.workflow.Decide(ctx, plan.ID, reqID.String(), dec)
	if errors.Is(err, orchestration.ErrNotRunning) {
		return nil, ErrNotReady
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "plan.approve", Severity: "warning", ProjectID: plan.ProjectID, Principal: p,
			Attributes: map[string]any{
				"plan_id": plan.ID, "environment": plan.Environment, "revision": plan.Revision,
				"automatic": true, "trigger": plan.Rollback.Trigger,
			},
		})
	}
	return s.settle(ctx, plan), nil
}

// systemDecision reports whether d is Loomplane automation's.
func systemDecision(d orchestration.Decision) bool {
	_, system := security.IsSystemSubject(d.By)
	return system || (d.Actor != nil && d.Actor.IsSystem())
}

// rollbackFilter turns a rollback's computed result into one that only
// returns services' code and settings to the restored release, and lists
// the rest as leftovers, which stay as the bad release left them:
//
//  1. Keep actions, and those that only record a Cloud placement, stay.
//  2. An update or replacement of a service that holds no data stays.
//  3. Everything else is kept as it is and listed as a leftover: nothing
//     is created, deleted, or changed that holds data.
//  4. Findings about a leftover are dropped, since nothing happens to it.
//  5. A service restored with a link to a resource nothing runs is a
//     problem: rollback never recreates it.
//  6. Nothing ramps as a canary: a rollback goes back all at once.
//
// It updates result in place, with its summary, and returns the leftovers.
func rollbackFilter(result *Result, bindings []*Binding) []Leftover {
	leftovers := []Leftover{}
	left := map[Subject]map[string]bool{ServiceSubject: {}, ResourceSubject: {}}
	for i := range result.Actions {
		a := &result.Actions[i]
		if a.Op == Keep || a.AdoptCloudPlacement {
			continue
		}
		data := DataBearing(*a)
		if a.Subject == ServiceSubject && !data && (a.Op == Update || a.Op == Replace) {
			continue
		}
		reason := leftoverReason(a.Op, a.Subject, data)
		leftovers = append(leftovers, Leftover{Subject: a.Subject, Name: a.Name, Op: a.Op, DataBearing: data, Reason: reason})
		if left[a.Subject] == nil {
			left[a.Subject] = map[string]bool{}
		}
		left[a.Subject][a.Name] = true
		keepAsItIs(a, reason)
	}
	about := func(f Finding) bool { return left[f.Subject][f.Name] || canaryFinding(f.Code) }
	result.Problems = slices.DeleteFunc(result.Problems, about)
	result.Warnings = slices.DeleteFunc(result.Warnings, about)
	running := map[string]bool{}
	for _, b := range bindings {
		if b.Subject == ResourceSubject {
			running[b.Name] = true
		}
	}
	for _, a := range result.Actions {
		if a.Subject != ServiceSubject || a.Service == nil || (a.Op != Update && a.Op != Replace) {
			continue
		}
		named := map[string]bool{}
		for _, link := range a.Service.Env {
			if link.Subject != ResourceSubject || running[link.From] || named[link.From] {
				continue
			}
			named[link.From] = true
			result.Problems = append(result.Problems, Finding{
				Code: codeRollbackNeedsResource, Subject: ServiceSubject, Name: a.Name, Path: "services." + a.Name,
				Message: fmt.Sprintf("%s needs %s, which the release you're rolling back removed. Rollback never recreates a resource, so it can't restore %s; release a version that creates %s instead.",
					a.Name, link.From, a.Name, link.From),
			})
		}
	}
	// The thresholds stay, for what judges the rollback after it deploys.
	if result.Release != nil {
		result.Release.Canary = nil
	}
	result.recount()
	result.Executable = len(result.Problems) == 0 &&
		!slices.ContainsFunc(result.Warnings, func(f Finding) bool { return f.Code == "provider-unavailable" })
	return leftovers
}

// canaryFinding reports whether code is about a service ramping as a
// canary, which a rollback never does. A canary ramping in the
// environment still blocks it (codeCanaryInProgress).
func canaryFinding(code string) bool {
	return code == codeCanaryUnavailable || code == codeCanaryAppWide || code == codeCanaryPermissions
}

// keepAsItIs makes a a plain Keep: nothing it would have done reaches the
// runner, route cleanup, capacity or backups.
func keepAsItIs(a *Action, reason string) {
	a.Op, a.Reason = Keep, reason
	a.Changes, a.Routes, a.BackupEveryHours, a.Compensation = nil, nil, 0, nil
	if a.Service != nil {
		cfg := *a.Service
		cfg.Artifact, cfg.CapacityIntent = nil, nil
		a.Service = &cfg
	}
}

// leftoverReason says why a rollback leaves something as the release it
// undoes left it. op is what restoring the previous release would have
// done to it.
func leftoverReason(op Op, subject Subject, data bool) string {
	did := map[Op]string{Delete: "Created", Update: "Changed", Replace: "Replaced", Create: "Removed"}[op] +
		" by the release you're rolling back. "
	if data {
		what := "a data-bearing " + string(subject)
		switch op {
		case Delete:
			return did + "Rollback never removes " + what + "; delete it yourself if you don't need it."
		case Update:
			return did + "Rollback never reverts " + what + "."
		case Replace:
			return did + "Rollback never moves " + what + " back."
		default:
			return did + "Rollback never recreates " + what + ", which would come back empty."
		}
	}
	switch op {
	case Delete:
		return did + "Rollback never removes anything; delete it yourself if you don't need it."
	case Create:
		return did + "Rollback never recreates anything; deploy it again if you need it."
	default:
		return did + "Rollback only reverts services' code and settings."
	}
}

// appPlatformRollback is how a rollback returns a DigitalOcean service to
// what the restored release ran: App Platform builds only its branch's
// newest commit, so the rollback goes back to the deployment that release
// recorded, or can't be done.
func appPlatformRollback(a Action, img ReleasedImage, from string) (ArtifactChoice, *ReusedArtifact, *Finding) {
	choice := ArtifactChoice{Service: a.Name}
	refuse := func(message string) (ArtifactChoice, *ReusedArtifact, *Finding) {
		choice.Reason = message
		return choice, nil, &Finding{Code: codeRollbackUnavailable, Subject: ServiceSubject, Name: a.Name, Path: "services." + a.Name, Message: message}
	}
	const recorded = "App Platform can only roll back to a deployment Loomplane recorded; "
	switch {
	// The runner moves a replaced service onto a new app, which has no
	// deployment of the restored release to go back to.
	case a.Op == Replace:
		return refuse("App Platform can only roll back an app that still runs " + a.Name + "; the release you're rolling back moved it off App Platform")
	case img.Service == "" || img.Provider != a.Provider:
		return refuse(recorded + from + " has no App Platform release of " + a.Name + " to return to")
	case img.Revision == "":
		return refuse(recorded + "this release predates that")
	case img.CommitSHA != a.Service.CommitSHA:
		return refuse(recorded + "the deployment it recorded ran " + shortCommit(img.CommitSHA) + ", not " + shortCommit(a.Service.CommitSHA))
	}
	choice.Reused, choice.Revision, choice.From = true, img.Revision, from
	return choice, &ReusedArtifact{Revision: img.Revision, CommitSHA: img.CommitSHA, FromEnvironment: from, FromDeploymentID: img.DeploymentID}, nil
}
