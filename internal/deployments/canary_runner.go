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
	if _, err = r.updateCanary(ctx, d.OrgID, d.ID, func(c *Canary) error {
		svc := c.service(a.ID)
		if svc == nil {
			return fmt.Errorf("the canary doesn't ramp %s", a.Name)
		}
		svc.Baseline, svc.Candidate, svc.Image, svc.Refs = t.Baseline, t.Candidate, t.Image, maps.Clone(t.Refs)
		return nil
	}); err != nil {
		return fmt.Errorf("record %s's candidate: %w", a.Name, err)
	}
	step.Image = t.Image
	step.Status, step.Message = StepRamping, candidateUp
	return nil
}

// updateCanary changes a deployment's canary as it's stored now, and
// stores it.
func (r *Runner) updateCanary(ctx context.Context, orgID, deploymentID string, change func(*Canary) error) (*Canary, error) {
	st, ok := r.store.(CanaryStore)
	if !ok {
		return nil, errors.New("this deployment store can't keep a canary")
	}
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return nil, err
	}
	if d.Canary == nil {
		return nil, fmt.Errorf("deployment %s has no canary", deploymentID)
	}
	if changeErr := change(d.Canary); changeErr != nil {
		return nil, changeErr
	}
	d.Canary.UpdatedAt = r.now().UTC()
	if setErr := st.SetCanary(ctx, orgID, deploymentID, d.Canary); setErr != nil {
		return nil, setErr
	}
	return d.Canary, nil
}

// canaryAction is what the ramp needs to act on one canary service: a job
// built the way run builds the action's, and what run would bind with.
type canaryAction struct {
	job       *Job
	exec      Weighted
	connID    string
	resources map[string]*planning.Binding
}

// canaryJob builds the job for one canary service as run would: its
// connection and credential, the binding it ramps from, and the variables
// it runs with, since a promote may redeploy with them. It saves no
// progress and shows none: each ramp call is short and safe to repeat.
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
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return nil, err
	}
	var current *planning.Binding
	byName := map[string]*planning.Binding{}
	resources := map[string]*planning.Binding{}
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name == a.ID {
			current = b
		}
		switch b.Subject {
		case planning.ServiceSubject:
			byName[b.Name] = b
		case planning.ResourceSubject:
			resources[b.Name] = b
		}
	}
	connID := a.ConnectionID
	if connID == "" {
		connID = plan.Connections[prov.Cloud]
	}
	if connID == "" {
		return nil, fmt.Errorf("the plan names no %s connection", prov.Cloud)
	}
	conn, cred, err := r.credentials.Credential(ctx, d.OrgID, connID)
	if err != nil {
		return nil, err
	}
	if captureErr := r.captureAppliedVariables(ctx, d, &a, byName, resources); captureErr != nil {
		return nil, captureErr
	}
	pinAppliedSource(d, plan, &a)
	job := &Job{
		BuildCredentials: r.credentials, Logs: r.logs, Attempt: logAttempt(ctx, d.Retries),
		Deployment: d, Plan: plan, Action: a, Connection: conn, Credential: cred, Binding: current,
		Checkpoint: func([]byte) error { return nil }, Progress: func(string, string) {},
		CommitSHA: commitFor(d, a), SourceID: commitFor(d, a),
	}
	if a.Service != nil {
		if job.Env, job.Secrets, err = r.env(ctx, d, a, byName, resources); err != nil {
			return nil, err
		}
	}
	return &canaryAction{job: job, exec: exec, connID: connID, resources: resources}, nil
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
// last one took it, which the step's hold counts from.
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
		step := d.Step(svc.ActionID)
		if step != nil && step.Status == StepSucceeded {
			continue
		}
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

// promote makes one candidate what runs, binds it, and settles its step.
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
		step.Warnings = append(step.Warnings, r.releaseConsumers(ctx, d, a.Name, current, a.Service, ca.resources)...)
	}
	step.Warnings = append(step.Warnings, r.released(ctx, d, bound, false)...)
	finished := r.now().UTC()
	if step.StartedAt != nil {
		r.telemetry.StepFinished(a.Provider, string(a.Op), StepSucceeded, finished.Sub(*step.StartedAt))
	}
	step.Status, step.URL, step.Image, step.FinishedAt = StepSucceeded, out.URL, out.Image, &finished
	step.Message = doneMessage(step)
	return r.store.UpdateStep(ctx, d.OrgID, d.ID, *step)
}

// CanaryRevert implements orchestration.CanaryDeployer. Each candidate up
// gives all of its traffic back to its baseline and is removed, and its
// step fails with reason; a step that never ran is skipped. Bindings never
// changed, so they still name the baselines. A service that couldn't be
// reverted keeps its step ramping, and the error names each one, so a
// retry tries only those again.
func (r *Runner) CanaryRevert(ctx context.Context, orgID, deploymentID, reason string) error {
	d, plan, err := r.canaryDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	switch {
	case d.Canary == nil || d.Canary.Status == CanaryRolledBack:
		return nil
	case d.Canary.Status == CanaryPromoting || d.Canary.Status == CanaryPromoted || d.Canary.Status == CanaryPromoteFailed:
		return fmt.Errorf("the canary is %s, so it can't be reverted", d.Canary.Status)
	}
	if _, err = r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
		c.Status, c.Reason = CanaryReverting, reason
		return nil
	}); err != nil {
		return err
	}
	var failed []string
	for _, svc := range d.Canary.Services {
		step := d.Step(svc.ActionID)
		if step == nil {
			continue
		}
		finished := r.now().UTC()
		switch step.Status {
		case StepRamping:
			if svc.Candidate != "" {
				if rerr := r.revert(ctx, d, plan, svc); rerr != nil {
					failed = append(failed, fmt.Sprintf("%s: %v", svc.Name, rerr))
					continue
				}
			}
			if step.StartedAt != nil {
				r.telemetry.StepFinished(svc.Provider, step.Op, StepFailed, finished.Sub(*step.StartedAt))
			}
			step.Status, step.Message, step.FinishedAt = StepFailed, reason, &finished
		case StepPending:
			step.Status, step.Message = StepSkipped, "Skipped after an earlier step failed"
		default:
			continue
		}
		if updateErr := r.store.UpdateStep(ctx, orgID, d.ID, *step); updateErr != nil {
			return updateErr
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	_, err = r.updateCanary(ctx, orgID, deploymentID, func(c *Canary) error {
		c.Status, c.Weight = CanaryRolledBack, 0
		return nil
	})
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

// settleCanary ends a failed deployment's canary: one still reverting
// couldn't be reverted, and one still promoting couldn't be promoted.
func (r *Runner) settleCanary(ctx context.Context, d *Deployment) error {
	if d.Canary == nil || (d.Canary.Status != CanaryReverting && d.Canary.Status != CanaryPromoting) {
		return nil
	}
	_, err := r.updateCanary(ctx, d.OrgID, d.ID, func(c *Canary) error {
		switch c.Status {
		case CanaryReverting:
			c.Status = CanaryRevertFailed
		case CanaryPromoting:
			c.Status = CanaryPromoteFailed
		}
		return nil
	})
	return err
}
