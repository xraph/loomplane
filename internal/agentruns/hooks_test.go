package agentruns

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/agentscope"
)

func readyNotice(at time.Time, renotified bool) run.ReadyNotice {
	return run.ReadyNotice{Source: run.ReadyExternalResults, At: at, Renotified: renotified}
}

func suspensionOf(org string) suspension.Notice {
	return suspension.Notice{
		SuspensionID: id.NewSuspensionID(), Reason: suspension.ReasonExternalTool,
		Pending: []suspension.PendingCall{{ID: "call_1", Name: "ask_person", Arguments: `{"question":"Which region?"}`}},
		Scope:   agentscope.Scope{OrgID: org}.Cortex(),
	}
}

func TestDuplicateReadyHooksContinueOnce(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	mustMove(t, w, task, StatusWaiting)
	h := &Hooks{Service: w.svc}
	ctx, runID := runCtx(task), mustRunID(t, task.RunID)
	for range 2 {
		if err := h.OnRunReady(ctx, id.Nil, runID, readyNotice(w.clock.now(), false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.OnRunReclaimable(ctx, id.Nil, runID, run.ReclaimNotice{LeaseExpiredAt: w.clock.now()}); err != nil {
		t.Fatal(err)
	}
	w.clock.advance(4 * time.Minute)
	if err := h.OnRunReady(ctx, id.Nil, runID, readyNotice(w.clock.now(), true)); err != nil {
		t.Fatal(err)
	}
	if got := w.pendingNamed(t, JobContinue); len(got) != 1 || got[0] != (JobPayload{OrgID: orgA, TaskID: task.ID}) {
		t.Fatalf("continuations %+v", got)
	}
	w.clock.advance(2 * time.Minute)
	if err := h.OnRunReady(ctx, id.Nil, runID, readyNotice(w.clock.now(), true)); err != nil {
		t.Fatal(err)
	}
	if got := w.pendingNamed(t, JobContinue); len(got) != 2 {
		t.Fatalf("after 6 minutes: %d continuations", len(got))
	}
	// The continuation clears the mark, so the next ready hook queues
	// again at once.
	if err := w.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	if err := h.OnRunReady(ctx, id.Nil, runID, readyNotice(w.clock.now(), false)); err != nil {
		t.Fatal(err)
	}
	if got := w.pendingNamed(t, JobContinue); len(got) != 3 {
		t.Fatalf("after a continuation ran: %d continuations", len(got))
	}
}

func TestSuspendedHookMarksWaitingAndHandsOff(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	mustMove(t, w, task, StatusRunning)
	h := &Hooks{Service: w.svc}
	if err := h.OnRunSuspended(runCtx(task), id.Nil, mustRunID(t, task.RunID), suspensionOf(orgA)); err != nil {
		t.Fatal(err)
	}
	if got := w.store.task(t, task.ID); got.Status != StatusWaiting {
		t.Fatalf("status %s", got.Status)
	}
	suspended, _ := w.handler.all()
	if len(suspended) != 1 || suspended[0] != (suspensionCall{taskID: task.ID, reason: string(suspension.ReasonExternalTool)}) {
		t.Fatalf("handed off %+v", suspended)
	}
	evs := w.store.eventsOf(task.ID)
	if len(evs) == 0 || evs[len(evs)-1].Kind != EventStatus {
		t.Fatalf("events %+v", evs)
	}
}

func TestTerminalHooksFinishTheTask(t *testing.T) {
	w := newWorld(t)
	h := &Hooks{Service: w.svc}
	ok := w.adaChat(t)
	mustMove(t, w, ok, StatusRunning)
	w.engine.setRun(ok.RunID, &run.Run{State: run.StateCompleted, TokensUsed: 321})
	if err := h.OnRunCompleted(runCtx(ok), id.Nil, mustRunID(t, ok.RunID), "done", time.Second); err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, ok.ID)
	if got.Status != StatusCompleted || got.Tokens != 321 || got.FinishedAt == nil || got.Error != "" {
		t.Fatalf("completed task %+v", got)
	}
	// The terminal hook that follows finds it finished and does nothing more.
	if err := h.OnRunTerminated(runCtx(ok), id.Nil, mustRunID(t, ok.RunID), run.TerminalSummary{State: run.StateCompleted}); err != nil {
		t.Fatal(err)
	}

	failed := w.adaChat(t)
	mustMove(t, w, failed, StatusRunning)
	w.engine.setRun(failed.RunID, &run.Run{State: run.StateFailed, TokensUsed: 12, Error: "the model refused"})
	if err := h.OnRunFailed(runCtx(failed), id.Nil, mustRunID(t, failed.RunID), errors.New("the model refused")); err != nil {
		t.Fatal(err)
	}
	if got = w.store.task(t, failed.ID); got.Status != StatusFailed || got.Error != "the model refused" || got.Tokens != 12 {
		t.Fatalf("failed task %+v", got)
	}

	cancelled := w.adaChat(t)
	mustMove(t, w, cancelled, StatusWaiting)
	w.engine.setRun(cancelled.RunID, &run.Run{State: run.StateCancelled})
	if err := h.OnRunTerminated(runCtx(cancelled), id.Nil, mustRunID(t, cancelled.RunID), run.TerminalSummary{State: run.StateCancelled}); err != nil {
		t.Fatal(err)
	}
	if got = w.store.task(t, cancelled.ID); got.Status != StatusCancelled {
		t.Fatalf("cancelled task %+v", got)
	}

	if _, finished := w.handler.all(); !slices.Equal(finished, []string{ok.ID, failed.ID, cancelled.ID}) {
		t.Fatalf("finished %v", finished)
	}
	evs := w.store.eventsOf(ok.ID)
	if len(evs) == 0 || evs[len(evs)-1].Kind != EventStatus {
		t.Fatalf("events %+v", evs)
	}
}

// A key that can't be read fails the run with the message an admin can
// act on, not the provider's chain of wrapped errors.
func TestFailureReasonsAreClear(t *testing.T) {
	for in, want := range map[string]string{
		"llm: complete: nexus: agentsettings: refused a call: couldn't read the agent's provider key: the workspace's AI key couldn't be read: vault: timeout": "the workspace's AI key couldn't be read",
		"budget: " + "this workspace has used its AI budget for the month; an admin can raise it in AI settings":                                               "this workspace has used its AI budget for the month; an admin can raise it in AI settings",
		"plain": "plain",
	} {
		if got := reason(in); got != want {
			t.Errorf("reason(%q) = %q, want %q", in, got, want)
		}
	}
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	if got := reason(string(long)); len(got) > maxErrorBytes {
		t.Errorf("a long reason is %d bytes", len(got))
	}
}
