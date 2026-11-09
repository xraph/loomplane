package agentruns

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/plugin"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/agentscope"
)

// Hooks are cortex's run hooks for the agent's tasks. Each reads the task
// through the workspace on the hook's context, which carries the run's
// own scope, so a hook never touches another workspace's task. None waits
// on the model: they write the task and queue a job.
type Hooks struct{ Service *Service }

var (
	_ plugin.RunSuspended   = (*Hooks)(nil)
	_ plugin.RunReady       = (*Hooks)(nil)
	_ plugin.RunReclaimable = (*Hooks)(nil)
	_ plugin.RunCompleted   = (*Hooks)(nil)
	_ plugin.RunFailed      = (*Hooks)(nil)
	_ plugin.RunTerminated  = (*Hooks)(nil)
	_ plugin.ToolCompleted  = (*Hooks)(nil)
	_ plugin.ToolFailed     = (*Hooks)(nil)
	_ plugin.ToolDenied     = (*Hooks)(nil)
)

// SafetyStopped is a task's error when Loomplane's safety checks stopped
// its run's answer.
const SafetyStopped = "Loomplane's safety checks stopped this answer."

// Name implements plugin.Extension.
func (h *Hooks) Name() string { return "loomplane-agent-runs" }

// task is the task of runID in the workspace ctx's scope names.
func (h *Hooks) task(ctx context.Context, runID id.AgentRunID) (*Service, *Task, bool) {
	s := h.Service
	if s == nil {
		return nil, nil, false
	}
	scope, err := agentscope.FromCortex(cortex.ScopeFromContext(ctx))
	if err != nil {
		s.warn("agentruns: a run hook came without a Loomplane scope", err)
		return nil, nil, false
	}
	t, err := s.store.ByRun(ctx, scope.OrgID, runID.String())
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.warn("agentruns: a run hook couldn't read its task", err)
		}
		return nil, nil, false
	}
	return s, t, true
}

// OnRunReady queues the run's continuation, once per five minutes.
func (h *Hooks) OnRunReady(ctx context.Context, _ id.AgentID, runID id.AgentRunID, _ run.ReadyNotice) error {
	return h.continueLater(ctx, runID)
}

// OnRunReclaimable queues the run's continuation, once per five minutes.
func (h *Hooks) OnRunReclaimable(ctx context.Context, _ id.AgentID, runID id.AgentRunID, _ run.ReclaimNotice) error {
	return h.continueLater(ctx, runID)
}

func (h *Hooks) continueLater(ctx context.Context, runID id.AgentRunID) error {
	s, t, ok := h.task(ctx, runID)
	if !ok || Terminal(t.Status) {
		return nil
	}
	if err := s.continueLater(context.WithoutCancel(ctx), t); err != nil {
		s.warn("agentruns: couldn't queue a run's continuation", err)
	}
	return nil
}

// OnRunSuspended marks the task waiting and hands the pause to the
// suspension handler.
func (h *Hooks) OnRunSuspended(ctx context.Context, _ id.AgentID, runID id.AgentRunID, n suspension.Notice) error {
	s, t, ok := h.task(ctx, runID)
	if !ok || Terminal(t.Status) {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	s.move(ctx, t, []string{StatusQueued, StatusRunning}, StatusWaiting)
	if handler := s.suspensionHandler(); handler != nil {
		if err := handler.OnSuspended(ctx, t, n); err != nil {
			s.warn("agentruns: couldn't hand a paused run to a person", err)
		}
	}
	return nil
}

// OnToolCompleted records a tool call that succeeded. The result itself
// isn't kept: it's the model's to read, and it can carry what the log
// mustn't.
func (h *Hooks) OnToolCompleted(_ context.Context, runID id.AgentRunID, tool, _ string, _ time.Duration) error {
	if h.Service != nil {
		h.Service.toolOutcome(runID.String(), toolOutcome{tool: tool, outcome: OutcomeOK})
	}
	return nil
}

// OnToolFailed records a tool call that failed, with its error cut to
// 300 bytes.
func (h *Hooks) OnToolFailed(_ context.Context, runID id.AgentRunID, tool string, err error) error {
	why := ""
	if err != nil {
		why = cut(err.Error(), maxOutcomeMessage)
	}
	if h.Service != nil {
		h.Service.toolOutcome(runID.String(), toolOutcome{tool: tool, outcome: OutcomeFailed, message: why})
	}
	return nil
}

// OnToolDenied records a tool call the authorizer refused, with why.
func (h *Hooks) OnToolDenied(_ context.Context, runID id.AgentRunID, tool, reason string) error {
	if h.Service != nil {
		h.Service.toolOutcome(runID.String(), toolOutcome{tool: tool, outcome: OutcomeDenied, message: cut(reason, maxOutcomeMessage)})
	}
	return nil
}

// OnRunCompleted finishes the task.
func (h *Hooks) OnRunCompleted(ctx context.Context, _ id.AgentID, runID id.AgentRunID, _ string, _ time.Duration) error {
	h.finish(ctx, runID, StatusCompleted, "")
	return nil
}

// OnRunFailed fails the task with the run's error.
func (h *Hooks) OnRunFailed(ctx context.Context, _ id.AgentID, runID id.AgentRunID, err error) error {
	why := ""
	if err != nil {
		why = err.Error()
	}
	h.finish(ctx, runID, StatusFailed, why)
	return nil
}

// OnRunTerminated finishes the task however the run ended: it's the one
// hook every ending fires.
func (h *Hooks) OnRunTerminated(ctx context.Context, _ id.AgentID, runID id.AgentRunID, sum run.TerminalSummary) error {
	if status, ok := statusOf(sum.State); ok {
		h.finish(ctx, runID, status, "")
	}
	return nil
}

// finish moves the task to status with the tokens and error the run
// recorded, once its last events are written (see finishRun). A task that
// already finished is left as it is.
func (h *Hooks) finish(ctx context.Context, runID id.AgentRunID, status, hookError string) {
	s, t, ok := h.task(ctx, runID)
	if !ok || Terminal(t.Status) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	tokens, runError, stop := -1, "", run.StopReason("")
	if eng := s.engine(); eng != nil {
		if r, err := eng.GetRun(ctx, runID); err == nil && r != nil {
			tokens, runError, stop = r.TokensUsed, r.Error, r.StopReason
		}
	}
	s.finishRun(ctx, t, status, tokens, failure(status, stop, runError, hookError))
}
