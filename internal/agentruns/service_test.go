package agentruns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/shield"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestStartQueuesAJobAndNothingElse(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	if task.Status != StatusQueued || !strings.HasPrefix(task.ID, "atask_") || !strings.HasPrefix(task.RunID, "arun_") {
		t.Fatalf("task %+v", task)
	}
	if task.RequestedBy != ada || task.SessionID == "" || task.MaxTokens != ChatSpec().DefaultMaxTokens {
		t.Fatalf("task %+v", task)
	}
	jobs := w.pending(t)
	if len(jobs) != 1 || jobs[0].Name != JobStart || jobs[0].Queue != Queue || jobs[0].MaxRetries != 5 || jobs[0].Timeout != 30*time.Minute {
		t.Fatalf("jobs %+v", jobs)
	}
	if got := w.pendingNamed(t, JobStart); got[0] != (JobPayload{OrgID: orgA, TaskID: task.ID}) {
		t.Fatalf("payload %+v", got)
	}
	if streams, continues, _, created, _ := w.engine.calls(); len(streams)+len(continues)+created != 0 {
		t.Fatal("Start called the engine")
	}
	// The stored principal is the agent acting for Ada.
	rec, err := w.store.Record(context.Background(), orgA, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := w.svc.codec.DecodePrincipal(context.Background(), rec.Principal)
	if err != nil {
		t.Fatal(err)
	}
	p := decoded.(security.Principal)
	if person, ok := p.Person(); !p.IsAgent() || !ok || person.UserID != ada || rec.Input != "How are my deployments?" {
		t.Fatalf("stored %+v, %q", p, rec.Input)
	}
}

func TestStartRefusesWhenDisabled(t *testing.T) {
	w := newWorld(t)
	w.settings.turnOff(orgA)
	_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "hi"})
	if !errors.Is(err, agentsettings.ErrDisabled) {
		t.Fatalf("Start while off: %v", err)
	}
	if w.store.count() != 0 || len(w.pending(t)) != 0 {
		t.Fatal("a task was written")
	}
}

func TestStartRefusesAtBudget(t *testing.T) {
	w := newWorld(t)
	w.settings.exhausted[orgA] = true
	_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "hi"})
	if !errors.Is(err, agentsettings.ErrBudgetExhausted) {
		t.Fatalf("Start at the budget: %v", err)
	}
	if w.store.count() != 0 {
		t.Fatal("a task was written")
	}
}

func TestStartNeedsAgentUse(t *testing.T) {
	w := newWorld(t)
	for _, p := range []*security.Principal{personPtr(orgA, "usr_stranger"), personPtr(orgA, bob)} {
		_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: p, Input: "hi"})
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("%s: %v", p.UserID, err)
		}
	}
	// A person of another workspace can't start a task in this one.
	_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgB, bob), Input: "hi"})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Bob of Ledger in Engines: %v", err)
	}
	if w.store.count() != 0 {
		t.Fatal("a task was written")
	}
}

func TestStartRefusesAnotherOrgsProject(t *testing.T) {
	w := newWorld(t)
	for _, in := range []StartInput{
		{ProjectID: "prj_ledger"},
		{ProjectID: "prj_none"},
		{EnvironmentID: "prj_mono_staging"},
		{ProjectID: "prj_mono", EnvironmentID: "prj_ledger_staging"},
	} {
		in.Kind, in.OrgID, in.OnBehalfOf, in.Input = KindChat, orgA, personPtr(orgA, ada), "hi"
		if _, err := w.svc.Start(context.Background(), in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	// The agent alone is held to the same.
	if _, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, ProjectID: "prj_ledger", Input: "hi"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("background in another org's project: %v", err)
	}
	if w.store.count() != 0 {
		t.Fatal("a task was written")
	}
	// Its own project and environment start.
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono", EnvironmentID: "prj_mono_staging"})
	if task.ProjectID != "prj_mono" || task.EnvironmentID != "prj_mono_staging" {
		t.Fatalf("task %+v", task)
	}
}

func TestStartCapsTokens(t *testing.T) {
	w := newWorld(t)
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada)})
	if task.MaxTokens != 200_000 {
		t.Fatalf("default %d", task.MaxTokens)
	}
	task = w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), MaxTokens: 1_000_000})
	if task.MaxTokens != 1_000_000 {
		t.Fatalf("ceiling %d", task.MaxTokens)
	}
	for _, n := range []int{1_000_001, -1} {
		_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "hi", MaxTokens: n})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%d tokens: %v", n, err)
		}
	}
}

func TestStartRefusesUnknownKindsAndReservedMetadata(t *testing.T) {
	w := newWorld(t)
	if _, err := w.svc.Start(context.Background(), StartInput{Kind: KindInvestigation, OrgID: orgA, Input: "hi"}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unregistered kind: %v", err)
	}
	_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "hi",
		Metadata: map[string]string{metaReleasePlan: "plan_x"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved metadata: %v", err)
	}
	if _, err = w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no input: %v", err)
	}
}

// A conversation continues only for the person who started it, at the
// same scope.
func TestSessionsStayWithTheirPerson(t *testing.T) {
	w := newWorld(t)
	first := w.adaChat(t)
	next := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), SessionID: first.SessionID})
	if next.SessionID != first.SessionID {
		t.Fatalf("session %s, want %s", next.SessionID, first.SessionID)
	}
	for _, in := range []StartInput{
		{OnBehalfOf: personPtr(orgA, bo), SessionID: first.SessionID},
		{OnBehalfOf: personPtr(orgA, ada), SessionID: first.SessionID, ProjectID: "prj_mono"},
		{OnBehalfOf: personPtr(orgA, ada), SessionID: "sess_01jbzzzzzzzzzzzzzzzzzzzzzz"},
		{OnBehalfOf: personPtr(orgA, ada), SessionID: "not-a-session"},
	} {
		in.Kind, in.OrgID, in.Input = KindChat, orgA, "hi"
		if _, err := w.svc.Start(context.Background(), in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	other := w.adaChat(t)
	if other.SessionID == first.SessionID {
		t.Fatal("a new chat joined another conversation")
	}
}

func TestStartFailsTheTaskWhenItCantBeQueued(t *testing.T) {
	w := newWorld(t)
	w.svc.jobs = func() *dispatchengine.Engine { return nil }
	_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "hi"})
	if err == nil {
		t.Fatal("Start queued nothing and said nothing")
	}
	tasks, _ := w.store.Open(context.Background(), orgA)
	if len(tasks) != 0 {
		t.Fatalf("open tasks %+v", tasks)
	}
	all, _ := w.store.List(context.Background(), orgA, StoreFilter{})
	if len(all) != 1 || all[0].Status != StatusFailed || all[0].Error == "" {
		t.Fatalf("tasks %+v", all)
	}
}

func TestRegisterKindRefusesDuplicates(t *testing.T) {
	w := newWorld(t)
	if err := w.svc.RegisterKind(ChatSpec()); err == nil {
		t.Fatal("a second chat spec was registered")
	}
	if err := w.svc.RegisterKind(KindSpec{Kind: KindInvestigation}); err == nil {
		t.Fatal("a spec with no agent was registered")
	}
	if err := w.svc.RegisterKind(KindSpec{Kind: KindInvestigation, Agent: "loomplane-investigation", MaxSteps: 10, DefaultMaxTokens: 10, MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestChatSpecIsAsDecided(t *testing.T) {
	s := ChatSpec()
	if s.Kind != KindChat || s.Agent != "loomplane-chat" || s.Tools != nil || s.MaxSteps != 40 ||
		s.DefaultMaxTokens != 200_000 || s.MaxTokens != 1_000_000 || s.UseSummaryModel {
		t.Fatalf("spec %+v", s)
	}
	for _, want := range []string{"infrastructure engineer", "<untrusted_content>", "request_secret", "get_plan", "revision", "ask_person", "production"} {
		if !strings.Contains(s.Prompt, want) {
			t.Errorf("the prompt doesn't mention %q", want)
		}
	}
	if strings.ContainsRune(s.Prompt, '\u2014') {
		t.Error("the prompt has an em dash")
	}
}

func TestStartJobEnsuresTheAgentAtTheTaskScope(t *testing.T) {
	w := newWorld(t)
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	_, _, _, created, updated := w.engine.calls()
	if created != 1 || updated != 0 {
		t.Fatalf("created %d, updated %d", created, updated)
	}
	cfg := w.engine.created[0]
	want := agentscope.Scope{OrgID: orgA, ProjectID: "prj_mono"}.Cortex()
	if cfg.Name != "loomplane-chat" || cfg.Scope.Canonical() != want.Canonical() || !cfg.Enabled {
		t.Fatalf("agent %+v", cfg)
	}
	if cfg.Guardrails[agentsafety.GuardrailShieldProfile] != agentsafety.Guardrails()[agentsafety.GuardrailShieldProfile] {
		t.Fatalf("guardrails %+v", cfg.Guardrails)
	}
	// Another task at the same scope finds it.
	if err := w.runStart(t, w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})); err != nil {
		t.Fatal(err)
	}
	if _, _, _, created, updated = w.engine.calls(); created != 1 || updated != 0 {
		t.Fatalf("second job: created %d, updated %d", created, updated)
	}
	// A workspace-wide task needs its own.
	if err := w.runStart(t, w.adaChat(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, created, _ = w.engine.calls(); created != 2 {
		t.Fatalf("org scope: created %d", created)
	}
	// A changed prompt updates it.
	w.svc.mu.Lock()
	spec := w.svc.kinds[KindChat]
	spec.Prompt += " Be brief."
	w.svc.kinds[KindChat] = spec
	w.svc.mu.Unlock()
	if err := w.runStart(t, w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})); err != nil {
		t.Fatal(err)
	}
	if _, _, _, created, updated = w.engine.calls(); created != 2 || updated != 1 || !strings.HasSuffix(w.engine.updated[0].SystemPrompt, "Be brief.") {
		t.Fatalf("changed prompt: created %d, updated %d", created, updated)
	}
}

func TestStartJobPassesRunIDAndBudget(t *testing.T) {
	w := newWorld(t)
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono", EnvironmentID: "prj_mono_staging", MaxTokens: 5000})
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	streams, _, _, _, _ := w.engine.calls()
	if len(streams) != 1 {
		t.Fatalf("streams %d", len(streams))
	}
	s := streams[0]
	o := s.overrides
	if o.RunID.String() != task.RunID || o.Budget == nil || o.Budget.MaxTokens != 5000 || o.Model != "claude-sonnet-5-5" ||
		o.SessionID.String() != task.SessionID || o.MaxSteps != 40 || s.agent != "loomplane-chat" || s.input != "How are my deployments?" {
		t.Fatalf("overrides %+v", o)
	}
	for _, name := range []string{"Project prj_mono", "staging", orgA} {
		if !strings.Contains(o.SystemPrompt, name) {
			t.Errorf("the prompt doesn't name %q: %s", name, o.SystemPrompt)
		}
	}
	if !strings.HasPrefix(o.SystemPrompt, ChatSpec().Prompt) {
		t.Error("the prompt doesn't start with the spec's")
	}
	if !o.ToolsRestricted || len(o.Tools) != len(w.registry.Ops()) || !slices.Contains(o.Tools, "list_teams") {
		t.Fatalf("tools %v", o.Tools)
	}
	// The run's context is bound from the task row alone.
	ctx := s.ctx
	if agentops.TaskIDFrom(ctx) != task.ID || pipeline.TenantID(ctx) != "nx_"+orgA || shield.TenantFromContext(ctx) != orgA ||
		shield.AppFromContext(ctx) != agentscope.App {
		t.Fatalf("ctx: task %q, nexus %q, shield %q/%q", agentops.TaskIDFrom(ctx), pipeline.TenantID(ctx), shield.TenantFromContext(ctx), shield.AppFromContext(ctx))
	}
	if got := cortex.ScopeFromContext(ctx).Canonical(); got != (agentscope.Scope{OrgID: orgA, ProjectID: "prj_mono", EnvironmentID: "prj_mono_staging"}).Cortex().Canonical() {
		t.Fatalf("scope %s", got)
	}
	p, _ := cortex.PrincipalFromContext(ctx).(security.Principal)
	if person, ok := p.Person(); !p.IsAgent() || p.OrgID != orgA || !ok || person.UserID != ada {
		t.Fatalf("principal %+v", p)
	}
	if got := w.store.task(t, task.ID); got.Status != StatusRunning {
		t.Fatalf("status %s", got.Status)
	}
}

func TestStartJobUsesTheSummaryModelWhenTheKindSaysSo(t *testing.T) {
	w := newWorld(t)
	if err := w.svc.RegisterKind(KindSpec{Kind: KindInvestigation, Agent: "loomplane-investigation", Prompt: "Investigate.",
		MaxSteps: 10, DefaultMaxTokens: 1000, MaxTokens: 1000, UseSummaryModel: true, Tools: []string{"whoami"}}); err != nil {
		t.Fatal(err)
	}
	task := w.start(t, StartInput{Kind: KindInvestigation})
	if task.RequestedBy != "" {
		t.Fatalf("background task requested by %q", task.RequestedBy)
	}
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	streams, _, _, _, _ := w.engine.calls()
	if o := streams[0].overrides; o.Model != "claude-haiku-5-5" || !slices.Equal(o.Tools, []string{"whoami"}) {
		t.Fatalf("overrides %+v", o)
	}
	p, _ := cortex.PrincipalFromContext(streams[0].ctx).(security.Principal)
	if _, ok := p.Person(); !p.IsAgent() || ok {
		t.Fatalf("background principal %+v", p)
	}
}

func TestStartJobRetryContinues(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	w.engine.streamErr = fmt.Errorf("create run: %w", cortex.ErrAlreadyExists)
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	if _, continues, _, _, _ := w.engine.calls(); !slices.Equal(continues, []string{task.RunID}) {
		t.Fatalf("continues %v", continues)
	}
}

func TestStartJobOnAFinishedTaskDoesNothing(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	if err := w.svc.Cancel(context.Background(), person(orgA, ada), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	if streams, _, _, created, _ := w.engine.calls(); len(streams) != 0 || created != 0 {
		t.Fatal("a cancelled task ran")
	}
}

// A job for a revoked person fails the task before any model call.
func TestStartJobFailsARevokedTask(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	w.members.set(orgA, "usr_gone", security.MemberOwner)
	gone := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, "usr_gone")})
	w.members.set(orgA, "usr_gone", "")
	if err := w.authz.Revoke(context.Background(), orgA, "usr_gone"); err != nil {
		t.Fatal(err)
	}
	if err := w.runStart(t, gone); err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, gone.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "may no longer use it") {
		t.Fatalf("task %+v", got)
	}
	if streams, _, _, _, _ := w.engine.calls(); len(streams) != 0 {
		t.Fatal("the revoked task ran")
	}
	if _, finished := w.handler.all(); !slices.Contains(finished, gone.ID) {
		t.Fatal("OnFinished wasn't called")
	}
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
}

// Jobs and hooks read a task only through the workspace they name, and
// bind what they run from the task row.
func TestJobsKeepToTheirWorkspace(t *testing.T) {
	w := newWorld(t)
	theirs := w.start(t, StartInput{OrgID: orgB, OnBehalfOf: personPtr(orgB, bob)})
	// A payload naming Engines with Ledger's task finds nothing.
	for _, job := range []func() error{
		func() error {
			return StartJob(w.svc).Handler(context.Background(), JobPayload{OrgID: orgA, TaskID: theirs.ID})
		},
		func() error {
			return ContinueJob(w.svc).Handler(context.Background(), JobPayload{OrgID: orgA, TaskID: theirs.ID})
		},
	} {
		if err := job(); err != nil {
			t.Fatal(err)
		}
	}
	if streams, continues, _, created, _ := w.engine.calls(); len(streams)+len(continues)+created != 0 {
		t.Fatal("a job ran another workspace's task")
	}
	// Engines' hooks never touch Ledger's task.
	ctx := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: orgA}.Cortex())
	h := &Hooks{Service: w.svc}
	runID := mustRunID(t, theirs.RunID)
	_ = h.OnRunReady(ctx, id.Nil, runID, readyNotice(w.clock.now(), false))
	_ = h.OnRunSuspended(ctx, id.Nil, runID, suspensionOf(orgA))
	_ = h.OnRunFailed(ctx, id.Nil, runID, errors.New("boom"))
	if got := w.store.task(t, theirs.ID); got.Status != StatusQueued {
		t.Fatalf("Ledger's task is %s", got.Status)
	}
	if n := len(w.pendingNamed(t, JobContinue)); n != 0 {
		t.Fatalf("%d continuations", n)
	}
	// Ledger's job binds Ledger.
	if err := w.runStart(t, theirs); err != nil {
		t.Fatal(err)
	}
	streams, _, _, _, _ := w.engine.calls()
	if len(streams) != 1 || shield.TenantFromContext(streams[0].ctx) != orgB || pipeline.TenantID(streams[0].ctx) != "nx_"+orgB {
		t.Fatalf("streams %+v", streams)
	}
}

func TestCancelAllCancelsEveryOpenTask(t *testing.T) {
	w := newWorld(t)
	running, waiting, done, queued := w.adaChat(t), w.adaChat(t), w.adaChat(t), w.adaChat(t)
	theirs := w.start(t, StartInput{OrgID: orgB, OnBehalfOf: personPtr(orgB, bob)})
	ctx := context.Background()
	mustMove(t, w, running, StatusRunning)
	mustMove(t, w, waiting, StatusWaiting)
	mustMove(t, w, done, StatusCompleted)
	if err := w.svc.CancelAll(ctx, orgA); err != nil {
		t.Fatal(err)
	}
	_, _, cancels, _, _ := w.engine.calls()
	slices.Sort(cancels)
	want := []string{running.RunID, waiting.RunID, queued.RunID}
	slices.Sort(want)
	if !slices.Equal(cancels, want) {
		t.Fatalf("cancelled runs %v, want %v", cancels, want)
	}
	for _, task := range []*Task{running, waiting, queued} {
		if got := w.store.task(t, task.ID); got.Status != StatusCancelled || got.FinishedAt == nil {
			t.Fatalf("%s is %s", task.ID, got.Status)
		}
	}
	if got := w.store.task(t, done.ID); got.Status != StatusCompleted {
		t.Fatalf("the completed task is %s", got.Status)
	}
	if got := w.store.task(t, theirs.ID); got.Status != StatusQueued {
		t.Fatalf("Ledger's task is %s", got.Status)
	}
	if _, finished := w.handler.all(); len(finished) != 3 {
		t.Fatalf("finished %v", finished)
	}
}

func mustMove(t *testing.T, w *world, task *Task, to string) {
	t.Helper()
	ok, err := w.store.Transition(context.Background(), task.OrgID, task.ID, Transition{From: open, Status: to})
	if err != nil || !ok {
		t.Fatalf("move %s to %s: %v %v", task.ID, to, ok, err)
	}
}

func TestChatTasksArePrivate(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})
	if _, err := w.svc.Get(ctx, person(orgA, ada), task.ID); err != nil {
		t.Fatalf("Ada: %v", err)
	}
	for _, p := range []security.Principal{person(orgA, bo), person(orgA, vi), person(orgB, bob)} {
		if _, err := w.svc.Get(ctx, p, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", p.UserID, err)
		}
		if _, err := w.svc.Events(ctx, p, task.ID, 0, 10); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s events: %v", p.UserID, err)
		}
		if err := w.svc.Cancel(ctx, p, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s cancel: %v", p.UserID, err)
		}
		list, err := w.svc.List(ctx, p, ListFilter{})
		if err != nil || len(list) != 0 {
			t.Fatalf("%s lists %v, %v", p.UserID, list, err)
		}
	}
	if _, err := w.svc.Get(ctx, person(orgA, al), task.ID); err != nil {
		t.Fatalf("the admin: %v", err)
	}
	// Background work is for everyone who may read its project.
	if err := w.svc.RegisterKind(KindSpec{Kind: KindInvestigation, Agent: "loomplane-investigation", MaxSteps: 10, DefaultMaxTokens: 10, MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	bg := w.start(t, StartInput{Kind: KindInvestigation, ProjectID: "prj_mono"})
	for _, user := range []string{bo, vi, al} {
		if _, err := w.svc.Get(ctx, person(orgA, user), bg.ID); err != nil {
			t.Fatalf("%s reads background work: %v", user, err)
		}
	}
	list, err := w.svc.List(ctx, person(orgA, bo), ListFilter{})
	if err != nil || len(list) != 1 || list[0].ID != bg.ID {
		t.Fatalf("Bo lists %+v, %v", list, err)
	}
	if list, err = w.svc.List(ctx, person(orgA, ada), ListFilter{Kind: KindChat}); err != nil || len(list) != 1 {
		t.Fatalf("Ada lists %+v, %v", list, err)
	}
	// Only a manager cancels background work.
	if err = w.svc.Cancel(ctx, person(orgA, bo), bg.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Bo cancels background work: %v", err)
	}
	if err = w.svc.Cancel(ctx, person(orgA, al), bg.ID); err != nil {
		t.Fatalf("the admin cancels: %v", err)
	}
	if err = w.svc.Cancel(ctx, person(orgA, al), bg.ID); !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("cancel twice: %v", err)
	}
}

func TestContinueRunRevokedFailsTheTask(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	mustMove(t, w, task, StatusWaiting)
	w.engine.continueErr = fmt.Errorf("cortex: continue: %w: the person the agent acts for may no longer use it", cortex.ErrPrincipalRevoked)
	if err := w.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, task.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "may no longer use it") {
		t.Fatalf("task %+v", got)
	}
	if _, finished := w.handler.all(); !slices.Equal(finished, []string{task.ID}) {
		t.Fatalf("finished %v", finished)
	}
}

func TestContinueRunOutcomes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{cortex.ErrRunLeased, StatusRunning},
		{cortex.ErrRunNotContinuable, StatusRunning},
		{cortex.ErrRunAlreadyDone, StatusRunning},
		{cortex.ErrEffectUncertain, StatusFailed},
		{cortex.ErrReclaimLimit, StatusFailed},
		{cortex.ErrRunNotRecoverable, StatusFailed},
	} {
		w := newWorld(t)
		task := w.adaChat(t)
		mustMove(t, w, task, StatusRunning)
		w.engine.continueErr = fmt.Errorf("continue: %w", c.err)
		if err := w.runContinue(t, task); err != nil {
			t.Fatalf("%v: the job failed: %v", c.err, err)
		}
		if got := w.store.task(t, task.ID); got.Status != c.want {
			t.Fatalf("%v: status %s, want %s", c.err, got.Status, c.want)
		}
	}
	// Anything else is the job's to retry.
	w := newWorld(t)
	task := w.adaChat(t)
	w.engine.continueErr = errors.New("the database went away")
	if err := w.runContinue(t, task); err == nil {
		t.Fatal("a transient error ended the job")
	}
}

func TestSweepRequeuesWhatWasLost(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fresh := w.adaChat(t)
	w.clock.advance(3 * time.Minute)
	recent := w.adaChat(t)
	lost := fresh
	stuck := w.adaChat(t)
	mustMove(t, w, stuck, StatusWaiting)
	if ok, err := w.store.ClaimContinue(ctx, orgA, stuck.ID, w.clock.now(), continueStale); err != nil || !ok {
		t.Fatal(ok, err)
	}
	w.clock.advance(6 * time.Minute)
	before := len(w.pendingNamed(t, JobStart))
	if err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	starts := w.pendingNamed(t, JobStart)[before:]
	ids := make([]string, 0, len(starts))
	for _, p := range starts {
		ids = append(ids, p.TaskID)
	}
	slices.Sort(ids)
	want := []string{lost.ID, recent.ID, stuck.ID}
	want = slices.DeleteFunc(want, func(id string) bool { return id == stuck.ID })
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("re-queued starts %v, want %v", ids, want)
	}
	if conts := w.pendingNamed(t, JobContinue); len(conts) != 1 || conts[0].TaskID != stuck.ID {
		t.Fatalf("continuations %+v", conts)
	}
	// Swept once, a task isn't queued again before it goes stale again.
	if err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(w.pendingNamed(t, JobStart)) - before; n != 2 {
		t.Fatalf("%d starts after a second sweep", n)
	}
	if n := len(w.pendingNamed(t, JobContinue)); n != 1 {
		t.Fatalf("%d continuations after a second sweep", n)
	}
}

func TestSweepJobRunsWhatJoinsIt(t *testing.T) {
	w := newWorld(t)
	called := 0
	def := SweepJob(w.svc, func(context.Context) error { called++; return nil })
	// The sweep runs on the default queue, which every server works, so
	// with AI off its schedule lands on the do-nothing handler.
	if def.Name != JobSweep || def.Opts.Queue != "default" {
		t.Fatalf("name %s, queue %s", def.Name, def.Opts.Queue)
	}
	if err := def.Handler(context.Background(), struct{}{}); err != nil || called != 1 {
		t.Fatalf("sweep: %v, called %d", err, called)
	}
}

// A plan counts as a person's request only by the task record's
// ReleasePlanID, which Start sets from the person's own call and checks:
// never from metadata, and never because the agent made the plan in their
// chat.
func TestRequestedComesFromTheTaskRecord(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	adaP := person(orgA, ada)
	forAda := security.AgentPrincipal(orgA, &adaP)
	release := &planning.Plan{ID: "plan_release", OrgID: orgA, ProjectID: "prj_mono", Revision: "r1", CreatedBy: ben()}
	w.plans.add(release)
	named := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono", ReleasePlanID: "plan_release", ReleaseRevision: "r1"})
	chat := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})
	bg := w.start(t, StartInput{ProjectID: "prj_mono"})
	call := func(task *Task, p security.Principal) agentops.Call {
		return agentops.Call{Principal: p, Scope: agentscope.Scope{OrgID: orgA, ProjectID: "prj_mono"}, TaskID: task.ID, RunID: task.RunID}
	}
	agentPlan := &planning.Plan{ID: "plan_made", OrgID: orgA, ProjectID: "prj_mono", Revision: "r1", CreatedBy: security.AgentSubject(orgA)}
	changed := *release
	changed.Revision = "r2"
	elsewhere := *release
	elsewhere.ProjectID = "prj_two"
	boP := person(orgA, bo)
	for _, c := range []struct {
		name string
		call agentops.Call
		plan *planning.Plan
		want bool
	}{
		{"the plan the person named, at its revision", call(named, forAda), release, true},
		{"the named plan after it changed", call(named, forAda), &changed, false},
		{"the named plan in another project", call(named, forAda), &elsewhere, false},
		{"a plan the agent made in the chat", call(chat, forAda), agentPlan, false},
		{"a chat that names no plan", call(chat, forAda), release, false},
		{"another person's call on the task", call(named, security.AgentPrincipal(orgA, &boP)), release, false},
		{"the agent alone", call(bg, security.AgentPrincipal(orgA, nil)), release, false},
		{"no task", agentops.Call{Principal: forAda, Scope: agentscope.Scope{OrgID: orgA}, RunID: named.RunID}, release, false},
		{"another run's call", agentops.Call{Principal: forAda, Scope: agentscope.Scope{OrgID: orgA}, TaskID: named.ID, RunID: chat.RunID}, release, false},
	} {
		got, err := w.svc.Requested(ctx, c.call, c.plan)
		if got != c.want || (c.want && err != nil) {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	bobP := person(orgB, bob)
	other := agentops.Call{Principal: security.AgentPrincipal(orgB, &bobP), Scope: agentscope.Scope{OrgID: orgB}, TaskID: named.ID, RunID: named.RunID}
	if got, _ := w.svc.Requested(ctx, other, release); got {
		t.Error("Ledger's call counted Engines' task")
	}
}

// Only a person who may approve the project's deployments names a release
// plan, and only one of the task's own workspace and project, at the
// revision they saw.
func TestStartValidatesTheReleasePlan(t *testing.T) {
	w := newWorld(t)
	w.plans.add(&planning.Plan{ID: "plan_mono", OrgID: orgA, ProjectID: "prj_mono", Revision: "r1"})
	w.plans.add(&planning.Plan{ID: "plan_two", OrgID: orgA, ProjectID: "prj_two", Revision: "r1"})
	w.plans.add(&planning.Plan{ID: "plan_ledger", OrgID: orgB, ProjectID: "prj_ledger", Revision: "r1"})
	ada := personPtr(orgA, ada)
	for name, in := range map[string]StartInput{
		"another org's plan":     {OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_ledger", ReleaseRevision: "r1"},
		"another project's plan": {OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_two", ReleaseRevision: "r1"},
		"no project":             {OnBehalfOf: ada, ReleasePlanID: "plan_mono", ReleaseRevision: "r1"},
		"no revision":            {OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_mono"},
		"a stale revision":       {OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_mono", ReleaseRevision: "r0"},
		"no plan":                {OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_none", ReleaseRevision: "r1"},
		"the agent alone":        {ProjectID: "prj_mono", ReleasePlanID: "plan_mono", ReleaseRevision: "r1"},
		"through metadata":       {OnBehalfOf: ada, ProjectID: "prj_mono", Metadata: map[string]string{metaReleasePlan: "plan_mono"}},
	} {
		in.Kind, in.OrgID, in.Input = KindChat, orgA, "Release it."
		if _, err := w.svc.Start(context.Background(), in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	for _, user := range []string{bo, vi} {
		_, err := w.svc.Start(context.Background(), StartInput{Kind: KindChat, OrgID: orgA, Input: "Release it.",
			OnBehalfOf: personPtr(orgA, user), ProjectID: "prj_mono", ReleasePlanID: "plan_mono", ReleaseRevision: "r1"})
		if !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s without deployment:approve: %v", user, err)
		}
	}
	if w.store.count() != 0 {
		t.Fatal("a task was written")
	}
	task := w.start(t, StartInput{OnBehalfOf: ada, ProjectID: "prj_mono", ReleasePlanID: "plan_mono", ReleaseRevision: "r1"})
	got := w.store.task(t, task.ID)
	if got.ReleasePlanID != "plan_mono" || got.ReleaseRevision != "r1" {
		t.Fatalf("task %+v", got)
	}
}

func ben() string { return "usr_ben" }

// The driver never approves anything itself: only cortex resuming a run a
// person approved at a checkpoint does, inside the engine. Nothing here
// calls the registry's Dispatch, whose approved flag would excuse a
// checkpoint.
func TestTheDriverNeverDispatchesItself(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	dispatchCall := regexp.MustCompile(`\.Dispatch\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if dispatchCall.Match(src) {
			t.Errorf("%s calls Dispatch", f)
		}
	}
}

// keep the engine import for the fake's types.
var _ = engine.EventToken

// allowAll is a promotion gate with every check green.
type allowAll struct{}

func (allowAll) Evaluate(context.Context, agentauthz.PromotionRequest) (agentauthz.GateDecision, error) {
	return agentauthz.GateDecision{Allowed: true}, nil
}

// Through the real authorizer, with autonomy on and every gate green: a
// release the person named when they started the task reaches the gate
// and is allowed; a plan the agent made in their chat is "nobody asked".
func TestOnlyTheReleaseAPersonNamedCounts(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.projects.projects["prj_mono"].AutonomousPromotion = true
	a := &agentauthz.Authorizer{Registry: w.registry, Projects: w.projects, Authz: w.authz, Requested: w.svc.Requested}
	a.SetGate(allowAll{})
	adaP := person(orgA, ada)
	forAda := security.AgentPrincipal(orgA, &adaP)
	release := &planning.Plan{ID: "plan_release", OrgID: orgA, ProjectID: "prj_mono", Revision: "r1", CreatedBy: security.AgentSubject(orgA)}
	w.plans.add(release)
	named := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono", ReleasePlanID: release.ID, ReleaseRevision: "r1"})
	chat := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), ProjectID: "prj_mono"})
	callIn := func(task *Task) agentops.Call {
		return agentops.Call{Principal: forAda, Scope: agentscope.Scope{OrgID: orgA, ProjectID: "prj_mono"}, TaskID: task.ID, RunID: task.RunID}
	}
	if allowed, failed, err := a.Promote(ctx, callIn(named), release); err != nil || !allowed {
		t.Fatalf("the named release: %v %v %v", allowed, failed, err)
	}
	allowed, failed, err := a.Promote(ctx, callIn(chat), release)
	if err != nil || allowed || len(failed) != 1 || !strings.Contains(failed[0], "nobody asked") {
		t.Fatalf("the chat's own plan: %v %v %v", allowed, failed, err)
	}
	alone := agentops.Call{Principal: security.AgentPrincipal(orgA, nil), Scope: agentscope.Scope{OrgID: orgA}, TaskID: named.ID, RunID: named.RunID}
	if allowed, _, _ = a.Promote(ctx, alone, release); allowed {
		t.Fatal("the agent alone promoted")
	}
}

// A cancel that lands after the job bound its task and before it drives
// the run stops the run from starting, wherever it lands.
func TestACancelDuringStartStopsTheRun(t *testing.T) {
	for _, when := range []string{"between bind and claim", "while the agent is ensured", "just before the stream"} {
		w := newWorld(t)
		task := w.adaChat(t)
		var once sync.Once
		cancel := func() {
			once.Do(func() {
				if err := w.svc.Cancel(context.Background(), person(orgA, ada), task.ID); err != nil {
					t.Error(err)
				}
			})
		}
		switch when {
		case "between bind and claim":
			w.svc.beforeClaim = cancel
		case "while the agent is ensured":
			w.engine.onGetAgent = cancel
		default:
			w.svc.beforeDrive = cancel
		}
		if err := w.runStart(t, task); err != nil {
			t.Fatal(err)
		}
		if streams, _, _, _, _ := w.engine.calls(); len(streams) != 0 {
			t.Fatalf("%s: the cancelled task's run started", when)
		}
		if got := w.store.task(t, task.ID); got.Status != StatusCancelled {
			t.Fatalf("%s: status %s", when, got.Status)
		}
	}
}

// A task that still can't start an hour after it was asked for fails,
// rather than being queued again every two minutes forever.
func TestSweepGivesUpOnATaskThatNeverStarts(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	w.clock.advance(61 * time.Minute)
	if err := w.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, task.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "couldn't start") {
		t.Fatalf("task %+v", got)
	}
	if n := len(w.pendingNamed(t, JobStart)); n != 1 {
		t.Fatalf("%d starts queued", n)
	}
}
