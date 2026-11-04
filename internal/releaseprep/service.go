package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// PrepareInput is what a person asks to release.
type PrepareInput struct {
	From    string   `json:"from,omitempty"`    // environment name; default the project's only staging-kind environment
	To      string   `json:"to,omitempty"`      // default the project's only production-kind environment
	Scope   []string `json:"scope,omitempty"`   // source aliases; default every source
	Replace bool     `json:"replace,omitempty"` // supersede an open production plan someone else made
}

// Runs is *agentruns.Service.
type Runs interface {
	Start(ctx context.Context, in agentruns.StartInput) (*agentruns.Task, error)
	// ByRun reads a task with no principal, so it works after the
	// workspace turns its agent off.
	ByRun(ctx context.Context, orgID, runID string) (*agentruns.Task, error)
	// Cancel cancels a task as the person who asked for it.
	Cancel(ctx context.Context, p security.Principal, taskID string) error
	// CancelTask cancels a task for someone who may approve the release
	// but didn't ask for it.
	CancelTask(ctx context.Context, orgID, id string) error
}

// Requests is *agentrequests.Service.
type Requests interface {
	List(ctx context.Context, p security.Principal, f agentrequests.ListFilter) (*agentrequests.RequestList, error)
}

// Teams is *teams.Service: who hears about a project.
type Teams interface {
	Resolve(ctx context.Context, q notifications.Querier, orgID, projectID, named, reason string) (teams.Route, error)
}

// PlanStore is *planning.PgStore: a plan read with no principal, which
// still works after the workspace turns its agent off and the agent loses
// its roles.
type PlanStore interface {
	GetPlan(ctx context.Context, orgID, id string) (*planning.Plan, error)
}

// Places is *projects.PgStore: whether a report's project and
// environments still exist, read with no principal. Nothing ties a report
// to them in the database, so Settle closes a report that outlived one.
type Places interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*projects.Environment, error)
}

// ReleasePush builds the push for a release notice. Push notifications'
// agent mapper is the real one; without it notices go without a push.
type ReleasePush interface {
	ReleasePush(ctx context.Context, r *ReleaseReport, state string) (*notifications.Push, error)
}

// Deps are what a Service needs.
type Deps struct {
	Store    Store
	Sources  Sources
	Runs     Runs
	Requests Requests
	Jobs     func() *dispatchengine.Engine
	Safety   *agentsafety.Scanner
	// Binder binds the outbound scans of concerns and notices to the
	// workspace.
	Binder    *agentscope.Binder
	Teams     Teams
	PlanStore PlanStore
	Places    Places
	// Push is optional.
	Push  ReleasePush
	Audit projects.Auditor
	// Notify queues a notice; nil is notifications.Enqueue.
	Notify func(ctx context.Context, q notifications.Execer, n notifications.Notice) error
	Now    func() time.Time
	Warn   func(msg string, err error)
}

// Errors of the service.
var (
	// ErrOpenPlan is wrapped with the plan's ID and who made it.
	ErrOpenPlan     = errors.New("production has a plan waiting for a decision")
	ErrNoStaging    = errors.New("this project has no staging environment to promote from")
	ErrNoProduction = errors.New("this project has no production environment to promote to")
	ErrAmbiguous    = errors.New("this project has more than one environment of that kind; name it")
	ErrInvalid      = errors.New("invalid release preparation")
	ErrNotYourTask  = errors.New("only the release preparation that built this report can add concerns to it")
	// ErrNotCancelable is a report that has already finished.
	ErrNotCancelable = errors.New("the release preparation has already finished")
)

// Bounds of a request.
const (
	serviceListLimit = 20
	serviceMaxList   = 50
	maxScope         = 100
	maxAlias         = 200
	// requestLookup is how many of a project's requests a read searches
	// for the report's approve_plan request.
	requestLookup = 100
)

// withheldConcern stands in for a concern the outbound scan didn't let
// through. The concern keeps its kind, so an ask still asks.
const withheldConcern = "The agent raised a concern Loomplane withheld from display."

// Service prepares releases on a person's request, reads and cancels
// them, takes the agent's concerns, and settles what became of them.
type Service struct {
	d         Deps
	collector *Collector
	gate      *Gate
	// planPoll and planWait are how often and how long the prepare job
	// reads a plan that's still planning.
	planPoll, planWait time.Duration
	// planPollFor and planRequeue bound one run of the job's wait.
	planPollFor, planRequeue time.Duration
}

// NewService returns a Service. Its Gate and the Service read through one
// Collector.
func NewService(d Deps) *Service {
	c := &Collector{Sources: d.Sources, Now: d.Now}
	return &Service{
		d: d, collector: c, gate: &Gate{Store: d.Store, Collector: c, Audit: d.Audit, Now: d.Now},
		planPoll: planPoll, planWait: PlanWait, planPollFor: planPollFor, planRequeue: planRequeue,
	}
}

// SetSources replaces what the Service and its Gate read. The extension
// sets the real sources and integration tests swap fakes in, before
// anything is served.
func (s *Service) SetSources(src Sources) { s.collector.Sources = src }

// Collector is the evidence collector, shared with the Gate.
func (s *Service) Collector() *Collector { return s.collector }

// Gate is the promotion gate to install in 3a's authorizer.
func (s *Service) Gate() *Gate { return s.gate }

func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) warn(msg string, err error) {
	if s.d.Warn != nil {
		s.d.Warn(msg, err)
	}
}

func (s *Service) sources() Sources { return s.collector.Sources }

// aPerson is a person acting for themselves: not the agent, not
// Loomplane's automation, not the agent acting for someone.
func aPerson(p security.Principal) bool {
	return p.Kind == security.KindUser && p.OnBehalfOf == nil && p.OrgID != "" && person(p.UserID)
}

// require checks perm on the project. Every check names the project.
func (s *Service) require(ctx context.Context, p security.Principal, perm security.Permission, projectID string) error {
	authz := s.sources().Authz
	if authz == nil {
		return errors.New("releaseprep: no authorizer is configured")
	}
	if projectID == "" {
		return fmt.Errorf("%w: no project", ErrInvalid)
	}
	return authz.Require(ctx, p, perm, projectID)
}

// Prepare starts a release preparation for production, from staging. Only
// a person may ask, so nothing automatic ever starts one. It refuses while
// another preparation of the same environment is under way, and when a
// plan for production waits for a decision that this preparation can't
// reuse, unless the person says replace. The prepare job does the rest.
func (s *Service) Prepare(ctx context.Context, p security.Principal, projectID string, in PrepareInput, via string) (*ReleaseReport, error) {
	if err := s.mayPrepare(ctx, p, projectID, via); err != nil {
		return nil, err
	}
	t, err := s.target(ctx, p, projectID, in)
	if err != nil {
		return nil, err
	}
	r := &ReleaseReport{
		OrgID: p.OrgID, ProjectID: projectID, FromEnvironmentID: t.from.ID, FromEnvironment: t.from.Name,
		ToEnvironmentID: t.to.ID, ToEnvironment: t.to.Name, Scope: t.scope, Status: StatusPlanning, State: StatePlanning,
		RequestedBy: p.UserID, Via: via, PlanID: t.planID, ReusedPlan: t.reused,
	}
	if cerr := s.d.Store.Create(ctx, r); cerr != nil {
		return nil, cerr
	}
	if qerr := s.enqueue(ctx, JobPayload{OrgID: r.OrgID, ReportID: r.ID, Supersedes: t.supersedes}); qerr != nil {
		// Nothing will prepare it: settle it, so the environment is free.
		if serr := s.d.Store.Settle(context.WithoutCancel(ctx), r.OrgID, r.ID, StatePlanning, StatusFailed, StateFailed,
			"Loomplane couldn't start preparing the release; try again", "the preparation job couldn't be queued", s.now()); serr != nil {
			s.warn("releaseprep: couldn't settle a report whose job wasn't queued", serr)
		}
		return nil, fmt.Errorf("queue the release preparation: %w", qerr)
	}
	s.audit(ctx, "release.prepare", "info", p, projectID, map[string]any{
		"report_id": r.ID, "from": t.from.Name, "to": t.to.Name, "scope": slices.Clone(t.scope), "replace": in.Replace, "via": via,
		"supersedes": t.supersedes,
	})
	return r, nil
}

// mayPrepare checks that p is a person who may use the agent and create
// deployments on the project, through a known channel, while the
// workspace's agent is on.
func (s *Service) mayPrepare(ctx context.Context, p security.Principal, projectID, via string) error {
	if !aPerson(p) {
		return fmt.Errorf("%w: only a person can ask the agent to prepare a release", security.ErrForbidden)
	}
	switch via {
	case ViaDashboard, ViaCLI, ViaChat:
	default:
		return fmt.Errorf("%w: unknown via %q", ErrInvalid, via)
	}
	src := s.sources()
	if src.Budgets == nil || src.Projects == nil || src.Plans == nil {
		return errors.New("releaseprep: release preparation isn't wired on this server")
	}
	if err := s.require(ctx, p, security.AgentUse, projectID); err != nil {
		return err
	}
	if err := s.require(ctx, p, security.DeploymentCreate, projectID); err != nil {
		return err
	}
	on, err := src.Budgets.Enabled(ctx, p.OrgID)
	if err != nil {
		return err
	}
	if !on {
		return agentsettings.ErrDisabled
	}
	return nil
}

// target is what a preparation releases: from and to, the scope, and the
// open plan it reuses, if any.
type target struct {
	from, to *projects.Environment
	scope    []string
	planID   string
	reused   bool
	// supersedes is the open plan the person agreed to replace.
	supersedes string
}

// target resolves the environments and the scope, makes room for a new
// report of production, and finds the open plan to reuse.
func (s *Service) target(ctx context.Context, p security.Principal, projectID string, in PrepareInput) (*target, error) {
	scope, err := normalizeScope(in.Scope)
	if err != nil {
		return nil, err
	}
	envs, err := s.sources().Projects.Environments(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	t := &target{scope: scope}
	if t.from, err = pick(envs, in.From, projects.KindStaging, ErrNoStaging); err != nil {
		return nil, err
	}
	if t.to, err = pick(envs, in.To, projects.KindProduction, ErrNoProduction); err != nil {
		return nil, err
	}
	if aerr := s.clearActive(ctx, p.OrgID, t.to.ID); aerr != nil {
		return nil, aerr
	}
	if t.planID, t.reused, t.supersedes, err = s.openPlan(ctx, p, projectID, t.from, t.to, scope, in.Replace); err != nil {
		return nil, err
	}
	return t, nil
}

// normalizeScope trims, dedupes and sorts the aliases; nil or empty is
// every source.
func normalizeScope(in []string) ([]string, error) {
	if len(in) > maxScope {
		return nil, fmt.Errorf("%w: name at most %d sources", ErrInvalid, maxScope)
	}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || utf8.RuneCountInString(a) > maxAlias {
			return nil, fmt.Errorf("%w: a source alias is 1 to %d characters", ErrInvalid, maxAlias)
		}
		out = append(out, a)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// pick resolves an environment of kind by name, or the project's only one
// of that kind.
func pick(envs []*projects.Environment, name, kind string, none error) (*projects.Environment, error) {
	if name = strings.TrimSpace(name); name != "" {
		for _, e := range envs {
			if e == nil || e.Name != name {
				continue
			}
			if e.Kind != kind {
				return nil, fmt.Errorf("%w: %s is a %s environment, and a release promotes from a staging environment to a production one",
					ErrInvalid, e.Name, orDefault(e.Kind, "untyped"))
			}
			return e, nil
		}
		return nil, fmt.Errorf("%w: this project has no environment named %q", ErrInvalid, name)
	}
	var found []*projects.Environment
	for _, e := range envs {
		if e != nil && e.Kind == kind {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return nil, none
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, e := range found {
		names[i] = e.Name
	}
	return nil, fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(names, ", "))
}

// clearActive makes room for a new report of the environment: a report
// still under way is ErrInProgress, and one that has in fact finished is
// settled first.
func (s *Service) clearActive(ctx context.Context, orgID, environmentID string) error {
	a, err := s.d.Store.Active(ctx, orgID, environmentID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if serr := s.settle(ctx, a, newSweep()); serr != nil {
		return serr
	}
	again, err := s.d.Store.Active(ctx, orgID, environmentID)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	}
	return fmt.Errorf("%w: %s", ErrInProgress, again.ID)
}

// openPlan finds production's open plan. A promotion from the same
// staging environment that still matches what staging runs, of the same
// scope, is reused. Any other open plan is someone's to decide, and is
// ErrOpenPlan unless the person asked to replace it; then supersedes names
// it, so the job replaces that plan and no other.
func (s *Service) openPlan(ctx context.Context, p security.Principal, projectID string, from, to *projects.Environment, scope []string, replace bool) (planID string, reused bool, supersedes string, err error) {
	src := s.sources()
	plans, _, err := src.Plans.List(ctx, p, projectID, to.Name, page.Cursor{}, 1)
	if err != nil {
		return "", false, "", err
	}
	if len(plans) == 0 || plans[0] == nil || !plans[0].Open() {
		return "", false, "", nil
	}
	plan := plans[0]
	reuse, err := s.reusable(ctx, p.OrgID, plan, from, scope)
	if err != nil {
		return "", false, "", err
	}
	if reuse {
		return plan.ID, true, "", nil
	}
	left, err := s.leftover(ctx, p.OrgID, plan)
	if err != nil {
		return "", false, "", err
	}
	if replace || left {
		return "", false, plan.ID, nil
	}
	return "", false, "", openPlanError(plan)
}

// leftover reports whether an open plan is the agent's own, left by a
// release preparation that has ended: made by the agent, and named by a
// report that's settled. The next preparation supersedes it. A plan the
// agent made in someone's chat, or anyone else's, is never a leftover.
func (s *Service) leftover(ctx context.Context, orgID string, plan *planning.Plan) (bool, error) {
	if plan.CreatedBy != security.AgentSubject(orgID) {
		return false, nil
	}
	r, err := s.d.Store.ForPlan(ctx, orgID, plan.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return r.Status != StatusPlanning && r.Status != StatusReviewing, nil
}

// reusable reports whether production's open plan is the promotion a
// preparation would make: from the same staging environment, of the same
// scope, still matching what staging runs.
func (s *Service) reusable(ctx context.Context, orgID string, plan *planning.Plan, from *projects.Environment, scope []string) (bool, error) {
	if plan.Kind != planning.KindPromotion || plan.FromEnvironmentID != from.ID || !sameScope(plan, scope) {
		return false, nil
	}
	match, _, err := s.sources().Plans.PinsMatchSource(ctx, orgID, plan)
	if err != nil {
		return false, fmt.Errorf("check production's open plan against staging: %w", err)
	}
	return match, nil
}

// openPlanError is ErrOpenPlan for plan, naming it and who made it.
func openPlanError(plan *planning.Plan) error {
	return fmt.Errorf("%w: plan %s, made by %s; decide it, or prepare again with replace to supersede it",
		ErrOpenPlan, plan.ID, actorName(plan.CreatedBy))
}

// sameScope reports whether the plan moves exactly the sources asked
// for: every one of its sources when scope is empty.
func sameScope(plan *planning.Plan, scope []string) bool {
	var in []string
	for _, ps := range plan.Sources {
		if ps.InScope {
			in = append(in, ps.Alias)
		}
	}
	slices.Sort(in)
	if len(scope) == 0 {
		return len(in) > 0 && len(in) == len(plan.Sources)
	}
	return slices.Equal(in, scope)
}

// enqueue queues the prepare job; opts add to JobOptions.
func (s *Service) enqueue(ctx context.Context, payload JobPayload, opts ...job.Option) error {
	var eng *dispatchengine.Engine
	if s.d.Jobs != nil {
		eng = s.d.Jobs()
	}
	if eng == nil {
		return errors.New("releaseprep: the job engine isn't ready")
	}
	_, err := dispatchengine.Enqueue(ctx, eng, JobPrepare, payload, append(JobOptions(), opts...)...)
	return err
}

// Get returns a report to someone who may read the project's
// deployments, with where it stands now.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*ReleaseReport, error) {
	r, err := s.d.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if rerr := s.require(ctx, p, security.DeploymentRead, r.ProjectID); rerr != nil {
		return nil, rerr
	}
	s.fill(ctx, p, r, s.requests(ctx, p, r.ProjectID), newSweep())
	return r, nil
}

// List returns a project's newest reports: limit 1 to 50, 0 for 20.
func (s *Service) List(ctx context.Context, p security.Principal, projectID string, limit int) ([]*ReleaseReport, error) {
	switch {
	case limit == 0:
		limit = serviceListLimit
	case limit < 1 || limit > serviceMaxList:
		return nil, fmt.Errorf("%w: list 1 to %d reports", ErrInvalid, serviceMaxList)
	}
	if err := s.require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	reports, err := s.d.Store.List(ctx, p.OrgID, projectID, limit)
	if err != nil {
		return nil, err
	}
	reqs := s.requests(ctx, p, projectID)
	sw := newSweep()
	for _, r := range reports {
		s.fill(ctx, p, r, reqs, sw)
	}
	return reports, nil
}

// Lookup reads a report with no principal, for push notifications.
func (s *Service) Lookup(ctx context.Context, orgID, id string) (*ReleaseReport, error) {
	return s.d.Store.Get(ctx, orgID, id)
}

// requests are the project's requests, for a person; the agent's own
// reads never need them and may not list them.
func (s *Service) requests(ctx context.Context, p security.Principal, projectID string) []*agentrequests.Request {
	if s.d.Requests == nil || !aPerson(p) {
		return nil
	}
	list, err := s.d.Requests.List(ctx, p, agentrequests.ListFilter{Status: "all", ProjectID: projectID, Limit: requestLookup})
	if err != nil {
		if !errors.Is(err, security.ErrForbidden) {
			s.warn("releaseprep: couldn't read the project's agent requests", err)
		}
		return nil
	}
	return list.Requests
}

// fill sets the read-time fields: who decided the plan, its approve_plan
// request, and, while the report is unsettled, its state from the plan
// and the task. A settled report keeps what Settle stored, which its
// events and notices said. Any read that fails keeps the stored state.
func (s *Service) fill(ctx context.Context, p security.Principal, r *ReleaseReport, reqs []*agentrequests.Request, sw *sweep) {
	r.RequestID = matchRequest(reqs, r)
	var plan *planning.Plan
	planRead := r.PlanID == ""
	if r.PlanID != "" && s.sources().Plans != nil {
		got, err := s.sources().Plans.Get(ctx, p, r.PlanID)
		switch {
		case err == nil:
			plan, planRead, r.DecidedBy = got, true, got.DecidedBy
		case errors.Is(err, planning.ErrNotFound):
			planRead = true
		default:
			s.warn("releaseprep: couldn't read release report "+r.ID+"'s plan", err)
		}
	}
	if !open(r) || !planRead {
		return
	}
	task, err := s.task(ctx, r, sw)
	if err != nil {
		s.warn("releaseprep: couldn't read release report "+r.ID+"'s task", err)
		return
	}
	r.State, r.StateReason = DeriveState(r, plan, task)
}

// open is a report Settle still has to look at.
func open(r *ReleaseReport) bool {
	return r.Status == StatusPlanning || r.Status == StatusReviewing || !Terminal(r.State)
}

// matchRequest is the newest approve_plan request of the report's run for
// its plan.
func matchRequest(reqs []*agentrequests.Request, r *ReleaseReport) string {
	if r.PlanID == "" || r.TaskID == "" {
		return ""
	}
	var best *agentrequests.Request
	for _, q := range reqs {
		if q == nil || q.Kind != agentrequests.KindApprovePlan || q.Target.PlanID != r.PlanID || q.TaskID != r.TaskID {
			continue
		}
		if best == nil || q.CreatedAt.After(best.CreatedAt) {
			best = q
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

// task reads the report's task by its run, with no principal. A task
// cancelled with no reason of its own while the workspace's agent is off
// was cancelled by turning it off, and gets that reason.
func (s *Service) task(ctx context.Context, r *ReleaseReport, sw *sweep) (*agentruns.Task, error) {
	if r.RunID == "" || s.d.Runs == nil {
		return nil, nil
	}
	t, err := s.d.Runs.ByRun(ctx, r.OrgID, r.RunID)
	if errors.Is(err, agentruns.ErrNotFound) {
		// The run's task is gone: it ended, with nothing to say why.
		return &agentruns.Task{RunID: r.RunID, Status: agentruns.StatusFailed, ReleasePlanID: r.PlanID}, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Status == agentruns.StatusCancelled && t.Error == "" && !sw.agentOn(ctx, s, r.OrgID) {
		t.Error = agentsettings.ErrDisabled.Error()
	}
	return t, nil
}

// Cancel stops a preparation that is still under way: someone who may
// approve the project's deployments, or the person who asked for it while
// they may use the agent on the project. A read-only key may do neither.
// What already happened stands: Cancel derives the report's state first,
// and a final one (the agent promoted it, a person decided it, the run
// stopped) is settled as it is, with its notice, and refused with
// ErrNotCancelable. The plan is left as it is, for a person to decide.
func (s *Service) Cancel(ctx context.Context, p security.Principal, id string) (*ReleaseReport, error) {
	if !aPerson(p) {
		return nil, fmt.Errorf("%w: only a person can cancel a release preparation", security.ErrForbidden)
	}
	r, err := s.d.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	requester, err := s.mayCancel(ctx, p, r)
	if err != nil {
		return nil, err
	}
	// The job may set the task between the read and the write, which moves
	// the state: read again once and cancel what's there then.
	for try := 0; ; try++ {
		if ferr := s.finalOutcome(ctx, r); ferr != nil {
			return nil, ferr
		}
		err = s.d.Store.Settle(ctx, r.OrgID, r.ID, r.State, StatusCancelled, StateCancelled, "cancelled by "+p.UserID, "", s.now())
		if !errors.Is(err, ErrMovedOn) || try == 1 {
			break
		}
		if r, err = s.d.Store.Get(ctx, p.OrgID, id); err != nil {
			return nil, err
		}
	}
	if errors.Is(err, ErrMovedOn) {
		return nil, ErrNotCancelable
	}
	if err != nil {
		return nil, err
	}
	if r.TaskID != "" && s.d.Runs != nil {
		var cerr error
		if requester {
			cerr = s.d.Runs.Cancel(ctx, p, r.TaskID)
		} else {
			cerr = s.d.Runs.CancelTask(ctx, r.OrgID, r.TaskID)
		}
		// The report is cancelled whatever the run does: the gate refuses
		// a report that isn't reviewing.
		if cerr != nil && !errors.Is(cerr, agentruns.ErrNotCancelable) {
			s.warn("releaseprep: couldn't cancel release report "+r.ID+"'s task", cerr)
		}
	}
	s.audit(ctx, "release.cancel", "info", p, r.ProjectID, map[string]any{"report_id": r.ID, "task_id": r.TaskID, "requester": requester})
	return s.Get(ctx, p, id)
}

// mayCancel checks p may cancel r: deployment:approve on the project, or
// being its requester with agent:use there. requester says which applied.
func (s *Service) mayCancel(ctx context.Context, p security.Principal, r *ReleaseReport) (requester bool, err error) {
	if rerr := s.require(ctx, p, security.DeploymentRead, r.ProjectID); rerr != nil {
		return false, rerr
	}
	err = s.require(ctx, p, security.DeploymentApprove, r.ProjectID)
	if err == nil {
		return p.UserID == r.RequestedBy, nil
	}
	if !errors.Is(err, security.ErrForbidden) || p.UserID != r.RequestedBy {
		return false, err
	}
	if uerr := s.require(ctx, p, security.AgentUse, r.ProjectID); uerr != nil {
		return false, uerr
	}
	return true, nil
}

// finalOutcome refuses to cancel a report that isn't under way, or whose
// derived state is already final; the latter is settled as it is first.
func (s *Service) finalOutcome(ctx context.Context, r *ReleaseReport) error {
	if r.Status != StatusPlanning && r.Status != StatusReviewing {
		return ErrNotCancelable
	}
	sw := newSweep()
	o, err := s.derive(ctx, r, sw)
	if err != nil {
		return err
	}
	if !Terminal(o.state) {
		return nil
	}
	if ferr := s.finish(ctx, r, o, sw); ferr != nil {
		return ferr
	}
	return fmt.Errorf("%w: it ended %s", ErrNotCancelable, o.state)
}

// Note adds the agent's concern to the report its run is reviewing.
// taskID is the calling run's task, which Loomplane knows, never the
// model. The text is outbound-scanned before it's stored, since people
// read it; a text the scan won't pass is withheld, keeping its kind.
func (s *Service) Note(ctx context.Context, p security.Principal, taskID, reportID string, c Concern) (*ReleaseReport, error) {
	r, err := s.d.Store.Get(ctx, p.OrgID, reportID)
	if err != nil {
		return nil, err
	}
	if taskID == "" || r.TaskID != taskID {
		return nil, ErrNotYourTask
	}
	if r.Status != StatusReviewing {
		return nil, fmt.Errorf("%w: the release preparation has finished", ErrInvalid)
	}
	if c.Kind != ConcernRisk && c.Kind != ConcernAsk {
		return nil, fmt.Errorf("%w: a concern is a risk or an ask", ErrInvalid)
	}
	text := strings.TrimSpace(c.Text)
	if n := utf8.RuneCountInString(text); n == 0 || n > MaxConcernLength {
		return nil, fmt.Errorf("%w: a concern is 1 to %d characters", ErrInvalid, MaxConcernLength)
	}
	concern := Concern{Kind: c.Kind, Text: s.concernText(ctx, r, text), RunID: r.RunID, At: s.now()}
	if aerr := s.d.Store.AddConcern(ctx, r.OrgID, r.ID, concern); aerr != nil {
		return nil, aerr
	}
	s.audit(ctx, "release.concern", "info", p, r.ProjectID, map[string]any{"report_id": r.ID, "kind": c.Kind})
	return s.d.Store.Get(ctx, r.OrgID, r.ID)
}

// concernText is text as people may read it: through the outbound scan,
// bound to the report's workspace, or withheld.
func (s *Service) concernText(ctx context.Context, r *ReleaseReport, text string) string {
	bound, ok := s.bind(ctx, r.OrgID, r.ProjectID)
	if !ok || s.d.Safety == nil {
		return withheldConcern
	}
	out, err := s.d.Safety.Outbound(bound, text)
	if err != nil || strings.TrimSpace(out) == "" {
		return withheldConcern
	}
	return strings.TrimSpace(out)
}

// bind is ctx bound to the workspace for the outbound scan: as it is when
// it's already bound there, such as inside the agent's run, else bound as
// the agent. It fails while the workspace's agent is off.
func (s *Service) bind(ctx context.Context, orgID, projectID string) (context.Context, bool) {
	if sc, ok := agentscope.From(ctx); ok && sc.OrgID == orgID {
		return ctx, true
	}
	if s.d.Binder == nil {
		return ctx, false
	}
	bound, err := s.d.Binder.Bind(ctx, security.AgentPrincipal(orgID, nil), agentscope.Scope{OrgID: orgID, ProjectID: projectID})
	if err != nil {
		return ctx, false
	}
	return bound, true
}

func (s *Service) audit(ctx context.Context, action, severity string, p security.Principal, projectID string, attrs map[string]any) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: action, Severity: severity, ProjectID: projectID, Principal: p, Attributes: attrs,
	})
}
