package agentrequests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

const (
	maxAnswerRunes = 4000
	maxReasonRunes = 1000
	maxSecretBytes = 65536
	maxListLimit   = 100
	listLimit      = 50
)

// Resumer continues a paused run. *engine.Engine is the real one.
type Resumer interface {
	DecideCheckpoint(ctx context.Context, checkpointID id.CheckpointID, d checkpoint.Decision) error
	ProvideResults(ctx context.Context, runID id.AgentRunID, in engine.ResumeInput) error
}

// Runs is what the service asks of the run driver. *agentruns.Service is
// the real one.
type Runs interface {
	ByRun(ctx context.Context, orgID, runID string) (*agentruns.Task, error)
	// Bind is the task's scope and principal, from its stored row, for
	// every library: what a request is scanned and resumed under.
	Bind(ctx context.Context, task *agentruns.Task) (context.Context, error)
	RecordRequest(ctx context.Context, t *agentruns.Task, requestID, kind string) error
}

// Plans reads and rejects plans. *planning.Service is the real one.
type Plans interface {
	Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error)
	Reject(ctx context.Context, p security.Principal, id string, in planning.DecideInput) (*planning.Plan, error)
}

// Projects reads projects. *projects.Service is the real one.
type Projects interface {
	Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error)
	List(ctx context.Context, p security.Principal) ([]*projects.Project, error)
	Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error)
}

// Variables sets a secret as the person who typed it.
// *environments.Service is the real one.
type Variables interface {
	ListProject(ctx context.Context, p security.Principal, projectID string) ([]*environments.Variable, error)
	CreateProject(ctx context.Context, p security.Principal, projectID string, in environments.CreateVariableInput) (*environments.Variable, error)
	Update(ctx context.Context, p security.Principal, id string, in environments.UpdateVariableInput) (*environments.Variable, error)
}

// GitHub reads the workspace's GitHub installations.
// *repositories.Connections is the real one.
type GitHub interface {
	Status(ctx context.Context, p security.Principal) (*repositories.GitHubStatus, error)
}

// Connections lists the workspace's cloud connections.
// *connections.Service is the real one.
type Connections interface {
	List(ctx context.Context, p security.Principal) ([]*connections.Connection, error)
}

// Authorizer is *security.Authorizer.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Safety scans what the agent sends people and what people send the
// agent. *agentsafety.Scanner is the real one.
type Safety interface {
	Outbound(ctx context.Context, text string) (string, error)
	Untrusted(ctx context.Context, source, text string) string
}

// Teams routes requests. *teams.Service is the real one.
type Teams interface {
	Resolve(ctx context.Context, q notifications.Querier, orgID, projectID, named, reason string) (teams.Route, error)
	IsMember(ctx context.Context, q notifications.Querier, orgID, teamID, userID string) (bool, error)
}

// People names a person for the model and for notices.
type People interface {
	Name(ctx context.Context, userID string) string
}

// Deps are what a Service needs.
type Deps struct {
	Store       Store
	Engine      func() Resumer
	Runs        Runs
	Plans       Plans
	Projects    Projects
	Variables   Variables
	GitHub      GitHub
	Connections Connections
	Authz       Authorizer
	Safety      Safety
	Teams       Teams
	People      People
	// DB is where team membership is read outside a request's own
	// transaction.
	DB    notifications.Querier
	Audit projects.Auditor
	// Enqueue queues a notice in the request's transaction; nil is
	// notifications.Enqueue.
	Enqueue func(ctx context.Context, q notifications.Execer, n notifications.Notice) error
	Now     func() time.Time
	Warn    func(msg string, err error)
}

// Service turns the agent's pauses into requests, and settles them.
type Service struct {
	d       Deps
	enqueue func(ctx context.Context, q notifications.Execer, n notifications.Notice) error
	now     func() time.Time
}

var _ agentruns.SuspensionHandler = (*Service)(nil)

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{d: d, enqueue: d.Enqueue, now: d.Now}
	if s.enqueue == nil {
		s.enqueue = notifications.Enqueue
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

func (s *Service) warn(msg string, err error) {
	if s.d.Warn != nil && err != nil {
		s.d.Warn(msg, err)
	}
}

func (s *Service) engine() (Resumer, error) {
	if s.d.Engine == nil {
		return nil, errors.New("agentrequests: cortex isn't running")
	}
	e := s.d.Engine()
	if e == nil {
		return nil, errors.New("agentrequests: cortex isn't running")
	}
	return e, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// require is Authorizer.Require, with someone who isn't a member refused
// as forbidden.
func (s *Service) require(ctx context.Context, p security.Principal, perm security.Permission, resource string) error {
	if s.d.Authz == nil {
		return errors.New("agentrequests: no authorizer is configured")
	}
	err := s.d.Authz.Require(ctx, p, perm, resource)
	if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
		return fmt.Errorf("%w: %w", security.ErrForbidden, err)
	}
	return err
}

func (s *Service) allowed(ctx context.Context, p security.Principal, perm security.Permission, resource string) (bool, error) {
	err := s.require(ctx, p, perm, resource)
	if errors.Is(err, security.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

// person checks p is a person of a workspace who may use the agent. The
// agent and Loomplane automation never see or settle requests: the agent
// answering itself would make every request moot.
func (s *Service) person(ctx context.Context, p security.Principal) error {
	if p.OrgID == "" || p.UserID == "" || p.Kind != security.KindUser || p.OnBehalfOf != nil {
		return fmt.Errorf("%w: only a person answers the agent's requests", security.ErrForbidden)
	}
	return s.require(ctx, p, security.AgentUse, "")
}

// Get returns a request p may see: anyone who may use the agent sees the
// workspace's requests, on the projects they can read.
func (s *Service) Get(ctx context.Context, p security.Principal, reqID string) (*Request, error) {
	if err := s.person(ctx, p); err != nil {
		return nil, err
	}
	r, err := s.d.Store.Get(ctx, p.OrgID, reqID)
	if err != nil {
		return nil, err
	}
	if r.ProjectID != "" {
		ok, aerr := s.allowed(ctx, p, security.ProjectRead, r.ProjectID)
		if aerr != nil {
			return nil, aerr
		}
		if !ok {
			return nil, ErrNotFound
		}
	}
	return r, nil
}

// List returns the newest requests p may see. With For "me", only those
// asked of p, routed to a team p is in, or routed to the workspace's
// admins when p manages its AI.
func (s *Service) List(ctx context.Context, p security.Principal, f ListFilter) (*RequestList, error) {
	sf := StoreFilter{ProjectID: f.ProjectID, Limit: f.Limit}
	switch f.Status {
	case "", StatusPending:
		sf.Status = StatusPending
	case "all":
	default:
		return nil, invalid("status is pending or all")
	}
	switch {
	case sf.Limit == 0:
		sf.Limit = listLimit
	case sf.Limit < 0 || sf.Limit > maxListLimit:
		return nil, invalid("list 1 to %d requests", maxListLimit)
	}
	if f.For != "" && f.For != "me" {
		return nil, invalid(`for is "me" or nothing`)
	}
	if err := s.person(ctx, p); err != nil {
		return nil, err
	}
	if f.For == "me" {
		admin, err := s.allowed(ctx, p, security.AIManage, "")
		if err != nil {
			return nil, err
		}
		sf.Mine, sf.Admin = p.UserID, admin
	}
	rows, err := s.d.Store.List(ctx, p.OrgID, sf)
	if err != nil {
		return nil, err
	}
	readable := map[string]bool{}
	out := make([]*Request, 0, len(rows))
	for _, r := range rows {
		if r.ProjectID != "" {
			ok, seen := readable[r.ProjectID]
			if !seen {
				if ok, err = s.allowed(ctx, p, security.ProjectRead, r.ProjectID); err != nil {
					return nil, err
				}
				readable[r.ProjectID] = ok
			}
			if !ok {
				continue
			}
		}
		out = append(out, r)
	}
	return &RequestList{Requests: out}, nil
}

// open is a pending request p may see, of one of kinds.
func (s *Service) open(ctx context.Context, p security.Principal, reqID string, kinds ...Kind) (*Request, error) {
	r, err := s.Get(ctx, p, reqID)
	if err != nil {
		return nil, err
	}
	if len(kinds) > 0 && !slices.Contains(kinds, r.Kind) {
		if r.Kind == KindApprovePlan {
			return nil, invalid("approve or reject the plan itself")
		}
		return nil, invalid("a %s request isn't settled this way", r.Kind)
	}
	if r.Status != StatusPending {
		return nil, ErrNotPending
	}
	return r, nil
}

// maySettle checks p may settle r: the permission r's kind needs, on r's
// own project as stored. A question goes to whoever was asked: the asker,
// a current member of the team it was routed to, or someone who manages
// the workspace's AI.
func (s *Service) maySettle(ctx context.Context, p security.Principal, r *Request) error {
	switch r.Kind {
	case KindAnswer:
		if r.AskedFor != "" && p.UserID == r.AskedFor {
			return nil
		}
		if r.TeamID != "" && s.d.Teams != nil {
			member, err := s.d.Teams.IsMember(ctx, s.d.DB, r.OrgID, r.TeamID, p.UserID)
			if err != nil {
				return err
			}
			if member {
				return nil
			}
		}
		return s.require(ctx, p, security.AIManage, "")
	case KindProvideSecret:
		return s.require(ctx, p, security.VariableManage, r.ProjectID)
	case KindConnectGitHub:
		return s.require(ctx, p, security.RepositoryConnect, "")
	case KindCreateConnection:
		return s.require(ctx, p, security.CloudAccountManage, "")
	case KindApprovePlan:
		return s.require(ctx, p, security.DeploymentApprove, r.ProjectID)
	}
	return fmt.Errorf("%w: unknown request kind %q", security.ErrForbidden, r.Kind)
}

// Answer answers a question, and the run goes on with the answer.
func (s *Service) Answer(ctx context.Context, p security.Principal, reqID string, in AnswerInput) (*Request, error) {
	r, err := s.open(ctx, p, reqID, KindAnswer)
	if err != nil {
		return nil, err
	}
	if aerr := s.maySettle(ctx, p, r); aerr != nil {
		return nil, aerr
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || utf8.RuneCountInString(text) > maxAnswerRunes {
		return nil, invalid("an answer is 1 to %d characters", maxAnswerRunes)
	}
	return s.settle(ctx, p, r, Settlement{Status: StatusAnswered, Outcome: OutcomeAnswered, AnsweredBy: p.UserID, Answer: text}, nil)
}

// ProvideSecret sets the secret the agent asked for, as p, and tells the
// run it's set. The value goes to the variables service and nowhere else:
// not the request, the model, an event, the audit log or a log line.
func (s *Service) ProvideSecret(ctx context.Context, p security.Principal, reqID string, in SecretInput) (*Request, error) {
	r, err := s.open(ctx, p, reqID, KindProvideSecret)
	if err != nil {
		return nil, err
	}
	if aerr := s.maySettle(ctx, p, r); aerr != nil {
		return nil, aerr
	}
	if in.Value == "" || len(in.Value) > maxSecretBytes {
		return nil, invalid("a secret is 1 to %d bytes", maxSecretBytes)
	}
	set := func(ctx context.Context) error { return s.setSecret(ctx, p, r, in.Value) }
	return s.settle(ctx, p, r, Settlement{Status: StatusAnswered, Outcome: OutcomeProvided, AnsweredBy: p.UserID}, set)
}

// setSecret writes the secret as p: an update when the variable exists,
// else a new one.
func (s *Service) setSecret(ctx context.Context, p security.Principal, r *Request, value string) error {
	if s.d.Variables == nil {
		return errors.New("agentrequests: the variables service isn't configured")
	}
	vars, err := s.d.Variables.ListProject(ctx, p, r.ProjectID)
	if err != nil {
		return secretError(err)
	}
	for _, v := range vars {
		if v.Scope != environments.ScopeOrganization && v.Key == r.Target.Key && v.Environment == r.Target.Environment && v.Service == r.Target.Service {
			secret := true
			_, err = s.d.Variables.Update(ctx, p, v.ID, environments.UpdateVariableInput{Value: value, Secret: &secret})
			return secretError(err)
		}
	}
	_, err = s.d.Variables.CreateProject(ctx, p, r.ProjectID, environments.CreateVariableInput{
		Key: r.Target.Key, Value: value, Secret: true, Environment: r.Target.Environment, Service: r.Target.Service,
	})
	return secretError(err)
}

// secretError is the variables service's error as the request API answers
// it. None of them carries the value.
func secretError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, environments.ErrInvalid) {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// Complete says a GitHub or cloud connection is made. Loomplane checks it
// is, and the run goes on with what was connected.
func (s *Service) Complete(ctx context.Context, p security.Principal, reqID string) (*Request, error) {
	r, err := s.open(ctx, p, reqID, KindConnectGitHub, KindCreateConnection)
	if err != nil {
		return nil, err
	}
	if aerr := s.maySettle(ctx, p, r); aerr != nil {
		return nil, aerr
	}
	st, done, err := s.connected(ctx, p, r)
	if err != nil {
		return nil, err
	}
	if !done {
		return nil, ErrNotDone
	}
	return s.settle(ctx, p, r, st, nil)
}

// connected checks, as p, whether what r asks for has been connected since
// r was made, and how r settles if it has.
func (s *Service) connected(ctx context.Context, p security.Principal, r *Request) (Settlement, bool, error) {
	switch r.Kind {
	case KindConnectGitHub:
		if s.d.GitHub == nil {
			return Settlement{}, false, errors.New("agentrequests: GitHub isn't configured")
		}
		st, err := s.d.GitHub.Status(ctx, p)
		if err != nil {
			return Settlement{}, false, err
		}
		var accounts []string
		for _, i := range st.Installations {
			if !i.CreatedAt.Before(r.CreatedAt) && !slices.Contains(accounts, i.Account) {
				accounts = append(accounts, i.Account)
			}
		}
		if len(accounts) == 0 {
			return Settlement{}, false, nil
		}
		by := ""
		if p.Kind == security.KindUser && !p.IsAgent() {
			by = p.UserID
		}
		return Settlement{Status: StatusAnswered, Outcome: OutcomeConnected, AnsweredBy: by, Answer: strings.Join(accounts, ", ")}, true, nil
	case KindCreateConnection:
		if s.d.Connections == nil {
			return Settlement{}, false, errors.New("agentrequests: cloud connections aren't configured")
		}
		conns, err := s.d.Connections.List(ctx, p)
		if err != nil {
			return Settlement{}, false, err
		}
		for _, c := range conns {
			if strings.EqualFold(c.Cloud, r.Target.Cloud) && !c.CreatedAt.Before(r.CreatedAt) {
				by := c.CreatedBy
				if by == "" && !p.IsAgent() {
					by = p.UserID
				}
				return Settlement{Status: StatusAnswered, Outcome: OutcomeConnected, AnsweredBy: by, Answer: c.Name}, true, nil
			}
		}
		return Settlement{}, false, nil
	}
	return Settlement{}, false, invalid("a %s request isn't completed", r.Kind)
}

// Decline says no. A plan approval is declined by rejecting the plan, as p,
// with the reason; anything else goes back to the run as p's refusal.
func (s *Service) Decline(ctx context.Context, p security.Principal, reqID string, in DeclineInput) (*Request, error) {
	r, err := s.open(ctx, p, reqID)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return nil, invalid("a reason is at most %d characters", maxReasonRunes)
	}
	if r.Kind == KindApprovePlan {
		return s.declinePlan(ctx, p, r, reason)
	}
	if aerr := s.maySettle(ctx, p, r); aerr != nil {
		return nil, aerr
	}
	return s.settle(ctx, p, r, Settlement{Status: StatusDeclined, Outcome: OutcomeDeclined, AnsweredBy: p.UserID, Answer: reason}, nil)
}

func (s *Service) declinePlan(ctx context.Context, p security.Principal, r *Request, reason string) (*Request, error) {
	if s.d.Plans == nil {
		return nil, errors.New("agentrequests: the planning service isn't configured")
	}
	_, err := s.d.Plans.Reject(ctx, p, r.Target.PlanID, planning.DecideInput{Note: reason})
	decided := errors.Is(err, planning.ErrNotReady) || errors.Is(err, planning.ErrSuperseded)
	if err != nil && !decided {
		return nil, err
	}
	// The plan has left ready, by this rejection or before it: settle the
	// checkpoint now rather than at the next sweep.
	if serr := s.settleApproval(ctx, r); serr != nil {
		s.warn("agentrequests: a declined plan's checkpoint will be settled by the sweep", serr)
	}
	if decided {
		return nil, ErrNotPending
	}
	return s.d.Store.Get(ctx, r.OrgID, r.ID)
}

// settle moves r to st as p, runs act (which sets a secret) when there is
// one, and resumes the run once every request of its pause is settled.
// If acting or resuming fails, the request is pending again and the error
// returned: the first one to settle a request is the one that counts, and
// a settle that didn't happen doesn't block the next person.
func (s *Service) settle(ctx context.Context, p security.Principal, r *Request, st Settlement, act func(context.Context) error) (*Request, error) {
	st.At = s.now().UTC()
	moved, err := s.d.Store.Settle(ctx, r.OrgID, r.ID, st)
	if err != nil {
		return nil, err
	}
	if !moved {
		return nil, ErrNotPending
	}
	reopen := func(cause error) error {
		if _, rerr := s.d.Store.Reopen(context.WithoutCancel(ctx), r.OrgID, r.ID, st); rerr != nil {
			return errors.Join(cause, fmt.Errorf("open the request again: %w", rerr))
		}
		return cause
	}
	if act != nil {
		if err = act(ctx); err != nil {
			return nil, reopen(err)
		}
	}
	if err = s.resume(ctx, r, true); err != nil {
		return nil, reopen(err)
	}
	s.audit(ctx, p, r, st)
	return s.d.Store.Get(ctx, r.OrgID, r.ID)
}

// resume hands the run the results of the pause r belongs to, once every
// request of it is settled. A run that has already moved on (resumed by
// whoever settled the last request at the same moment, or cancelled) is
// fine. strict says a task that can't be bound is an error; the sweep
// passes false, since a task that can't be bound any more never resumes.
func (s *Service) resume(ctx context.Context, r *Request, strict bool) error {
	pause, err := s.d.Store.Pause(ctx, r.OrgID, r.RunID, r.SuspensionID)
	if err != nil {
		return err
	}
	for _, other := range pause {
		if other.Status == StatusPending || other.Kind == KindApprovePlan {
			return nil
		}
	}
	task, err := s.d.Runs.ByRun(ctx, r.OrgID, r.RunID)
	if err != nil {
		if !strict {
			return nil
		}
		return err
	}
	bound, err := s.d.Runs.Bind(ctx, task)
	if err != nil {
		if !strict {
			s.warn("agentrequests: a settled request's run can't be resumed", err)
			return nil
		}
		return err
	}
	runID, err := id.ParseAgentRunID(r.RunID)
	if err != nil {
		return err
	}
	results := make([]engine.ToolResult, 0, len(pause))
	for _, other := range pause {
		results = append(results, s.result(bound, other))
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	err = eng.ProvideResults(bound, runID, engine.ResumeInput{ToolResults: results})
	if movedOn(err) {
		return nil
	}
	return err
}

// movedOn reports whether err says the run no longer waits on this pause.
func movedOn(err error) bool {
	return err == nil || errors.Is(err, cortex.ErrNotSuspended) || errors.Is(err, cortex.ErrInvalidState) ||
		errors.Is(err, cortex.ErrRunNotFound) || errors.Is(err, cortex.ErrRunNotContinuable)
}

// result is what the model reads of a settled request. Anything a person
// typed, or a name, goes through the untrusted scan; a secret's value is
// never here, only that it's set.
func (s *Service) result(bound context.Context, r *Request) engine.ToolResult {
	out := engine.ToolResult{ToolCallID: r.ToolCallID}
	scan := func(text string) string { return s.untrusted(bound, text) }
	name := s.name(bound, r.AnsweredBy)
	switch r.Status {
	case StatusAnswered:
		switch r.Kind {
		case KindAnswer:
			out.Content = scan(name + " answered: " + r.Answer)
		case KindProvideSecret:
			where := "every environment"
			if r.Target.Environment != "" {
				where = r.Target.Environment
			}
			out.Content = scan(fmt.Sprintf("%s set %s (secret) for %s; the value is not shown to you", name, r.Target.Key, where))
		case KindConnectGitHub:
			out.Content = scan("GitHub is connected: " + r.Answer)
		case KindCreateConnection:
			out.Content = scan(fmt.Sprintf("%s connected %s as %s", name, r.Target.Cloud, r.Answer))
		}
	case StatusDeclined:
		text := name + " declined"
		if r.Answer != "" {
			text += ": " + r.Answer
		}
		out.Error = scan(text)
	case StatusExpired:
		out.Error = "nobody answered in time"
	default:
		out.Error = "the request was cancelled"
		if r.Answer != "" {
			out.Error = r.Answer
		}
	}
	return out
}

func (s *Service) untrusted(bound context.Context, text string) string {
	if s.d.Safety == nil {
		return ""
	}
	return s.d.Safety.Untrusted(bound, "person_answer", text)
}

func (s *Service) name(ctx context.Context, userID string) string {
	switch {
	case userID == "":
		return "someone"
	case strings.HasPrefix(userID, "loomplane:"):
		return "Loomplane automation"
	}
	if _, agent := security.IsAgentSubject(userID); agent {
		return "the agent"
	}
	if s.d.People == nil {
		return "someone"
	}
	return s.d.People.Name(ctx, userID)
}

// audit records a settled request: who, what, and how it ended. Never an
// answer's text or a secret's value.
func (s *Service) audit(ctx context.Context, p security.Principal, r *Request, st Settlement) {
	if s.d.Audit == nil {
		return
	}
	attrs := map[string]any{
		"request_id": r.ID, "kind": string(r.Kind), "status": st.Status, "outcome": st.Outcome,
		"task_id": r.TaskID, "run_id": r.RunID, "team_id": r.TeamID, "answered_by": st.AnsweredBy,
	}
	switch r.Kind {
	case KindProvideSecret:
		attrs["key"], attrs["environment"] = r.Target.Key, r.Target.Environment
	case KindApprovePlan:
		attrs["plan_id"] = r.Target.PlanID
	}
	s.d.Audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: "agent.request.settle", Severity: "info", ProjectID: r.ProjectID, Principal: p, Attributes: attrs,
	})
}

// OnFinished cancels the task's pending requests: its run has ended, so
// nothing is waiting on them. The run is never resumed.
func (s *Service) OnFinished(ctx context.Context, task *agentruns.Task) error {
	if task == nil {
		return nil
	}
	cancelled, err := s.d.Store.CancelTask(ctx, task.OrgID, task.ID, s.now().UTC())
	if err != nil {
		return err
	}
	for _, r := range cancelled {
		s.audit(ctx, security.SystemPrincipal(task.OrgID), r, Settlement{Status: StatusCancelled, Outcome: OutcomeCancelled})
	}
	return nil
}
