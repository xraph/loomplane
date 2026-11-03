package agentruns

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/cortex"
	cortexagent "github.com/xraph/cortex/agent"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/execution"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/run"
	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// The agent's jobs and the sweep's schedule.
const (
	JobStart    = "loomplane.agent.start"
	JobContinue = "loomplane.agent.continue"
	JobSweep    = "loomplane.agent.sweep"
	CronSweep   = "loomplane.agent.sweep"
)

// JobPayload names a task. Its job reads everything else from the task's
// row.
type JobPayload struct{ OrgID, TaskID string }

// JobOptions are how a task's jobs are queued: on the agent's queue, for
// up to 30 minutes, retried five times.
func JobOptions() []job.Option {
	return []job.Option{job.WithQueue(Queue), job.WithMaxRetries(jobRetries), job.WithTimeout(jobTimeout)}
}

// StartJob runs a task's run from the start.
func StartJob(s *Service) *job.Definition[JobPayload] {
	return job.NewDefinition(JobStart, s.runStart, JobOptions()...)
}

// ContinueJob continues a task's run that is ready again, or that another
// worker left.
func ContinueJob(s *Service) *job.Definition[JobPayload] {
	return job.NewDefinition(JobContinue, s.runContinue, JobOptions()...)
}

// SweepJob runs Sweep, and then each of also (agentrequests.Settle joins
// here).
func SweepJob(s *Service, also ...func(context.Context) error) *job.Definition[struct{}] {
	return job.NewDefinition(JobSweep, func(ctx context.Context, _ struct{}) error {
		errs := make([]error, 0, 1+len(also))
		errs = append(errs, s.Sweep(ctx))
		for _, fn := range also {
			errs = append(errs, fn(ctx))
		}
		return errors.Join(errs...)
	}, job.WithQueue(Queue), job.WithMaxRetries(0))
}

// runStart is the start job: it binds the task from its row, makes sure
// its kind's agent exists at exactly the task's scope, and streams the run
// with the run ID the task was given, so a retried job finds the run
// instead of starting another.
func (s *Service) runStart(ctx context.Context, in JobPayload) error {
	t, err := s.store.Get(ctx, in.OrgID, in.TaskID)
	if errors.Is(err, ErrNotFound) {
		s.warn("agentruns: a start job names no task", fmt.Errorf("%w: %s/%s", err, in.OrgID, in.TaskID))
		return nil
	}
	if err != nil {
		return err
	}
	if Terminal(t.Status) {
		return nil
	}
	spec, ok := s.kind(t.Kind)
	if !ok {
		s.fail(ctx, t, fmt.Sprintf("this server doesn't run %s tasks", t.Kind))
		return nil
	}
	bound, t, rec, err := s.bindTask(ctx, t.OrgID, t.ID)
	if err != nil {
		return s.unbound(ctx, t, err)
	}
	eff, err := s.settings.Effective(bound, t.OrgID)
	if err != nil {
		return s.refused(ctx, t, err)
	}
	if err = s.settings.RequireBudget(bound, t.OrgID); err != nil {
		return s.refused(ctx, t, err)
	}
	eng := s.engine()
	if eng == nil {
		return errors.New("agentruns: cortex isn't ready")
	}
	runID, err := id.ParseAgentRunID(t.RunID)
	if err != nil {
		s.fail(ctx, t, "the task's run ID can't be read")
		return nil
	}
	tools := s.tools(spec)
	if aerr := s.ensureAgent(bound, eng, spec, tools); aerr != nil {
		return aerr
	}
	overrides := &engine.RunOverrides{
		RunID: runID, Model: eff.Model, SystemPrompt: spec.Prompt + s.scopeNames(bound, t),
		MaxSteps: spec.MaxSteps, Budget: &execution.Budget{MaxTokens: t.MaxTokens},
		Tools: tools, ToolsRestricted: true,
	}
	if spec.UseSummaryModel && eff.SummaryModel != "" {
		overrides.Model = eff.SummaryModel
	}
	if t.SessionID != "" {
		if overrides.SessionID, err = id.ParseSessionID(t.SessionID); err != nil {
			s.fail(ctx, t, "the task's conversation can't be read")
			return nil
		}
	}
	s.move(bound, t, []string{StatusQueued}, StatusRunning)
	err = s.drive(bound, t, func(ctx context.Context, events chan<- engine.StreamEvent) error {
		return eng.StreamAgent(ctx, spec.Agent, rec.Input, overrides, events)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cortex.ErrAlreadyExists):
		// A retried job: the run exists, so carry it on.
		return s.continueTask(bound, t)
	case ctx.Err() != nil:
		return err
	}
	s.fail(ctx, t, err.Error())
	return nil
}

// runContinue is the continue job.
func (s *Service) runContinue(ctx context.Context, in JobPayload) error {
	t, err := s.store.Get(ctx, in.OrgID, in.TaskID)
	if errors.Is(err, ErrNotFound) {
		s.warn("agentruns: a continue job names no task", fmt.Errorf("%w: %s/%s", err, in.OrgID, in.TaskID))
		return nil
	}
	if err != nil {
		return err
	}
	if Terminal(t.Status) {
		return nil
	}
	if cerr := s.store.ClearContinue(ctx, t.OrgID, t.ID); cerr != nil {
		return cerr
	}
	bound, t, _, err := s.bindTask(ctx, t.OrgID, t.ID)
	if err != nil {
		return s.unbound(ctx, t, err)
	}
	return s.continueTask(bound, t)
}

// continueTask continues the task's run on a bound context. A run another
// worker holds, one that isn't ready, or one that already ended is left
// alone; one that can't go on fails the task with why.
func (s *Service) continueTask(bound context.Context, t *Task) error {
	eng := s.engine()
	if eng == nil {
		return errors.New("agentruns: cortex isn't ready")
	}
	runID, err := id.ParseAgentRunID(t.RunID)
	if err != nil {
		s.fail(bound, t, "the task's run ID can't be read")
		return nil
	}
	err = s.drive(bound, t, func(ctx context.Context, events chan<- engine.StreamEvent) error {
		return eng.ContinueRunStream(ctx, runID, events)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cortex.ErrRunAlreadyDone):
		s.settle(bound, eng, t, runID)
		return nil
	case errors.Is(err, cortex.ErrRunLeased), errors.Is(err, cortex.ErrRunNotContinuable):
		return nil
	case errors.Is(err, cortex.ErrPrincipalRevoked), errors.Is(err, cortex.ErrEffectUncertain),
		errors.Is(err, cortex.ErrReclaimLimit), errors.Is(err, cortex.ErrRunNotRecoverable):
		s.fail(bound, t, err.Error())
		return nil
	}
	return err
}

// settle finishes a task whose run ended without its hook reaching us.
func (s *Service) settle(ctx context.Context, eng runner, t *Task, runID id.AgentRunID) {
	r, err := eng.GetRun(ctx, runID)
	if err != nil || r == nil {
		return
	}
	if status, ok := statusOf(r.State); ok {
		if _, err = s.finish(ctx, t, status, r.TokensUsed, failure(status, r.Error, "")); err != nil {
			s.warn("agentruns: couldn't settle a finished run's task", err)
		}
	}
}

// unbound handles a task that couldn't be bound. Its person can no longer
// act, or the workspace turned the agent off: the run is ended and the
// task fails with why. Anything else is the job's to retry.
func (s *Service) unbound(ctx context.Context, t *Task, err error) error {
	if t == nil {
		return err
	}
	switch {
	case errors.Is(err, cortex.ErrPrincipalRevoked), errors.Is(err, security.ErrForbidden),
		errors.Is(err, agentsettings.ErrDisabled):
		if eng := s.engine(); eng != nil {
			if runID, perr := id.ParseAgentRunID(t.RunID); perr == nil {
				if cerr := eng.CancelRun(s.runContext(ctx, t), runID); cerr != nil && !errors.Is(cerr, cortex.ErrRunNotFound) {
					s.warn("agentruns: couldn't end the run of a task that can't go on", cerr)
				}
			}
		}
		s.fail(ctx, t, err.Error())
		return nil
	}
	return err
}

// refused fails the task for a setting that refuses it now (the agent off,
// the budget spent) and retries anything else.
func (s *Service) refused(ctx context.Context, t *Task, err error) error {
	if errors.Is(err, agentsettings.ErrDisabled) || errors.Is(err, agentsettings.ErrBudgetExhausted) {
		s.fail(ctx, t, err.Error())
		return nil
	}
	return err
}

// tools is the kind's allowlist: its own, or every operation.
func (s *Service) tools(spec KindSpec) []string {
	if spec.Tools != nil {
		return slices.Clone(spec.Tools)
	}
	if s.registry == nil {
		return []string{}
	}
	ops := s.registry.Ops()
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.Def.Name)
	}
	return out
}

// ensureAgent makes sure the kind's agent config exists at exactly the
// task's scope, which is the only scope cortex finds it at, and matches
// the spec.
func (s *Service) ensureAgent(ctx context.Context, eng runner, spec KindSpec, tools []string) error {
	def := definition(spec, tools)
	cfg, err := eng.GetAgentByName(ctx, spec.Agent)
	switch {
	case errors.Is(err, cortex.ErrAgentNotFound):
		cfg = &cortexagent.Config{ID: id.NewAgentID(), Name: spec.Agent, Scope: cortex.ScopeFromContext(ctx)}
		s.describe(cfg, spec, tools, def)
		err = eng.CreateAgent(ctx, cfg)
		if errors.Is(err, cortex.ErrAlreadyExists) {
			// Another job made it first.
			return nil
		}
		return err
	case err != nil:
		return err
	}
	if cfg.Metadata != nil && cfg.Metadata[metaDefinition] == def {
		return nil
	}
	s.describe(cfg, spec, tools, def)
	return eng.UpdateAgent(ctx, cfg)
}

// metaDefinition is the agent config's metadata key for its definition.
const metaDefinition = "definition"

func (s *Service) describe(cfg *cortexagent.Config, spec KindSpec, tools []string, def string) {
	cfg.Description = "Loomplane's agent for " + string(spec.Kind) + " tasks"
	cfg.SystemPrompt = spec.Prompt
	cfg.Sections = nil
	cfg.Tools = tools
	cfg.MaxSteps = spec.MaxSteps
	cfg.MaxTokens = spec.MaxTokens
	cfg.Guardrails = agentsafety.Guardrails()
	cfg.Enabled = true
	md := maps.Clone(cfg.Metadata)
	if md == nil {
		md = map[string]any{}
	}
	md[metaDefinition] = def
	cfg.Metadata = md
}

// scopeNames is what the prompt says about where the task acts.
func (s *Service) scopeNames(ctx context.Context, t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nYou are working in workspace %s.", t.OrgID)
	if t.ProjectID == "" || s.projects == nil {
		return b.String()
	}
	agent := security.AgentPrincipal(t.OrgID, nil)
	project := t.ProjectID
	if p, err := s.projects.Get(ctx, agent, t.ProjectID); err == nil {
		project = fmt.Sprintf("%s (%s)", p.Name, p.Slug)
	}
	fmt.Fprintf(&b, " This conversation is about project %s only.", project)
	if t.EnvironmentID == "" {
		return b.String()
	}
	env := t.EnvironmentID
	if envs, err := s.projects.Environments(ctx, agent, t.ProjectID); err == nil {
		for _, e := range envs {
			if e.ID == t.EnvironmentID {
				env = e.Name
			}
		}
	}
	fmt.Fprintf(&b, " It is about its %s environment only.", env)
	return b.String()
}

// statusOf is a terminal run state as a task status.
func statusOf(state run.State) (string, bool) {
	switch state {
	case run.StateCompleted:
		return StatusCompleted, true
	case run.StateFailed, run.StateIncomplete:
		return StatusFailed, true
	case run.StateCancelled:
		return StatusCancelled, true
	}
	return "", false
}

// failure is why a run ended badly: its own error, the hook's, or what
// stopped it.
func failure(status, runError, hookError string) string {
	if status != StatusFailed {
		return ""
	}
	for _, why := range []string{runError, hookError} {
		if why != "" {
			return why
		}
	}
	return "the run stopped before it finished"
}
