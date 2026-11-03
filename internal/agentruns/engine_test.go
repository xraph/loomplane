package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/llm"
	cortexsqlite "github.com/xraph/cortex/store/sqlite"
	"github.com/xraph/cortex/suspension"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"
	shieldsqlite "github.com/xraph/shield/store/sqlite"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// turn is one scripted model answer: text, or tool calls.
type turn struct {
	text  string
	calls []llm.ToolCall
}

// scriptedModel answers each request with the next turn, streaming, and
// keeps every request.
type scriptedModel struct {
	mu       sync.Mutex
	turns    []turn
	requests []*llm.Request
}

func (m *scriptedModel) next(req *llm.Request) (turn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.turns) == 0 {
		return turn{}, errors.New("the script ran out")
	}
	t := m.turns[0]
	m.turns = m.turns[1:]
	return t, nil
}

func (m *scriptedModel) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	t, err := m.next(req)
	if err != nil {
		return nil, err
	}
	return &llm.Response{Content: t.text, ToolCalls: t.calls, Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}, nil
}

func (m *scriptedModel) CompleteStream(_ context.Context, req *llm.Request) (llm.Stream, error) {
	t, err := m.next(req)
	if err != nil {
		return nil, err
	}
	finish := "stop"
	if len(t.calls) > 0 {
		finish = "tool_calls"
	}
	chunks := []*llm.Chunk{}
	if t.text != "" {
		chunks = append(chunks, &llm.Chunk{Content: t.text})
	}
	chunks = append(chunks, &llm.Chunk{ToolCalls: t.calls, FinishReason: finish})
	return &scriptedStream{chunks: chunks}, nil
}

func (m *scriptedModel) seen() []*llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

type scriptedStream struct{ chunks []*llm.Chunk }

func (s *scriptedStream) Next(context.Context) (*llm.Chunk, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	return c, nil
}

func (s *scriptedStream) Close() error { return nil }

func (s *scriptedStream) Usage() *llm.Usage {
	return &llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
}

func call(t *testing.T, callID, name string, args any) llm.ToolCall {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolCall{ID: callID, Name: name, Arguments: string(raw)}
}

// cortexSQLite is cortex's SQLite store in a file of the test's own,
// migrated: an in-memory database is one per connection.
func cortexSQLite(t *testing.T) *cortexsqlite.Store {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, filepath.Join(t.TempDir(), "cortex.db")); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := cortexsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// seededShield is shield over SQLite with the InfraAgent preset seeded
// for both workspaces, as the agent's extension seeds them.
func seededShield(t *testing.T) *shieldengine.Engine {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := shieldsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{orgA, orgB} {
		if _, err = presets.InfraAgent().Seed(ctx, st, org, agentscope.App); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := shieldengine.New(shieldengine.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// fakeTeams lists a team whose description a person typed, and keeps who
// asked.
type fakeTeams struct {
	mu    sync.Mutex
	asked []security.Principal
}

const teamDescription = "Owns the payments API and the ledger database."

func (f *fakeTeams) List(_ context.Context, p security.Principal) (*teams.TeamList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, p)
	return &teams.TeamList{Teams: []teams.Team{{Name: "Payments", Slug: "payments", Description: teamDescription}}}, nil
}

// fakeCreator is the projects service the operations call: it creates
// projects, and lists and reads the world's.
type fakeCreator struct {
	agentops.ProjectService
	projects *fakeProjects
	mu       sync.Mutex
	created  []string
}

func (f *fakeCreator) List(_ context.Context, p security.Principal) ([]*projects.Project, error) {
	var out []*projects.Project
	for _, proj := range f.projects.projects {
		if proj.OrgID == p.OrgID {
			c := *proj
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeCreator) Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error) {
	return f.projects.Environments(ctx, p, projectID)
}

func (f *fakeCreator) Create(_ context.Context, p security.Principal, in projects.CreateProjectInput) (*projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in.Name)
	return &projects.Project{ID: "prj_new", OrgID: p.OrgID, Name: in.Name}, nil
}

func (f *fakeCreator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

// askFirst is the risk table with one more rule: creating a project
// waits for a person at a checkpoint.
type askFirst struct{ cortex.ToolAuthorizer }

func (a askFirst) Authorize(ctx context.Context, s cortex.Subject, tc llm.ToolCall) error {
	if tc.Name == "create_project" {
		return cortex.ApprovalRequired{Reason: "a person approves new projects", TTL: time.Hour}
	}
	return a.ToolAuthorizer.Authorize(ctx, s, tc)
}

// realRun is a world whose engine is cortex itself, over an in-memory
// store, with the agent's operations, the risk table, the codec and the
// run hooks.
type realRun struct {
	*world
	eng      *engine.Engine
	model    *scriptedModel
	teams    *fakeTeams
	creator  *fakeCreator
	handler  *noticeHandler
	cortexDB any
}

// noticeHandler keeps every notice and finished task.
type noticeHandler struct {
	mu       sync.Mutex
	notices  []suspension.Notice
	finished []string
}

func (h *noticeHandler) OnSuspended(_ context.Context, _ *Task, n suspension.Notice) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, n)
	return nil
}

func (h *noticeHandler) OnFinished(_ context.Context, t *Task) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finished = append(h.finished, t.ID)
	return nil
}

func (h *noticeHandler) last(t *testing.T) suspension.Notice {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notices) == 0 {
		t.Fatal("no suspension notice")
	}
	return h.notices[len(h.notices)-1]
}

func newRealRun(t *testing.T, ask bool, turns ...turn) *realRun {
	t.Helper()
	return newRealRunWith(t, realOptions{ask: ask}, turns...)
}

// realOptions are what a real run is built with beyond the defaults.
type realOptions struct {
	ask   bool          // creating a project waits at a checkpoint
	gate  bool          // every promotion check green, and autonomy on
	lease time.Duration // run leases, off when zero
	model llm.Client    // instead of the scripted one
}

func newRealRunWith(t *testing.T, o realOptions, turns ...turn) *realRun {
	t.Helper()
	r := &realRun{world: newWorld(t), model: &scriptedModel{turns: turns}, teams: &fakeTeams{}, handler: &noticeHandler{}}
	r.creator = &fakeCreator{projects: r.projects}
	scanner := &agentsafety.Scanner{Engine: seededShield(t)}
	reg := agentops.NewRegistry()
	deps := &agentops.Deps{Authz: r.authz, Teams: r.teams, Safety: scanner, Projects: r.creator, Plans: r.plans}
	az := &agentauthz.Authorizer{Registry: reg, Authz: r.authz, Plans: r.plans, Projects: r.projects, Requested: r.svc.Requested, ApprovalWindow: time.Hour}
	deps.Promote = az.Promote
	agentops.Builtins(reg, deps)
	r.svc.registry = reg
	if o.gate {
		az.SetGate(allowAll{})
		r.projects.projects["prj_mono"].AutonomousPromotion = true
	}
	var authz cortex.ToolAuthorizer = az
	if o.ask {
		authz = askFirst{authz}
	}
	var model llm.Client = r.model
	if o.model != nil {
		model = o.model
	}
	db := cortexSQLite(t)
	r.cortexDB = db
	opts := append(reg.EngineOptions(),
		engine.WithRunLease(o.lease),
		engine.WithStore(db),
		engine.WithLLM(model),
		engine.WithToolAuthorizer(authz),
		engine.WithPrincipalCodec(r.svc.codec),
		engine.WithExtension(&Hooks{Service: r.svc}),
		engine.WithRejectionsReturnToModel(),
	)
	eng, err := engine.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	r.svc.engine = func() runner { return eng }
	r.svc.SetSuspensionHandler(r.handler)
	return r
}

func (r *realRun) status(t *testing.T, task *Task) string {
	t.Helper()
	return r.store.task(t, task.ID).Status
}

// toolMessage is the content of the tool message answering callID in the
// model's last request.
func (r *realRun) toolMessage(t *testing.T, callID string) string {
	t.Helper()
	reqs := r.model.seen()
	for _, m := range reqs[len(reqs)-1].Messages {
		if m.Role == "tool" && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no tool message for %s", callID)
	return ""
}

// A run started and resumed by jobs, on contexts that carry nothing,
// acts in its task's workspace as its task's person, and the text a tool
// quotes reaches the model quoted by shield under that workspace.
func TestARunIsBoundFromItsTaskAcrossAPause(t *testing.T) {
	r := newRealRun(t, false,
		turn{calls: []llm.ToolCall{call(t, "call_ask", "ask_person", map[string]string{"question": "Which team owns payments?"})}},
		turn{calls: []llm.ToolCall{call(t, "call_teams", "list_teams", map[string]string{})}},
		turn{text: "Payments owns it."},
	)
	task := r.adaChat(t)
	if err := r.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	if got := r.status(t, task); got != StatusWaiting {
		t.Fatalf("after the question: %s (%s)", got, r.store.task(t, task.ID).Error)
	}
	n := r.handler.last(t)
	if n.Reason != suspension.ReasonExternalTool || len(n.Pending) != 1 || n.Pending[0].Name != "ask_person" {
		t.Fatalf("notice %+v", n)
	}

	// The person answers. Another workspace's job and hooks can't move it.
	scoped := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: orgA}.Cortex())
	if err := r.eng.ProvideResults(scoped, mustRunID(t, task.RunID), engine.ResumeInput{
		ToolResults: []engine.ToolResult{{ToolCallID: n.Pending[0].ID, Content: "Ask the payments team."}},
	}); err != nil {
		t.Fatal(err)
	}
	if conts := r.pendingNamed(t, JobContinue); len(conts) != 1 || conts[0].TaskID != task.ID {
		t.Fatalf("continuations %+v", conts)
	}
	if err := ContinueJob(r.svc).Handler(context.Background(), JobPayload{OrgID: orgB, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.model.seen()) != 1 {
		t.Fatal("Ledger's continuation ran Engines' run")
	}
	if err := r.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	if got := r.store.task(t, task.ID); got.Status != StatusCompleted || got.Tokens == 0 {
		t.Fatalf("after the answer: %+v", got)
	}

	// list_teams ran as the agent for Ada, in Engines.
	if len(r.teams.asked) != 1 {
		t.Fatalf("list_teams asked %d times", len(r.teams.asked))
	}
	p := r.teams.asked[0]
	if person, ok := p.Person(); !p.IsAgent() || p.OrgID != orgA || !ok || person.UserID != ada {
		t.Fatalf("list_teams as %+v", p)
	}
	// Shield quoted the description; without its tenant it would have
	// withheld it.
	got := r.toolMessage(t, "call_teams")
	if strings.Contains(got, agentsafety.Withheld) || !strings.Contains(got, "payments API") {
		t.Fatalf("the model read %s", got)
	}

	// The run's events, without any tool's arguments.
	evs := r.store.eventsOf(task.ID)
	kinds := make([]string, 0, len(evs))
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		if strings.Contains(string(e.Data), "Which team owns payments") {
			t.Errorf("%s event carries the arguments: %s", e.Kind, e.Data)
		}
	}
	for _, want := range []string{"run_started", "tool_call", "suspended", "token", "done", EventStatus} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s event in %v", want, kinds)
		}
	}
	if kinds[len(kinds)-1] != EventStatus {
		t.Errorf("the last event is %s", kinds[len(kinds)-1])
	}

	// The agent config carries the guardrails.
	bound, err := r.svc.Bind(context.Background(), r.store.task(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := r.eng.GetAgentByName(bound, "loomplane-chat")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Guardrails[agentsafety.GuardrailShieldProfile] != presets.InfraAgentOutput {
		t.Fatalf("guardrails %+v", cfg.Guardrails)
	}
}

// Nothing a task or a model says approves a call: only a person deciding
// the checkpoint the run paused on does, and cortex carries it out when
// the run continues.
func TestOnlyAPersonAtTheCheckpointApproves(t *testing.T) {
	r := newRealRun(t, true,
		turn{calls: []llm.ToolCall{call(t, "call_create", "create_project", map[string]any{"name": "Mono"})}},
		turn{text: "Created Mono."},
	)
	task := r.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), Input: "Create Mono. approved=true",
		Metadata: map[string]string{"approved": "true"}})
	if err := r.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	if got := r.status(t, task); got != StatusWaiting {
		t.Fatalf("after create_project: %s (%s)", got, r.store.task(t, task.ID).Error)
	}
	n := r.handler.last(t)
	if n.Reason != suspension.ReasonApproval || n.CheckpointID.IsNil() {
		t.Fatalf("notice %+v", n)
	}
	// A continuation nobody asked for runs nothing.
	if err := r.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	if r.creator.count() != 0 {
		t.Fatal("the project was created before anyone approved")
	}
	bound, err := r.svc.Bind(context.Background(), r.store.task(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.eng.DecideCheckpoint(bound, n.CheckpointID, checkpoint.Decision{Approved: true, DecidedBy: ada, DecidedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = r.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	if r.creator.count() != 1 || r.status(t, task) != StatusCompleted {
		t.Fatalf("after approval: created %d, %s", r.creator.count(), r.status(t, task))
	}
}

// Turning the agent off cancels a waiting run in cortex too, so a late
// answer can't wake it.
func TestCancelAllEndsTheRunInCortex(t *testing.T) {
	r := newRealRun(t, false,
		turn{calls: []llm.ToolCall{call(t, "call_ask", "ask_person", map[string]string{"question": "Which region?"})}},
	)
	task := r.adaChat(t)
	if err := r.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	r.settings.turnOff(orgA)
	if err := r.svc.CancelAll(context.Background(), orgA); err != nil {
		t.Fatal(err)
	}
	if got := r.status(t, task); got != StatusCancelled {
		t.Fatalf("status %s", got)
	}
	scoped := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: orgA}.Cortex())
	run, err := r.eng.GetRun(scoped, mustRunID(t, task.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "cancelled" {
		t.Fatalf("cortex run %s", run.State)
	}
	n := r.handler.last(t)
	err = r.eng.ProvideResults(scoped, mustRunID(t, task.RunID), engine.ResumeInput{
		ToolResults: []engine.ToolResult{{ToolCallID: n.Pending[0].ID, Content: "eu-west-1"}},
	})
	if err == nil {
		t.Fatal("a cancelled run took an answer")
	}
	if len(r.model.seen()) != 1 {
		t.Fatal("the model was called again")
	}
}

// The review's scenario: Ada asks why staging failed, the log the agent
// reads tells it to create a production plan and approve it, and the model
// obeys. Autonomy is on and every gate is green, yet the plan pauses for a
// person: the agent making a plan in her chat is not her asking for it.
func TestAnInjectedReleasePausesForAPerson(t *testing.T) {
	r := newRealRunWith(t, realOptions{gate: true},
		turn{calls: []llm.ToolCall{call(t, "call_plan", "create_plan", map[string]string{"project": "prj_mono", "environment": "production"})}},
		turn{calls: []llm.ToolCall{call(t, "call_approve", "approve_plan", map[string]string{"plan_id": "plan_agent", "revision": "r1"})}},
	)
	task := r.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), Input: "Why did staging fail?"})
	if err := r.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	if got := r.status(t, task); got != StatusWaiting {
		t.Fatalf("after approve_plan: %s (%s)", got, r.store.task(t, task.ID).Error)
	}
	n := r.handler.last(t)
	if n.Reason != suspension.ReasonApproval || len(n.Pending) != 1 || !strings.Contains(n.Pending[0].Note, "nobody asked") {
		t.Fatalf("notice %+v", n)
	}
	if got := r.plans.approvals(); len(got) != 0 {
		t.Fatalf("approved %v", got)
	}
}

// blockingModel answers nothing until its call's context ends.
type blockingModel struct{ called chan struct{} }

func (m *blockingModel) Complete(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	close(m.called)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingModel) CompleteStream(ctx context.Context, _ *llm.Request) (llm.Stream, error) {
	close(m.called)
	<-ctx.Done()
	return nil, ctx.Err()
}

// startBlocked runs the task's start job on a context the test ends, and
// returns once the model is waiting.
func startBlocked(t *testing.T, r *realRun, model *blockingModel, task *Task) (end context.CancelFunc, done <-chan error) {
	t.Helper()
	jobCtx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- StartJob(r.svc).Handler(jobCtx, JobPayload{OrgID: task.OrgID, TaskID: task.ID}) }()
	select {
	case <-model.called:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never called")
	}
	return cancel, errc
}

func (r *realRun) cortexRun(t *testing.T, task *Task) string {
	t.Helper()
	scoped := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: task.OrgID}.Cortex())
	run, err := r.eng.GetRun(scoped, mustRunID(t, task.RunID))
	if err != nil {
		t.Fatal(err)
	}
	return string(run.State)
}

// When the Dispatch attempt ends under a run (its lease lost, its timeout,
// a shutdown), cortex hands the run off for a continuation instead of
// ending it, and the task stays open.
func TestAnAttemptThatEndsHandsTheRunOff(t *testing.T) {
	model := &blockingModel{called: make(chan struct{})}
	r := newRealRunWith(t, realOptions{lease: time.Minute, model: model})
	task := r.adaChat(t)
	end, done := startBlocked(t, r, model, task)
	end()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the job didn't return")
	}
	if got := r.cortexRun(t, task); got != "reclaimable" {
		t.Fatalf("cortex run %s, want reclaimable", got)
	}
	if got := r.status(t, task); Terminal(got) {
		t.Fatalf("task %s", got)
	}
	if conts := r.pendingNamed(t, JobContinue); len(conts) != 1 || conts[0].TaskID != task.ID {
		t.Fatalf("continuations %+v", conts)
	}
}

// A person's cancel still stops the run outright.
func TestACancelledTaskStopsItsRun(t *testing.T) {
	model := &blockingModel{called: make(chan struct{})}
	r := newRealRunWith(t, realOptions{lease: time.Minute, model: model})
	task := r.adaChat(t)
	end, done := startBlocked(t, r, model, task)
	defer end()
	if err := r.svc.Cancel(context.Background(), person(orgA, ada), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the job didn't return")
	}
	if got := r.cortexRun(t, task); got != "cancelled" {
		t.Fatalf("cortex run %s, want cancelled", got)
	}
	if got := r.status(t, task); got != StatusCancelled {
		t.Fatalf("task %s", got)
	}
	if conts := r.pendingNamed(t, JobContinue); len(conts) != 0 {
		t.Fatalf("continuations %+v", conts)
	}
}

// A person's approval resumes the run approved through cortex, with the
// person recorded as who decided: agentrequests decides on the task's bound
// context with the person as an agentauthz.Decider. A bare person on the
// context is still refused by the codec, as no run may carry one.
func TestAPersonsApprovalResumesTheRunAsApproved(t *testing.T) {
	r := newRealRun(t, true,
		turn{calls: []llm.ToolCall{call(t, "call_create", "create_project", map[string]any{"name": "Mono"})}},
		turn{text: "Created Mono."},
	)
	task := r.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), Input: "Create Mono."})
	if err := r.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	n := r.handler.last(t)
	bound, err := r.svc.Bind(context.Background(), r.store.task(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	decider := security.Principal{OrgID: orgA, UserID: ada}
	err = r.eng.DecideCheckpoint(cortex.WithPrincipal(bound, decider), n.CheckpointID, checkpoint.Decision{Approved: true, DecidedBy: ada})
	if !errors.Is(err, cortex.ErrPrincipalUnavailable) {
		t.Fatalf("a bare person deciding: %v", err)
	}
	err = r.eng.DecideCheckpoint(cortex.WithPrincipal(bound, agentauthz.Decider{Person: decider}), n.CheckpointID,
		checkpoint.Decision{Approved: true, DecidedBy: ada, Reason: "approved by Ada"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := r.eng.GetCheckpoint(bound, n.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Decision == nil || !cp.Decision.Approved || !strings.Contains(string(cp.Decision.DecidedByPrincipal), ada) {
		t.Fatalf("checkpoint decision %+v", cp.Decision)
	}
	if err = r.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	if r.creator.count() != 1 || r.status(t, task) != StatusCompleted {
		t.Fatalf("after approval: created %d, %s", r.creator.count(), r.status(t, task))
	}
}
