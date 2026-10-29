package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// deployment runs, or after a canary dealt with the target itself.
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
