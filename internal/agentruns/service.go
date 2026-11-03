package agentruns

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/cortex"
	cortexagent "github.com/xraph/cortex/agent"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/suspension"
	dispatchengine "github.com/xraph/dispatch/engine"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
)

// Queue is the Dispatch queue the agent's jobs run on. The platform works
// it only when AI is on.
const Queue = "agent"

const (
	// queuedStale is how long a task may sit queued before the sweep
	// queues its start again.
	queuedStale = 2 * time.Minute
	// continueStale is how long a continuation may stay queued before a
	// hook or the sweep queues another. Cortex repeats its ready and
	// reclaimable notices on the same period.
	continueStale = 5 * time.Minute
	jobTimeout    = 30 * time.Minute
	jobRetries    = 5
	// maxInputBytes bounds what a task starts from.
	maxInputBytes = 64 << 10
	// maxErrorBytes bounds the reason a failed task shows.
	maxErrorBytes = 500
	// sweepLimit bounds the tasks one sweep queues again.
	sweepLimit = 100
	// watchPoll is how often a watch reads the log when no wake came.
	watchPoll = 2 * time.Second
)

// StartInput is a task to start.
type StartInput struct {
	Kind  Kind
	OrgID string
	// OnBehalfOf is the person, for a chat. Nil runs as the agent alone:
	// the watcher, or release preparation a job started.
	OnBehalfOf    *security.Principal
	ProjectID     string
	EnvironmentID string
	Input         string
	// SessionID continues a conversation; "" starts one.
	SessionID string
	// MaxTokens is 0 for the kind's default; above the kind's ceiling is
	// refused.
	MaxTokens int
	Metadata  map[string]string
}

// SuspensionHandler is told when a task's run waits on a person and when
// it finishes. agentrequests is the real one.
type SuspensionHandler interface {
	OnSuspended(ctx context.Context, task *Task, n suspension.Notice) error
	// OnFinished cancels the task's pending requests.
	OnFinished(ctx context.Context, task *Task) error
}

// Deps are what a Service needs.
type Deps struct {
	Store    Store
	Engine   func() *engine.Engine
	Jobs     func() *dispatchengine.Engine
	Settings *agentsettings.Service
	Binder   *agentscope.Binder
	Registry *agentops.Registry
	Authz    *security.Authorizer
	Projects *projects.Service
	// Codec writes a task's principal and reads it back, checking it may
	// still act: agentauthz.Codec, the one cortex runs with.
	Codec cortex.PrincipalCodec
	Wake  realtime.WakeSource
	Now   func() time.Time
	Warn  func(msg string, err error)
}

// runner is what the service asks of cortex. *engine.Engine is the real
// one.
type runner interface {
	StreamAgent(ctx context.Context, agentName, input string, overrides *engine.RunOverrides, events chan<- engine.StreamEvent) error
	ContinueRunStream(ctx context.Context, runID id.AgentRunID, events chan<- engine.StreamEvent) error
	CancelRun(ctx context.Context, runID id.AgentRunID) error
	GetAgentByName(ctx context.Context, name string) (*cortexagent.Config, error)
	CreateAgent(ctx context.Context, config *cortexagent.Config) error
	UpdateAgent(ctx context.Context, config *cortexagent.Config) error
	GetRun(ctx context.Context, runID id.AgentRunID) (*run.Run, error)
}

// settingsReader is what the service reads of the agent's settings.
type settingsReader interface {
	Effective(ctx context.Context, orgID string) (*agentsettings.Effective, error)
	RequireBudget(ctx context.Context, orgID string) error
}

type authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

type projectReader interface {
	Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error)
	Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error)
}

// Service runs the agent's tasks.
type Service struct {
	store    Store
	engine   func() runner
	jobs     func() *dispatchengine.Engine
	settings settingsReader
	binder   *agentscope.Binder
	registry *agentops.Registry
	authz    authorizer
	projects projectReader
	codec    cortex.PrincipalCodec
	wake     realtime.WakeSource
	now      func() time.Time
	warnFn   func(msg string, err error)
	poll     time.Duration

	mu      sync.RWMutex
	kinds   map[Kind]KindSpec
	handler SuspensionHandler
	// running cancels the runs this process's jobs are driving, by task.
	running map[string]context.CancelFunc
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{
		store: d.Store, jobs: d.Jobs, binder: d.Binder, registry: d.Registry, codec: d.Codec, wake: d.Wake,
		now: d.Now, warnFn: d.Warn, poll: watchPoll,
		kinds: map[Kind]KindSpec{}, running: map[string]context.CancelFunc{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.jobs == nil {
		s.jobs = func() *dispatchengine.Engine { return nil }
	}
	s.engine = func() runner { return nil }
	if d.Engine != nil {
		s.engine = func() runner {
			e := d.Engine()
			if e == nil {
				return nil
			}
			return e
		}
	}
	// Each is set only when given, so a missing one is nil, not an
	// interface holding a nil pointer.
	if d.Settings != nil {
		s.settings = d.Settings
	}
	if d.Authz != nil {
		s.authz = d.Authz
	}
	if d.Projects != nil {
		s.projects = d.Projects
	}
	return s
}

var _ agentsettings.Canceller = (*Service)(nil)

// SetSuspensionHandler sets who hears of pauses and finished tasks.
func (s *Service) SetSuspensionHandler(h SuspensionHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

func (s *Service) suspensionHandler() SuspensionHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handler
}

func (s *Service) warn(msg string, err error) {
	if s.warnFn != nil && err != nil {
		s.warnFn(msg, err)
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// require is Authorizer.Require, with a person who isn't a member refused
// as forbidden.
func (s *Service) require(ctx context.Context, p security.Principal, perm security.Permission, resource string) error {
	if s.authz == nil {
		return errors.New("agentruns: no authorizer is configured")
	}
	err := s.authz.Require(ctx, p, perm, resource)
	if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
		return fmt.Errorf("%w: %w", security.ErrForbidden, err)
	}
	return err
}

// allowed reports whether p holds perm, with a refusal as false.
func (s *Service) allowed(ctx context.Context, p security.Principal, perm security.Permission, resource string) (bool, error) {
	err := s.require(ctx, p, perm, resource)
	if errors.Is(err, security.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

// Start checks a task may run and queues its start job. It never calls the
// model: the job does, on whichever process works the agent's queue.
func (s *Service) Start(ctx context.Context, in StartInput) (*Task, error) {
	spec, ok := s.kind(in.Kind)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, in.Kind)
	}
	switch {
	case in.OrgID == "":
		return nil, invalid("a task names its workspace")
	case strings.TrimSpace(in.Input) == "":
		return nil, invalid("a task needs something to do")
	case len(in.Input) > maxInputBytes:
		return nil, invalid("a task's input is up to %d KiB", maxInputBytes>>10)
	}
	if _, reserved := in.Metadata[metaCreatedPlans]; reserved {
		return nil, invalid("%s is Loomplane's to set", metaCreatedPlans)
	}
	if s.settings == nil {
		return nil, errors.New("agentruns: the agent's settings aren't configured")
	}
	if _, err := s.settings.Effective(ctx, in.OrgID); err != nil {
		return nil, err
	}
	if err := s.settings.RequireBudget(ctx, in.OrgID); err != nil {
		return nil, err
	}
	var person *security.Principal
	if in.OnBehalfOf != nil {
		p := *in.OnBehalfOf
		if p.Kind != security.KindUser || p.OnBehalfOf != nil || p.UserID == "" || p.OrgID != in.OrgID {
			return nil, fmt.Errorf("%w: the agent acts only for a person of its own workspace", security.ErrForbidden)
		}
		if err := s.require(ctx, p, security.AgentUse, ""); err != nil {
			return nil, err
		}
		person = &p
	}
	if err := s.checkScope(ctx, in.OrgID, in.ProjectID, in.EnvironmentID); err != nil {
		return nil, err
	}
	requestedBy := ""
	if person != nil {
		requestedBy = person.UserID
		if in.ProjectID != "" {
			if err := s.require(ctx, *person, security.ProjectRead, in.ProjectID); err != nil {
				return nil, err
			}
		}
	}
	maxTokens := in.MaxTokens
	switch {
	case maxTokens == 0:
		maxTokens = spec.DefaultMaxTokens
	case maxTokens < 0 || maxTokens > spec.MaxTokens:
		return nil, invalid("a %s task may use 1 to %d tokens", in.Kind, spec.MaxTokens)
	}
	session, err := s.session(ctx, in, requestedBy)
	if err != nil {
		return nil, err
	}
	principal := security.AgentPrincipal(in.OrgID, person)
	if s.codec == nil {
		return nil, errors.New("agentruns: no principal codec is configured")
	}
	encoded, err := s.codec.EncodePrincipal(ctx, principal)
	if err != nil {
		return nil, err
	}
	taskID, err := typeid.Generate("atask")
	if err != nil {
		return nil, fmt.Errorf("agentruns: make a task ID: %w", err)
	}
	now := s.now().UTC()
	metadata := maps.Clone(in.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	task := &Task{
		ID: taskID.String(), OrgID: in.OrgID, ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID, Kind: in.Kind,
		RunID: id.NewAgentRunID().String(), SessionID: session, Status: StatusQueued, RequestedBy: requestedBy,
		MaxTokens: maxTokens, Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	}
	if cerr := s.store.Create(ctx, task, Record{Principal: encoded, Input: in.Input}); cerr != nil {
		return nil, cerr
	}
	if err = s.enqueue(ctx, JobStart, task); err != nil {
		s.fail(context.WithoutCancel(ctx), task, "the agent's run couldn't be queued")
		return nil, fmt.Errorf("queue the agent's run: %w", err)
	}
	return task, nil
}

// checkScope checks the project and environment are the workspace's, as
// the agent alone sees them.
func (s *Service) checkScope(ctx context.Context, orgID, projectID, environmentID string) error {
	if err := (agentscope.Scope{OrgID: orgID, ProjectID: projectID, EnvironmentID: environmentID}).Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if projectID == "" {
		return nil
	}
	if s.projects == nil {
		return errors.New("agentruns: no projects service is configured")
	}
	agent := security.AgentPrincipal(orgID, nil)
	if _, err := s.projects.Get(ctx, agent, projectID); err != nil {
		if errors.Is(err, projects.ErrNotFound) || errors.Is(err, security.ErrForbidden) {
			return invalid("project %s isn't in this workspace", projectID)
		}
		return err
	}
	if environmentID == "" {
		return nil
	}
	envs, err := s.projects.Environments(ctx, agent, projectID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(envs, func(e *projects.Environment) bool { return e.ID == environmentID }) {
		return invalid("environment %s isn't one of the project's", environmentID)
	}
	return nil
}

// session is the conversation a task joins. A new one gets its own ID,
// never cortex's shared default, so no two people's chats meet. One that
// continues must be the same person's, kind and scope.
func (s *Service) session(ctx context.Context, in StartInput, requestedBy string) (string, error) {
	if in.SessionID == "" {
		return id.NewSessionID().String(), nil
	}
	if _, err := id.ParseSessionID(in.SessionID); err != nil {
		return "", invalid("%q isn't a conversation", in.SessionID)
	}
	prev, err := s.store.Session(ctx, in.OrgID, in.SessionID)
	if errors.Is(err, ErrNotFound) {
		return "", invalid("there's no such conversation")
	}
	if err != nil {
		return "", err
	}
	if prev.Kind != in.Kind || prev.RequestedBy != requestedBy || prev.ProjectID != in.ProjectID || prev.EnvironmentID != in.EnvironmentID {
		return "", invalid("there's no such conversation")
	}
	return in.SessionID, nil
}

// enqueue queues one of the task's jobs on the agent's queue.
func (s *Service) enqueue(ctx context.Context, name string, t *Task) error {
	eng := s.jobs()
	if eng == nil {
		return errors.New("agentruns: the job engine isn't ready")
	}
	_, err := dispatchengine.Enqueue(ctx, eng, name, JobPayload{OrgID: t.OrgID, TaskID: t.ID},
		JobOptions()...)
	return err
}

// Get returns a task p may see: a chat to the person who started it and
// to those who manage the workspace's AI, background work to those who
// may use the agent and read its project. Anything else is ErrNotFound.
func (s *Service) Get(ctx context.Context, p security.Principal, taskID string) (*Task, error) {
	if p.OrgID == "" {
		return nil, ErrNotFound
	}
	t, err := s.store.Get(ctx, p.OrgID, taskID)
	if err != nil {
		return nil, err
	}
	ok, err := s.visible(ctx, p, t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return t, nil
}

func (s *Service) visible(ctx context.Context, p security.Principal, t *Task) (bool, error) {
	if p.OrgID != t.OrgID {
		return false, nil
	}
	if t.Kind == KindChat {
		if t.RequestedBy != "" && p.Kind == security.KindUser && p.UserID == t.RequestedBy {
			return s.allowed(ctx, p, security.AgentUse, "")
		}
		return s.allowed(ctx, p, security.AIManage, "")
	}
	ok, err := s.allowed(ctx, p, security.AgentUse, "")
	if !ok || err != nil {
		return ok, err
	}
	return s.allowed(ctx, p, security.ProjectRead, t.ProjectID)
}

// ByRun returns the task a run belongs to, in orgID.
func (s *Service) ByRun(ctx context.Context, orgID, runID string) (*Task, error) {
	return s.store.ByRun(ctx, orgID, runID)
}

// List returns the newest tasks p may see. It reads Limit tasks and leaves
// out the ones p may not see, so a page can come back short.
func (s *Service) List(ctx context.Context, p security.Principal, f ListFilter) ([]*Task, error) {
	switch {
	case f.Limit == 0:
		f.Limit = 50
	case f.Limit < 0 || f.Limit > 100:
		return nil, invalid("list 1 to 100 tasks")
	}
	if p.OrgID == "" {
		return []*Task{}, nil
	}
	tasks, err := s.store.List(ctx, p.OrgID, StoreFilter(f))
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		ok, err := s.visible(ctx, p, t)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// Events returns the events of a task p may see, after the sequence
// number given.
func (s *Service) Events(ctx context.Context, p security.Principal, taskID string, after int64, limit int) ([]Event, error) {
	switch {
	case limit == 0:
		limit = 200
	case limit < 0 || limit > 1000:
		return nil, invalid("read 1 to 1000 events")
	}
	t, err := s.Get(ctx, p, taskID)
	if err != nil {
		return nil, err
	}
	return s.store.Events(ctx, t.OrgID, t.ID, after, limit)
}

// Cancel stops a task: its requester or someone who manages the
// workspace's AI may.
func (s *Service) Cancel(ctx context.Context, p security.Principal, taskID string) error {
	t, err := s.Get(ctx, p, taskID)
	if err != nil {
		return err
	}
	if Terminal(t.Status) {
		return ErrNotCancelable
	}
	if t.RequestedBy == "" || p.UserID != t.RequestedBy {
		if rerr := s.require(ctx, p, security.AIManage, ""); rerr != nil {
			return rerr
		}
	}
	moved, err := s.cancel(ctx, t)
	if err != nil {
		return err
	}
	if !moved {
		return ErrNotCancelable
	}
	return nil
}

// CancelAll cancels every open task of the workspace: the agent turned
// off. It's agentsettings.Canceller.
func (s *Service) CancelAll(ctx context.Context, orgID string) error {
	tasks, err := s.store.Open(ctx, orgID)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range tasks {
		if _, cerr := s.cancel(ctx, t); cerr != nil {
			errs = append(errs, fmt.Errorf("cancel agent task %s: %w", t.ID, cerr))
		}
	}
	return errors.Join(errs...)
}

// cancel ends the task's run in cortex, stops it here if this process is
// driving it, and marks the task cancelled.
func (s *Service) cancel(ctx context.Context, t *Task) (bool, error) {
	s.stopLocal(t.ID)
	var runErr error
	if eng := s.engine(); eng != nil {
		if runID, err := id.ParseAgentRunID(t.RunID); err == nil {
			err = eng.CancelRun(s.runContext(ctx, t), runID)
			if err != nil && !errors.Is(err, cortex.ErrRunNotFound) && !errors.Is(err, cortex.ErrInvalidState) && !errors.Is(err, cortex.ErrNotSuspended) {
				runErr = err
			}
		}
	}
	moved, err := s.finish(context.WithoutCancel(ctx), t, StatusCancelled, -1, "")
	return moved, errors.Join(runErr, err)
}

// runContext is ctx bound to the task when it can be, and otherwise ctx
// with the task's cortex scope alone, which is all cancelling a run needs:
// a workspace that just turned the agent off can't be bound any more.
func (s *Service) runContext(ctx context.Context, t *Task) context.Context {
	if bound, err := s.Bind(ctx, t); err == nil {
		return bound
	}
	scope := agentscope.Scope{OrgID: t.OrgID, ProjectID: t.ProjectID, EnvironmentID: t.EnvironmentID}
	return agentops.WithTaskID(cortex.WithScope(ctx, scope.Cortex()), t.ID)
}

// Bind returns ctx carrying the task's scope and principal for every
// library, through agentscope.Binder, and the task for audit. Both come
// from the task's stored row, never from the caller or the model: the
// principal is read back through the codec, which checks the agent and
// the person may still act. agentrequests resumes runs with it.
func (s *Service) Bind(ctx context.Context, task *Task) (context.Context, error) {
	bound, _, _, err := s.bindTask(ctx, task.OrgID, task.ID)
	return bound, err
}

// bindTask binds the stored task orgID/taskID and returns it with its
// record.
func (s *Service) bindTask(ctx context.Context, orgID, taskID string) (context.Context, *Task, *Record, error) {
	t, err := s.store.Get(ctx, orgID, taskID)
	if err != nil {
		return ctx, nil, nil, err
	}
	rec, err := s.store.Record(ctx, t.OrgID, t.ID)
	if err != nil {
		return ctx, t, nil, err
	}
	if s.codec == nil || s.binder == nil {
		return ctx, t, rec, errors.New("agentruns: the codec and the scope binder aren't configured")
	}
	decoded, err := s.codec.DecodePrincipal(ctx, rec.Principal)
	if err != nil {
		return ctx, t, rec, err
	}
	p, ok := decoded.(security.Principal)
	if !ok || !p.IsAgent() || p.OrgID != t.OrgID {
		return ctx, t, rec, fmt.Errorf("%w: the task's principal isn't its workspace's agent", cortex.ErrPrincipalRevoked)
	}
	person, acting := p.Person()
	if acting != (t.RequestedBy != "") || (acting && person.UserID != t.RequestedBy) {
		return ctx, t, rec, fmt.Errorf("%w: the task's principal isn't the person who asked", cortex.ErrPrincipalRevoked)
	}
	bound, err := s.binder.Bind(ctx, p, agentscope.Scope{OrgID: t.OrgID, ProjectID: t.ProjectID, EnvironmentID: t.EnvironmentID})
	if err != nil {
		return ctx, t, rec, err
	}
	return agentops.WithTaskID(bound, t.ID), t, rec, nil
}

// Requested reports whether a stored record says the run's person asked
// for plan: the task they started names it (MetaPlanID), or the agent
// created it during their task. It's agentauthz.Authorizer.Requested.
// Nothing the model passes counts, and the agent alone never has a
// request.
func (s *Service) Requested(ctx context.Context, call agentops.Call, plan *planning.Plan) (bool, error) {
	person, ok := call.Principal.Person()
	if !ok || person.UserID == "" || call.TaskID == "" || plan == nil {
		return false, nil
	}
	t, err := s.store.Get(ctx, call.Principal.OrgID, call.TaskID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if t.RunID != call.RunID || t.RequestedBy != person.UserID || plan.OrgID != t.OrgID {
		return false, nil
	}
	if named := t.Metadata[MetaPlanID]; named != "" && named == plan.ID {
		return true, nil
	}
	return plan.CreatedBy == security.AgentSubject(t.OrgID) && slices.Contains(strings.Fields(t.Metadata[metaCreatedPlans]), plan.ID), nil
}

// RecordPlan records on the call's task a plan the agent created during
// it. It's agentops.Deps.PlanCreated.
func (s *Service) RecordPlan(ctx context.Context, call agentops.Call, plan *planning.Plan) {
	if call.TaskID == "" || plan == nil || plan.CreatedBy != security.AgentSubject(call.Principal.OrgID) {
		return
	}
	t, err := s.store.Get(ctx, call.Principal.OrgID, call.TaskID)
	if err != nil {
		s.warn("agentruns: couldn't read the task a plan was created in", err)
		return
	}
	if t.RunID != call.RunID {
		return
	}
	if err = s.store.AddPlan(context.WithoutCancel(ctx), t.OrgID, t.ID, plan.ID); err != nil {
		s.warn("agentruns: couldn't record the plan the agent created", err)
	}
}

// Sweep queues again what was lost: a task queued longer than two
// minutes gets its start job again, and a run whose continuation was
// queued five minutes ago and never started gets another. Cortex's own
// sweeper repeats the ready and reclaimable notices of runs nobody
// continued, which the hooks turn into continuations.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now().UTC()
	stale, err := s.store.Stale(ctx, now.Add(-queuedStale), now.Add(-continueStale), sweepLimit)
	if err != nil {
		return err
	}
	var errs []error
	for _, st := range stale {
		t, gerr := s.store.Get(ctx, st.OrgID, st.ID)
		if gerr != nil {
			errs = append(errs, gerr)
			continue
		}
		if st.Queued {
			if t.Status != StatusQueued {
				continue
			}
			if merr := s.store.MarkEnqueued(ctx, t.OrgID, t.ID, now); merr != nil {
				errs = append(errs, merr)
				continue
			}
			errs = append(errs, s.enqueue(ctx, JobStart, t))
			continue
		}
		errs = append(errs, s.continueLater(ctx, t))
	}
	return errors.Join(errs...)
}

// continueLater queues the task's continuation, unless one was queued in
// the last five minutes.
func (s *Service) continueLater(ctx context.Context, t *Task) error {
	claimed, err := s.store.ClaimContinue(ctx, t.OrgID, t.ID, s.now().UTC(), continueStale)
	if err != nil || !claimed {
		return err
	}
	if err = s.enqueue(ctx, JobContinue, t); err != nil {
		// The next notice or sweep tries again.
		return errors.Join(err, s.store.ClearContinue(ctx, t.OrgID, t.ID))
	}
	return nil
}

// fail marks the task failed with why.
func (s *Service) fail(ctx context.Context, t *Task, why string) {
	if _, err := s.finish(ctx, t, StatusFailed, -1, why); err != nil {
		s.warn("agentruns: couldn't mark a task failed", err)
	}
}

// finish moves an open task to a terminal status, with the tokens its run
// used when tokens isn't negative, and tells the suspension handler. It
// reports whether the task moved; a task that had already finished stays
// as it was.
func (s *Service) finish(ctx context.Context, t *Task, status string, tokens int, why string) (bool, error) {
	why = reason(why)
	tr := Transition{From: open, Status: status, Error: why}
	// A run this process is driving still has events on their way, its
	// last outcome among them: the pump writes the final status after
	// them, so a watcher never stops short of them.
	if !s.isLocal(t.ID) {
		tr.Event = statusEvent(status, why)
	}
	if tokens >= 0 {
		tr.Tokens = &tokens
	}
	moved, err := s.store.Transition(ctx, t.OrgID, t.ID, tr)
	if err != nil || !moved {
		return moved, err
	}
	if h := s.suspensionHandler(); h != nil {
		done, gerr := s.store.Get(ctx, t.OrgID, t.ID)
		if gerr != nil {
			done = t
		}
		if herr := h.OnFinished(ctx, done); herr != nil {
			s.warn("agentruns: couldn't settle a finished task's requests", herr)
		}
	}
	return true, nil
}

// move moves an open task to running or waiting.
func (s *Service) move(ctx context.Context, t *Task, from []string, to string) {
	if _, err := s.store.Transition(ctx, t.OrgID, t.ID, Transition{From: from, Status: to, Event: statusEvent(to, "")}); err != nil {
		s.warn("agentruns: couldn't record a task's status", err)
	}
}

func (s *Service) trackLocal(taskID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[taskID] = cancel
}

func (s *Service) untrackLocal(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, taskID)
}

func (s *Service) isLocal(taskID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.running[taskID]
	return ok
}

func (s *Service) stopLocal(taskID string) {
	s.mu.Lock()
	cancel := s.running[taskID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// reasons are the failures an admin can act on, which a failed task names
// as they are instead of the chain of errors around them.
var reasons = []error{
	agentsettings.ErrKeyUnavailable, agentsettings.ErrBudgetExhausted, agentsettings.ErrDisabled,
}

// reason is why as a failed task shows it: the sentence an admin can act
// on when there is one, otherwise the text, cut to maxErrorBytes.
func reason(why string) string {
	for _, known := range reasons {
		if strings.Contains(why, known.Error()) {
			return known.Error()
		}
	}
	why = strings.TrimSpace(why)
	if len(why) <= maxErrorBytes {
		return why
	}
	cut := maxErrorBytes
	for cut > 0 && !utf8RuneStart(why[cut]) {
		cut--
	}
	return why[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
