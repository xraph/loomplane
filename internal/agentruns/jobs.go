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
	"github.com/xraph/cortex/knowledge"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/session"
	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
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

// SweepQueue is the queue the sweep runs on: the default one, which every
// server works, AI on or off.
const SweepQueue = "default"

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
	}, job.WithQueue(SweepQueue), job.WithMaxRetries(0))
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
	runCtx, release := s.begin(bound, t)
	defer release()
	if !s.claim(runCtx, t, []string{StatusQueued}, StatusRunning) {
		return nil
	}
	tools := s.tools(spec, eng)
	agentID, err := s.ensureAgent(runCtx, eng, spec, tools)
	if err != nil {
		return err
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
		err = s.ensureSession(runCtx, eng, t, overrides.SessionID, agentID)
		if errors.Is(err, errOtherAgent) {
			s.fail(ctx, t, errOtherAgent.Error())
			return nil
		}
		if err != nil {
			return err
		}
	}
	err = s.drive(runCtx, t, func(ctx context.Context, events chan<- engine.StreamEvent) error {
		return eng.StreamAgent(ctx, spec.Agent, rec.Input, overrides, events)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cortex.ErrAlreadyExists):
		// A retried job: the run exists, so carry it on.
		return s.continueTask(runCtx, t)
	case runCtx.Err() != nil:
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
	runCtx, release := s.begin(bound, t)
	defer release()
	if !s.claim(runCtx, t, nil, "") {
		return nil
	}
	s.continueSession(runCtx, t)
	return s.continueTask(runCtx, t)
}

// errOtherAgent is why a chat whose cortex session is another agent's
// can't run in it.
var errOtherAgent = errors.New("this conversation belongs to another agent")

// ensureSession makes sure the task's conversation has its cortex
// session, at the task's scope and for agentID, before the run starts:
// cortex saves a run's memory only into a session that exists, and an
// override names one without making it. A session of another agent is
// errOtherAgent.
func (s *Service) ensureSession(ctx context.Context, eng runner, t *Task, sessionID id.SessionID, agentID id.AgentID) error {
	sessions := eng.Store()
	if sessions == nil {
		return errors.New("agentruns: cortex has no store")
	}
	sess, err := sessions.GetSession(ctx, sessionID)
	if errors.Is(err, cortex.ErrSessionNotFound) {
		sess = &session.Session{
			ID: sessionID, AgentID: agentID, IsDefault: false, Title: t.Metadata[MetaTitle],
			Metadata: map[string]any{"org_id": t.OrgID, "started_by": t.RequestedBy},
		}
		err = sessions.CreateSession(ctx, sess)
		if !errors.Is(err, cortex.ErrAlreadyExists) {
			return err
		}
		// Another job made it first.
		sess, err = sessions.GetSession(ctx, sessionID)
	}
	if err != nil {
		return err
	}
	if sess.AgentID != agentID {
		return errOtherAgent
	}
	return nil
}

// continueSession makes the cortex session of a conversation whose start
// job never made it (one a 3a server started), so the rest of the run's
// memory is saved. A run that goes on without it only loses that memory,
// so anything that stops it here is only logged.
func (s *Service) continueSession(ctx context.Context, t *Task) {
	eng := s.engine()
	spec, ok := s.kind(t.Kind)
	if eng == nil || !ok || t.SessionID == "" {
		return
	}
	sessionID, err := id.ParseSessionID(t.SessionID)
	if err != nil {
		return
	}
	cfg, err := eng.GetAgentByName(ctx, spec.Agent)
	if err == nil {
		err = s.ensureSession(ctx, eng, t, sessionID, cfg.ID)
	}
	if err != nil {
		s.warn("agentruns: couldn't make a continued conversation's session", err)
	}
}

// continueTask continues the task's run on a context begin made. A run another
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
		if _, err = s.finish(ctx, t, status, r.TokensUsed, failure(status, r.StopReason, r.Error, "")); err != nil {
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

// tools is the kind's allowlist: its own, or every operation and the
// builtins eng has that the agent may use.
func (s *Service) tools(spec KindSpec, eng runner) []string {
	if spec.Tools != nil {
		return slices.Clone(spec.Tools)
	}
	var ops []agentops.Op
	if s.registry != nil {
		ops = s.registry.Ops()
	}
	builtins := builtinTools(eng)
	out := make([]string, 0, len(ops)+len(builtins))
	for _, op := range ops {
		out = append(out, op.Def.Name)
	}
	return append(out, builtins...)
}

// knowing is a runner that may have a knowledge provider:
// *engine.Engine.
type knowing interface {
	Knowledge() knowledge.Provider
}

// builtinTools are the cortex builtins eng has that the agent may use:
// knowledge_search when it has a knowledge provider. A run's allowlist
// has to name them: cortex leaves a builtin out of a run whose allowlist
// doesn't, and refuses to prepare one that names a tool it doesn't have.
// Whether the model sees one is still the tool authorizer's call, per
// workspace.
func builtinTools(eng runner) []string {
	if k, ok := eng.(knowing); ok && k.Knowledge() != nil {
		return []string{agentauthz.KnowledgeSearch}
	}
	return nil
}

// ensureAgent makes sure the kind's agent config exists at exactly the
// task's scope, which is the only scope cortex finds it at, and matches
// the spec, and returns its ID.
func (s *Service) ensureAgent(ctx context.Context, eng runner, spec KindSpec, tools []string) (id.AgentID, error) {
	def := definition(spec, tools)
	cfg, err := eng.GetAgentByName(ctx, spec.Agent)
	switch {
	case errors.Is(err, cortex.ErrAgentNotFound):
		cfg = &cortexagent.Config{ID: id.NewAgentID(), Name: spec.Agent, Scope: cortex.ScopeFromContext(ctx)}
		s.describe(cfg, spec, tools, def)
		err = eng.CreateAgent(ctx, cfg)
		if errors.Is(err, cortex.ErrAlreadyExists) {
			// Another job made it first.
			if cfg, err = eng.GetAgentByName(ctx, spec.Agent); err != nil {
				return id.AgentID{}, err
			}
			return cfg.ID, nil
		}
		return cfg.ID, err
	case err != nil:
		return id.AgentID{}, err
	}
	if cfg.Metadata != nil && cfg.Metadata[metaDefinition] == def {
		return cfg.ID, nil
	}
	s.describe(cfg, spec, tools, def)
	return cfg.ID, eng.UpdateAgent(ctx, cfg)
}

// metaDefinition is the agent config's metadata key for its definition.
const metaDefinition = "definition"

func (s *Service) describe(cfg *cortexagent.Config, spec KindSpec, tools []string, def string) {
	cfg.Description = "Loomplane's agent for " + string(spec.Kind) + " tasks"
	cfg.SystemPrompt = spec.Prompt
	cfg.Sections = nil
	cfg.Tools = tools
	cfg.MaxSteps = spec.MaxSteps
	cfg.MaxTokens = spec.outputTokens() // per call; the task's budget goes in the run's Budget
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

// failure is why a run ended badly: SafetyStopped when the safety checks
// stopped it, else its own error, the hook's, or what stopped it.
func failure(status string, stop run.StopReason, runError, hookError string) string {
	if status != StatusFailed {
		return ""
	}
	if stop == run.StopSafetyBlock {
		return SafetyStopped
	}
	for _, why := range []string{runError, hookError} {
		if why != "" {
			return why
		}
	}
	return "the run stopped before it finished"
}
