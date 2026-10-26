package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// candidateUp is a ramping step's message once its candidate is up.
const candidateUp = "The candidate is up and takes no traffic yet"

// newCanary is the canary a deployment of plan starts with, or nil. Only a
// workflow build that ramps canaries (orchestration.CanaryContext) records
// one, so a run on an older build deploys every service all at once. A
// plan whose release ramps nothing has none either.
func (r *Runner) newCanary(ctx context.Context, plan *planning.Plan, d *Deployment) (*Canary, error) {
	if !orchestration.IsCanaryContext(ctx) || plan.Release == nil || len(plan.Release.Canary) == 0 {
		return nil, nil
	}
	bindings, err := r.plans.ListBindings(ctx, plan.OrgID, plan.EnvironmentID)
	if err != nil {
		return nil, err
	}
	c := &Canary{
		Status: CanaryPending, Steps: slices.Clone(plan.Release.Steps), HoldSeconds: plan.Release.HoldSeconds,
		Thresholds: plan.Release.Thresholds, Step: -1, Services: []CanaryService{}, UpdatedAt: d.StartedAt,
	}
	for _, a := range plan.Actions {
		if !plan.Release.Ramps(a.ID) || d.Step(a.ID) == nil {
			continue
		}
		svc := CanaryService{ActionID: a.ID, Name: a.Name, Provider: a.Provider}
		for _, b := range bindings {
			if string(b.Subject)+":"+b.Name == a.ID {
				svc.BindingID = b.ID
			}
		}
		c.Services = append(c.Services, svc)
	}
	if len(c.Services) == 0 {
		return nil, nil
	}
	return c, nil
}

// deployCandidate starts an action's candidate beside what runs, in place
// of Apply, and records it on the canary. Nothing is bound: the ramp
// decides whether the candidate is promoted or reverted.
func (r *Runner) deployCandidate(ctx context.Context, d *Deployment, exec Executor, job *Job, step *Step) error {
	a := job.Action
	w, ok := exec.(Weighted)
	if !ok {
		return fmt.Errorf("%w: %s can't ramp %s as a canary", ErrNoExecutor, a.Provider, a.Name)
	}
	job.Progress(StepDeploying, "Starting the candidate beside what runs")
	t, err := w.ApplyCandidate(ctx, job)
	if err != nil {
		return err
	}
	record := func(c *Canary) error {
		svc := c.service(a.ID)
		if svc == nil {
			return fmt.Errorf("the canary doesn't ramp %s", a.Name)
		}
		svc.Baseline, svc.Candidate, svc.Image, svc.Refs = t.Baseline, t.Candidate, t.Image, maps.Clone(t.Refs)
		return nil
	}
	var stopped *Canary
	if _, err = r.updateCanary(ctx, d.OrgID, d.ID, func(c *Canary) error {
		if c.stopped() {
			stopped = c
			return errCanaryStopped
		}
		return record(c)
	}); errors.Is(err, errCanaryStopped) {
		return r.removeLateCandidate(ctx, d, w, job, *t, stopped.Status, record)
	} else if err != nil {
		return fmt.Errorf("record %s's candidate: %w", a.Name, err)
	}
	step.Image = t.Image
	step.Status, step.Message = StepRamping, candidateUp
	return nil
}

// errCanaryStopped refuses a change that would move a stopped canary
// toward its candidates again.
var errCanaryStopped = errors.New("the canary has stopped")

// stopped reports whether a canary is going back to its baselines, or has
// settled as anything but promoted. Nothing may start a candidate or move
// traffic toward one after that, or an action or weigh still in flight
// when the workflow reverted would undo the revert.
func (c *Canary) stopped() bool {
	switch c.Status {
	case CanaryReverting, CanaryRolledBack, CanaryRevertFailed, CanaryPromoteFailed:
		return true
	}
	return false
}

// removeLateCandidate removes a candidate that came up after its canary
// stopped. When it can't be removed, the canary records it and that its
// revert failed, so traffic may still be split is what the canary says.
// Either way the action fails.
func (r *Runner) removeLateCandidate(ctx context.Context, d *Deployment, w Weighted, job *Job, t CanaryTarget, status string, record func(*Canary) error) error {
	name := job.Action.Name
	revertErr := w.Revert(ctx, job, t)
	if revertErr == nil {
		return fmt.Errorf("the canary is %s, so %s's candidate was removed", status, name)
	}
	_, err := r.updateCanary(ctx, d.OrgID, d.ID, func(c *Canary) error {
		if recordErr := record(c); recordErr != nil {
			return recordErr
		}
		c.Status = CanaryRevertFailed
		c.Reason = strings.TrimSpace(c.Reason + " " + fmt.Sprintf("Traffic may still be split: %s's candidate came up after the canary was %s and couldn't be removed: %v.", name, status, revertErr))
		return nil
	})
	return errors.Join(fmt.Errorf("the canary is %s, and %s's candidate couldn't be removed: %w", status, name, revertErr), err)
}

// updateCanary changes a deployment's canary as it's stored now, and
// stores it.
func (r *Runner) updateCanary(ctx context.Context, orgID, deploymentID string, change func(*Canary) error) (*Canary, error) {
	d, err := r.changeCanary(ctx, orgID, deploymentID, func(d *Deployment) error { return change(d.Canary) })
	if err != nil {
		return nil, err
	}
	return d.Canary, nil
}

// changeCanary is updateCanary for a change that also reads the
// deployment's steps. change sees the deployment as it's stored now, and
// nothing else changes its canary until this one is stored, so a decision
// change makes is never one a concurrent change made stale. It returns the
// deployment as change left it.
func (r *Runner) changeCanary(ctx context.Context, orgID, deploymentID string, change func(*Deployment) error) (*Deployment, error) {
	st, ok := r.store.(CanaryStore)
	if !ok {
		return nil, errors.New("this deployment store can't keep a canary")
	}
	return st.UpdateCanary(ctx, orgID, deploymentID, func(d *Deployment) error {
		if err := change(d); err != nil {
			return err
		}
		d.Canary.UpdatedAt = r.now().UTC()
		return nil
	})
}

// errCanarySettled leaves a canary that settled as it is: a change
// returns it to store nothing, and its caller takes it as done.
var errCanarySettled = errors.New("the canary has settled")

// stillUp reports whether a canary service's candidate is still up
// beside a baseline: recorded, and not promoted.
func stillUp(d *Deployment, svc CanaryService) bool {
	return svc.Candidate != "" && !promoted(d, svc)
}

// canaryAction is what the ramp needs to act on one canary service: a job
// built the way run builds the action's, and what run would bind with.
type canaryAction struct {
	job    *Job
	exec   Weighted
	connID string
	around *environmentBindings
}

// canaryJob builds the job for one canary service as run does, with the
// same helpers: its connection and credential, the binding it ramps from,
// its source, its variables and what it attaches, since a promote may
// redeploy with them and a revert restores the baseline with them. It
// saves no progress and shows none: each ramp call is short and safe to
// repeat.
func (r *Runner) canaryJob(ctx context.Context, d *Deployment, plan *planning.Plan, svc CanaryService) (*canaryAction, error) {
	execution, err := r.executionPlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(execution.Actions, func(a planning.Action) bool { return a.ID == svc.ActionID })
	if i < 0 {
		return nil, fmt.Errorf("the plan has no action %s", svc.ActionID)
	}
	a := execution.Actions[i]
	exec, ok := r.executors[a.Provider].(Weighted)
	if !ok {
		return nil, fmt.Errorf("%w: %s can't ramp %s as a canary", ErrNoExecutor, a.Provider, a.Name)
	}
	prov, err := r.providers.Get(a.Provider)
	if err != nil {
		return nil, err
	}
	around, err := r.environmentBindings(ctx, d, a.ID)
	if err != nil {
		return nil, err
	}
	connID := planConnection(plan, a, prov.Cloud)
	if connID == "" {
		return nil, fmt.Errorf("the plan names no %s connection", prov.Cloud)
	}
	conn, cred, err := r.credentials.Credential(ctx, d.OrgID, connID)
	if err != nil {
		return nil, err
	}
	if captureErr := r.captureAppliedVariables(ctx, d, &a, around.services, around.resources); captureErr != nil {
		return nil, captureErr
	}
	pinAppliedSource(d, plan, &a)
	job, err := r.newJob(ctx, d, plan, a, conn, cred, around.current)
	if err != nil {
		return nil, err
	}
	job.Checkpoint, job.Progress = func([]byte) error { return nil }, func(string, string) {}
	if inputErr := r.deployInputs(ctx, job, around); inputErr != nil {
		return nil, inputErr
	}
	return &canaryAction{job: job, exec: exec, connID: connID, around: around}, nil
}

// canaryDeployment reads a deployment and its plan.
func (r *Runner) canaryDeployment(ctx context.Context, orgID, deploymentID string) (*Deployment, *planning.Plan, error) {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return nil, nil, err
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return nil, nil, err
	}
	return d, plan, nil
}

// promoted reports whether a canary service's step succeeded: its
// candidate was promoted, in this run or one a retry followed, and it's
// what runs now.
func promoted(d *Deployment, svc CanaryService) bool {
	s := d.Step(svc.ActionID)
	return s != nil && s.Status == StepSucceeded
}

// CanaryRamp implements orchestration.CanaryDeployer. A deployment without
// a canary has an empty ramp, which the workflow skips.
func (r *Runner) CanaryRamp(ctx context.Context, orgID, deploymentID string) (orchestration.CanaryRamp, error) {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return orchestration.CanaryRamp{}, err
	}
	if d.Canary == nil || len(d.Canary.Services) == 0 {
		return orchestration.CanaryRamp{}, nil
	}
	return orchestration.CanaryRamp{
		Steps: slices.Clone(d.Canary.Steps), Hold: time.Duration(d.Canary.HoldSeconds) * time.Second,
		Lag: releasehealth.MetricsLag, MaxInconclusive: releasehealth.MaxInconclusive,
	}, nil
}

// CanaryWeigh implements orchestration.CanaryDeployer. It gives every
// candidate weight percent of its service's traffic, and returns when the
// last one took it, which the step's hold counts from. A service an
// earlier attempt promoted already runs the release, so it's left alone.
func (r *Runner) CanaryWeigh(ctx context.Context, orgID, deploymentID string, step, weight int) (time.Time, error) {
	d, plan, err := r.canaryDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return time.Time{}, err
	}
	c := d.Canary
	switch {
	case c == nil:
		return time.Time{}, fmt.Errorf("deployment %s has no canary", deploymentID)
	case c.Status != CanaryPending && c.Status != CanaryRamping:
		return time.Time{}, fmt.Errorf("the canary is %s, so its traffic doesn't move", c.Status)
	case step < 0 || step >= len(c.Steps) || c.Steps[step] != weight:
		return time.Time{}, fmt.Errorf("the canary has no step %d at %d%%", step, weight)
	}
	for _, svc := range c.Services {
		if promoted(d, svc) {
			continue
		}
		if s := d.Step(svc.ActionID); s == nil || s.Status != StepRamping || svc.Candidate == "" {
			return time.Time{}, fmt.Errorf("%s has no candidate up, so its traffic can't move", svc.Name)
		}
		ca, jobErr := r.canaryJob(ctx, d, plan, svc)
		if jobErr != nil {
			return time.Time{}, fmt.Errorf("%s: %w", svc.Name, jobErr)
		}
		if weighErr := ca.exec.SetWeights(ctx, ca.job, svc.target(), weight); weighErr != nil {
			return time.Time{}, fmt.Errorf("%s: %w", svc.Name, weighErr)
		}
	}
	c, err = r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
		// A revert may have started while the weights moved. It owns the
		// canary now, and puts its traffic back itself.
		if c.stopped() {
			return fmt.Errorf("the canary is %s, so its traffic doesn't move", c.Status)
		}
		c.Status, c.Step, c.Weight = CanaryRamping, step, weight
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return c.UpdatedAt, nil
}

// CanaryPromote implements orchestration.CanaryDeployer. Each candidate
// becomes what runs, and is bound as run binds what Apply deployed. A
// service already promoted is left as it is, so a retry carries on where
// one failed.
func (r *Runner) CanaryPromote(ctx context.Context, orgID, deploymentID string) error {
	d, plan, err := r.canaryDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	switch {
	case d.Canary == nil || d.Canary.Status == CanaryPromoted:
		return nil
	case d.Canary.Status != CanaryPending && d.Canary.Status != CanaryRamping && d.Canary.Status != CanaryPromoting:
		return fmt.Errorf("the canary is %s, so it can't be promoted", d.Canary.Status)
	}
	if _, err = r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
		c.Status = CanaryPromoting
		return nil
	}); err != nil {
		return err
	}
	for _, svc := range d.Canary.Services {
		if promoted(d, svc) {
			continue
		}
		step := d.Step(svc.ActionID)
		if step == nil || step.Status != StepRamping || svc.Candidate == "" {
			return fmt.Errorf("%s has no candidate up to promote", svc.Name)
		}
		if err = r.promote(ctx, d, plan, svc, step); err != nil {
			return fmt.Errorf("%s: %w", svc.Name, err)
		}
	}
	_, err = r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
		c.Status, c.Weight = CanaryPromoted, 100
		return nil
	})
	return err
}

// promote makes one candidate what runs, binds it, and settles its step,
// as run does after Apply: what an earlier move left is removed once it's
// bound, and only warns when it can't be.
func (r *Runner) promote(ctx context.Context, d *Deployment, plan *planning.Plan, svc CanaryService, step *Step) error {
	ca, err := r.canaryJob(ctx, d, plan, svc)
	if err != nil {
		return err
	}
	out, err := ca.exec.Promote(ctx, ca.job, svc.target())
	if err != nil {
		return err
	}
	a, current := ca.job.Action, ca.job.Binding
	bound, err := r.bind(ctx, d, a, current, out, ca.connID, nil)
	if err != nil {
		return err
	}
	if a.Service != nil {
		step.Warnings = append(step.Warnings, r.releaseConsumers(ctx, d, a.Name, current, a.Service, ca.around.resources)...)
	}
	step.Warnings = append(step.Warnings, r.released(ctx, d, bound, false)...)
	warn := func(err error) { step.Warnings = append(step.Warnings, err.Error()) }
	if removeErr := r.removeReplaced(ctx, ca.job, bound, nil, warn, &replaceCheckpoint{Replace: true}, func([]byte) error { return nil }); removeErr != nil {
		warn(removeErr)
	}
	finished := r.now().UTC()
	if step.StartedAt != nil {
		r.telemetry.StepFinished(a.Provider, string(a.Op), StepSucceeded, finished.Sub(*step.StartedAt))
	}
	step.Status, step.URL, step.Image, step.FinishedAt = StepSucceeded, out.URL, out.Image, &finished
	step.Message = doneMessage(step)
	return r.store.UpdateStep(ctx, d.OrgID, d.ID, *step)
}

// CanaryRevert implements orchestration.CanaryDeployer. Which services to
// revert comes from the canary as this marks it reverting, not from step
// statuses or an earlier read: a candidate recorded before then is
// reverted here, and one that comes up after finds the canary stopped and
// removes itself. Each candidate up gives all of its traffic back to its
// baseline and is removed, then its candidate is cleared, so a retry
// reverts only what's still up. Its step fails with reason; a step that
// never ran is skipped. Bindings never changed, so they still name the
// baselines. The error names each service that couldn't be reverted. A
// canary already settled isn't reverted again: rolled_back is done, and
// revert_failed was given up on when its deployment failed.
func (r *Runner) CanaryRevert(ctx context.Context, orgID, deploymentID, reason string) error {
	d, plan, err := r.canaryDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	if d.Canary == nil {
		return nil
	}
	d, err = r.changeCanary(ctx, orgID, deploymentID, func(d *Deployment) error {
		switch c := d.Canary; c.Status {
		case CanaryRolledBack, CanaryPromoteFailed:
			return errCanarySettled
		case CanaryPending, CanaryRamping, CanaryReverting:
			c.Status, c.Reason = CanaryReverting, reason
			return nil
		default:
			return fmt.Errorf("the canary is %s, so it can't be reverted", c.Status)
		}
	})
	if errors.Is(err, errCanarySettled) {
		return nil
	}
	if err != nil {
		return err
	}
	var failed []string
	var storeErrs []error
	for _, svc := range d.Canary.Services {
		step := d.Step(svc.ActionID)
		if step == nil || promoted(d, svc) {
			continue
		}
		if svc.Candidate != "" {
			if rerr := r.revert(ctx, d, plan, svc); rerr != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", svc.Name, rerr))
				continue
			}
			if _, cerr := r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
				if s := c.service(svc.ActionID); s != nil {
					s.Candidate = ""
				}
				return nil
			}); cerr != nil {
				storeErrs = append(storeErrs, fmt.Errorf("record that %s was reverted: %w", svc.Name, cerr))
				continue
			}
		}
		finished := r.now().UTC()
		switch step.Status {
		case StepPending:
			step.Status, step.Message = StepSkipped, "Skipped after an earlier step failed"
		case StepFailed, StepSkipped:
			continue
		default:
			if step.StartedAt != nil {
				r.telemetry.StepFinished(svc.Provider, step.Op, StepFailed, finished.Sub(*step.StartedAt))
			}
			step.Status, step.Message, step.FinishedAt = StepFailed, reason, &finished
		}
		if uerr := r.store.UpdateStep(ctx, orgID, d.ID, *step); uerr != nil {
			storeErrs = append(storeErrs, uerr)
		}
	}
	if len(failed) > 0 {
		storeErrs = append([]error{errors.New(strings.Join(failed, "; "))}, storeErrs...)
	}
	if len(storeErrs) > 0 {
		return errors.Join(storeErrs...)
	}
	_, err = r.changeCanary(ctx, orgID, deploymentID, func(d *Deployment) error {
		c := d.Canary
		// Something else settled it meanwhile: a late candidate that
		// couldn't be removed made it revert_failed, or a revert beside
		// this one finished first. Either way it says what's true already.
		if c.Status != CanaryReverting {
			return errCanarySettled
		}
		var up []string
		for _, svc := range c.Services {
			if stillUp(d, svc) {
				up = append(up, svc.Name)
			}
		}
		if len(up) > 0 {
			// Still reverting, so a retry reverts what this didn't.
			return fmt.Errorf("%s still has a candidate up", strings.Join(up, ", "))
		}
		// Every candidate is gone, so Weight is 0 either way. A service an
		// earlier attempt promoted keeps the release, so the deployment is
		// split between old and new: not a rollback.
		c.Weight = 0
		if slices.ContainsFunc(c.Services, func(svc CanaryService) bool { return promoted(d, svc) }) {
			c.Status = CanaryPromoteFailed
		} else {
			c.Status = CanaryRolledBack
		}
		return nil
	})
	if errors.Is(err, errCanarySettled) {
		return nil
	}
	return err
}

// revert moves one service's traffic back to its baseline and removes its
// candidate.
func (r *Runner) revert(ctx context.Context, d *Deployment, plan *planning.Plan, svc CanaryService) error {
	ca, err := r.canaryJob(ctx, d, plan, svc)
	if err != nil {
		return err
	}
	if weighErr := ca.exec.SetWeights(ctx, ca.job, svc.target(), 0); weighErr != nil {
		return weighErr
	}
	return ca.exec.Revert(ctx, ca.job, svc.target())
}

// CanarySettle implements orchestration.CanaryDeployer. The workflow
// calls it once it gave up on a promote or revert, whatever the
// deployment's status, so the canary ends as promote_failed or
// revert_failed with why traffic may still be split, and stops holding its
// environment. A canary already settled is left as it is.
func (r *Runner) CanarySettle(ctx context.Context, orgID, deploymentID, reason string) error {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	return r.settleCanary(ctx, d, reason, true)
}

// settleCanary ends the canary of a deployment that failed, from what its
// services show, so a failed deployment never shows a canary still in
// flight. It decides from the canary as it's stored when it settles, not
// from d, which may be older: a canary that settled meanwhile is left as
// it is. One still promoting couldn't be promoted. Otherwise, an
// unpromoted candidate still up means traffic may still be split, so the
// revert failed, whatever else was promoted. With none up, a service an
// earlier attempt promoted leaves old and new side by side, which a
// rollback can't be; with none promoted, every binding names what runs,
// and the canary rolled back. replace puts reason in place of the canary's
// own.
func (r *Runner) settleCanary(ctx context.Context, d *Deployment, reason string, replace bool) error {
	if d.Canary == nil {
		return nil
	}
	_, err := r.changeCanary(ctx, d.OrgID, d.ID, func(d *Deployment) error {
		c := d.Canary
		switch {
		case c.Status == CanaryPromoting:
			c.Status = CanaryPromoteFailed
		case c.Status != CanaryPending && c.Status != CanaryRamping && c.Status != CanaryReverting:
			return errCanarySettled
		case slices.ContainsFunc(c.Services, func(svc CanaryService) bool { return stillUp(d, svc) }):
			c.Status = CanaryRevertFailed
		case slices.ContainsFunc(c.Services, func(svc CanaryService) bool { return promoted(d, svc) }):
			c.Status, c.Weight = CanaryPromoteFailed, 0
		default:
			c.Status, c.Weight = CanaryRolledBack, 0
		}
		if replace || c.Reason == "" {
			c.Reason = reason
		}
		return nil
	})
	if errors.Is(err, errCanarySettled) {
		return nil
	}
	return err
}
