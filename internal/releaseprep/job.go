package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// planPoll is how often the prepare job reads a plan that's still
// planning.
const planPoll = 2 * time.Second

// metaReleaseReport is the review task's metadata key naming its report.
// It's the only metadata the job sets: the plan and the person are the
// task's typed fields.
const metaReleaseReport = "release_report_id"

// PrepareJob plans the promotion of one report, collects its evidence and
// starts the agent's review.
func PrepareJob(s *Service) *job.Definition[JobPayload] {
	return job.NewDefinition(JobPrepare, s.RunJob, JobOptions()...)
}

// SettleJob is the settle cron's job. It isn't retried: the next tick
// settles what this one couldn't.
func SettleJob(s *Service) *job.Definition[struct{}] {
	return job.NewDefinition(JobSettle, func(ctx context.Context, _ struct{}) error {
		return s.Settle(ctx)
	}, job.WithQueue(JobQueue), job.WithMaxRetries(0))
}

// The prepare job's reasons. Like every reason they travel in events and
// notices, so they're Loomplane's own words: never what planning, a cloud
// or the model said.
const (
	reasonNoRequester    = "the release preparation names no person who asked for it"
	reasonNotThisRelease = "the plan isn't the one this release preparation was for"
)

// requesterLeft is the reason for a requester who may no longer do what a
// preparation needs. It names them by user ID.
func requesterLeft(userID string) string {
	return userID + ", who asked for this release, may no longer prepare releases of this project"
}

// requesterCantUseAgent ends a review the requester may no longer have
// the agent run for them.
func requesterCantUseAgent(userID string) string {
	return userID + ", who asked for this release, may no longer use the agent on this project; " + waitsForAPerson
}

// cantPlan is the reason for a promotion planning refused.
func cantPlan(from, to string) string {
	return "Loomplane couldn't plan the promotion from " + from + " to " + to + "; plan it in Loomplane to see why"
}

// reviewStopped ends a review the agent couldn't start, the plan ready for
// a person. cause is one of agentsettings' sentences.
func reviewStopped(cause error) string {
	return "the agent couldn't review this release: " + cause.Error() + "; " + waitsForAPerson
}

// errSettled ends the job once it has settled the report, or found it
// settled: there's nothing to retry.
var errSettled = errors.New("releaseprep: the release report is settled")

// RunJob is the prepare job. Each stage is safe to repeat, so a retried job
// resumes where the report is: a report that isn't planning is left alone;
// one without a plan gets one, made as the agent acting for the person who
// asked; the job waits for the plan to leave planning; a plan that isn't
// ready settles the report with its state; a ready one has its evidence
// and a preview of the gates stored, and the agent's review starts for
// the person who asked.
//
// The review carries the plan and its revision as the person's request
// only while the person may approve production on the project, checked
// just before it starts; otherwise the agent reviews and the plan waits
// for someone who can approve it. Both come from the stored report, never
// from a payload or the model.
func (s *Service) RunJob(ctx context.Context, in JobPayload) error {
	r, err := s.d.Store.Get(ctx, in.OrgID, in.ReportID)
	if errors.Is(err, ErrNotFound) {
		s.warn("releaseprep: a prepare job names no report", fmt.Errorf("%w: %s/%s", err, in.OrgID, in.ReportID))
		return nil
	}
	if err != nil {
		return err
	}
	if r.Status != StatusPlanning {
		return nil
	}
	j := &prepare{s: s, r: r, supersedes: in.Supersedes, sw: newSweep()}
	err = j.run(ctx)
	if errors.Is(err, errSettled) {
		return nil
	}
	return err
}

// prepare is one run of the prepare job.
type prepare struct {
	s *Service
	r *ReleaseReport
	// supersedes is the open plan the person agreed to replace.
	supersedes string
	requester  security.Principal
	// agent is the agent acting for the requester: every read and the
	// plan go as it.
	agent    security.Principal
	from, to *projects.Environment
	sw       *sweep
}

func (j *prepare) run(ctx context.Context) error {
	s, r := j.s, j.r
	j.requester = security.ActorPrincipal(r.OrgID, r.RequestedBy)
	if !aPerson(j.requester) {
		return j.settle(ctx, StatusFailed, StateFailed, reasonNoRequester)
	}
	j.agent = security.AgentPrincipal(r.OrgID, &j.requester)
	src := s.sources()
	if src.Plans == nil || src.Projects == nil || src.Budgets == nil || s.d.Runs == nil {
		return errors.New("releaseprep: release preparation isn't wired on this server")
	}
	on, err := src.Budgets.Enabled(ctx, r.OrgID)
	if err != nil {
		return err
	}
	if !on {
		return j.agentOff(ctx, agentsettings.ErrDisabled)
	}
	if eerr := j.environments(ctx); eerr != nil {
		return eerr
	}
	if j.r.PlanID == "" {
		if perr := j.makePlan(ctx); perr != nil {
			return perr
		}
	}
	// Start refuses a revision that moved since the job read it: read the
	// plan again once and review what it is then.
	for attempt := 0; ; attempt++ {
		plan, err := j.waitPlan(ctx)
		if err != nil {
			return err
		}
		if plan.Status != planning.StatusReady {
			return j.notReady(ctx, plan)
		}
		moved, err := j.review(ctx, plan)
		if !moved || attempt == 1 {
			return err
		}
	}
}

// settle settles the report, with its notice, and ends the job. A report
// that moved on meanwhile, such as one a person cancelled, is left as it
// is.
func (j *prepare) settle(ctx context.Context, status, state, reason string) error {
	if err := j.s.finish(ctx, j.r, outcome{status, state, reason}, j.sw); err != nil {
		return err
	}
	return errSettled
}

// agentOff settles a report whose workspace can't run the agent: failed
// before there's a plan, and stopped with the plan left for a person once
// there is one.
func (j *prepare) agentOff(ctx context.Context, cause error) error {
	if j.r.PlanID == "" {
		return j.settle(ctx, StatusFailed, StateStopped, "the agent couldn't prepare this release: "+cause.Error())
	}
	return j.settle(ctx, StatusDone, StateStopped, reviewStopped(cause))
}

// refused is an error that means the person may not do it, or isn't a
// member any more.
func refused(err error) bool {
	return errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated)
}

// stoppedBy is the agentsettings sentence err is, or nil.
func stoppedBy(err error) error {
	for _, c := range causes {
		if errors.Is(err, c) {
			return c
		}
	}
	return nil
}

// environments reads the report's two environments as the agent for the
// requester.
func (j *prepare) environments(ctx context.Context) error {
	r := j.r
	envs, err := j.s.sources().Projects.Environments(ctx, j.agent, r.ProjectID)
	switch {
	case errors.Is(err, projects.ErrNotFound):
		return j.settle(ctx, StatusFailed, StateFailed, reasonNoProject)
	case refused(err):
		return j.settle(ctx, StatusFailed, StateFailed, requesterLeft(r.RequestedBy))
	case err != nil:
		return err
	}
	for _, e := range envs {
		switch {
		case e == nil:
		case e.ID == r.FromEnvironmentID:
			j.from = e
		case e.ID == r.ToEnvironmentID:
			j.to = e
		}
	}
	if j.from == nil || j.to == nil {
		return j.settle(ctx, StatusFailed, StateFailed, reasonNoEnvironment)
	}
	return nil
}

// makePlan makes the promotion and records it. It reads production's
// newest open plan first: a reusable promotion is reused, the plan the
// person agreed to replace is superseded, and any other open plan is a
// person's to decide, so the report fails with ErrOpenPlan's reason.
func (j *prepare) makePlan(ctx context.Context) error {
	s, r := j.s, j.r
	src := s.sources()
	open, _, err := src.Plans.List(ctx, j.agent, r.ProjectID, j.to.Name, page.Cursor{}, 1)
	switch {
	case refused(err):
		return j.settle(ctx, StatusFailed, StateFailed, requesterLeft(r.RequestedBy))
	case err != nil:
		return err
	}
	var plan *planning.Plan
	if len(open) > 0 && open[0] != nil && open[0].Open() {
		reuse, rerr := s.reusable(ctx, r.OrgID, open[0], j.from, r.Scope)
		switch {
		case rerr != nil:
			return rerr
		case reuse:
			plan = open[0]
		case j.supersedes == "" || open[0].ID != j.supersedes:
			return j.settle(ctx, StatusFailed, StateFailed, openPlanError(open[0]).Error())
		}
	}
	reused := plan != nil
	if !reused {
		plan, err = src.Plans.Create(ctx, j.agent, r.ProjectID, planning.CreatePlanInput{
			Environment: j.to.Name, From: j.from.Name, Scope: slices.Clone(r.Scope),
		})
		switch {
		case refused(err):
			return j.settle(ctx, StatusFailed, StateFailed, requesterLeft(r.RequestedBy))
		case errors.Is(err, planning.ErrInvalid), errors.Is(err, planning.ErrNoManifest), errors.Is(err, projects.ErrNotFound),
			errors.Is(err, projects.ErrEnvironmentNotFound), errors.Is(err, projects.ErrFleetManaged):
			return j.settle(ctx, StatusFailed, StateFailed, cantPlan(j.from.Name, j.to.Name))
		case err != nil:
			return err
		}
	}
	set, err := s.d.Store.SetPlan(ctx, r.OrgID, r.ID, plan.ID, reused)
	if err != nil {
		return err
	}
	if !set {
		// Cancelled, or an earlier try recorded its plan first: go on with
		// what's stored.
		again, gerr := s.d.Store.Get(ctx, r.OrgID, r.ID)
		if gerr != nil {
			return gerr
		}
		if again.Status != StatusPlanning || again.PlanID == "" {
			return errSettled
		}
		j.r = again
		return nil
	}
	j.r.PlanID, j.r.ReusedPlan = plan.ID, reused
	return nil
}

// waitPlan reads the report's plan until it leaves planning. Past
// planWait it gives up with an error, so Dispatch retries the job, which
// waits for the same plan.
func (j *prepare) waitPlan(ctx context.Context) (*planning.Plan, error) {
	s, r := j.s, j.r
	deadline := time.Now().Add(s.planWait)
	for {
		plan, err := s.sources().Plans.Get(ctx, j.agent, r.PlanID)
		switch {
		case errors.Is(err, planning.ErrNotFound):
			// A reused plan can go before the job reads it.
			return nil, j.settle(ctx, StatusFailed, StateFailed, reasonNoPlan)
		case refused(err):
			return nil, j.settle(ctx, StatusFailed, StateFailed, requesterLeft(r.RequestedBy))
		case err != nil:
			return nil, err
		}
		if plan.OrgID != r.OrgID || plan.ProjectID != r.ProjectID || plan.EnvironmentID != r.ToEnvironmentID {
			return nil, j.settle(ctx, StatusFailed, StateFailed, reasonNotThisRelease)
		}
		if plan.Status != planning.StatusPlanning {
			return plan, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("releaseprep: plan %s is still planning after %s", plan.ID, s.planWait)
		}
		timer := time.NewTimer(s.planPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// notReady stores the evidence of a plan the agent can't approve
// (blocked, failed, superseded, or one a person already decided), and
// settles the report with the plan's state. No review starts.
func (j *prepare) notReady(ctx context.Context, plan *planning.Plan) error {
	if err := j.storeEvidence(ctx, plan); err != nil {
		return err
	}
	probe := *j.r
	probe.Status = StatusReviewing
	state, reason := DeriveState(&probe, plan, nil)
	status := StatusDone
	if state == StateFailed {
		status = StatusFailed
	}
	return j.settle(ctx, status, state, reason)
}

// storeEvidence collects the evidence as the agent for the requester,
// previews the gates and stores both with the plan's revision.
func (j *prepare) storeEvidence(ctx context.Context, plan *planning.Plan) error {
	s, r := j.s, j.r
	ev := s.collector.Collect(ctx, CollectInput{Principal: j.agent, Plan: plan, From: j.from, To: j.to})
	project, err := s.sources().Projects.Get(ctx, j.agent, r.ProjectID)
	if err != nil {
		// Gate 1 previews as unread.
		project = nil
	}
	canApprove, why := s.gate.requester(ctx, s.sources().Authz, r)
	probe := *r
	probe.PlanRevision = plan.Revision
	gates := PreviewChecks(CheckInput{
		Project: project, RequesterCanApprove: canApprove, RequesterError: why, Report: &probe, Plan: plan,
		From: j.from, To: j.to, Evidence: ev, Now: s.now(),
	})
	err = s.d.Store.SetEvidence(ctx, r.OrgID, r.ID, plan.Revision, ev, gates)
	if errors.Is(err, ErrMovedOn) {
		return errSettled
	}
	return err
}

// review stores the evidence of a ready plan and starts the agent's
// review. moved says Start refused the plan's revision as changed, so the
// caller reads the plan again.
func (j *prepare) review(ctx context.Context, plan *planning.Plan) (moved bool, err error) {
	s := j.s
	if serr := j.storeEvidence(ctx, plan); serr != nil {
		return false, serr
	}
	// What the review starts from is what's stored.
	r, err := s.d.Store.Get(ctx, j.r.OrgID, j.r.ID)
	if err != nil {
		return false, err
	}
	if r.Status != StatusPlanning {
		return false, errSettled
	}
	if r.PlanID != plan.ID || r.PlanRevision != plan.Revision {
		return false, errors.New("releaseprep: the report's plan moved while the job prepared it")
	}
	j.r = r
	if s.d.Binder == nil {
		return false, errors.New("releaseprep: no scope binder is configured")
	}
	bound, err := s.d.Binder.Bind(ctx, j.agent, agentscope.Scope{OrgID: r.OrgID, ProjectID: r.ProjectID})
	if err != nil {
		if cause := stoppedBy(err); cause != nil {
			return false, j.settle(ctx, StatusDone, StateStopped, reviewStopped(cause))
		}
		return false, err
	}
	canApprove, err := j.mayApprove(ctx)
	if err != nil {
		return false, err
	}
	requester := j.requester
	in := agentruns.StartInput{
		Kind: agentruns.KindReleasePreparation, OrgID: r.OrgID, OnBehalfOf: &requester, ProjectID: r.ProjectID,
		EnvironmentID: r.ToEnvironmentID, Input: RenderInput(bound, s.d.Safety, r),
		Metadata: map[string]string{metaReleaseReport: r.ID},
	}
	if canApprove {
		in.ReleasePlanID, in.ReleaseRevision = r.PlanID, r.PlanRevision
	}
	task, err := s.d.Runs.Start(ctx, in)
	if err != nil {
		switch cause := stoppedBy(err); {
		case cause != nil:
			return false, j.settle(ctx, StatusDone, StateStopped, reviewStopped(cause))
		case errors.Is(err, agentruns.ErrInvalid):
			return true, err
		case refused(err):
			return false, j.settle(ctx, StatusDone, StateStopped, requesterCantUseAgent(r.RequestedBy))
		}
		return false, err
	}
	return false, j.record(ctx, task)
}

// mayApprove reports whether the requester holds deployment:approve on the
// project now, checked as them, never as the agent.
func (j *prepare) mayApprove(ctx context.Context) (bool, error) {
	authz := j.s.sources().Authz
	if authz == nil {
		return false, errors.New("releaseprep: no authorizer is configured")
	}
	switch err := authz.Require(ctx, j.requester, security.DeploymentApprove, j.r.ProjectID); {
	case err == nil:
		return true, nil
	case refused(err):
		return false, nil
	default:
		return false, err
	}
}

// record stores the review's task on the report. A report that moved on
// meanwhile, such as one a person cancelled before the task was there to
// cancel, gets its task cancelled here; so does one the write failed for,
// since the retry starts another.
func (j *prepare) record(ctx context.Context, task *agentruns.Task) error {
	set, err := j.s.d.Store.SetTask(ctx, j.r.OrgID, j.r.ID, task.ID, task.RunID)
	if err != nil {
		j.cancelTask(ctx, task)
		return err
	}
	if !set {
		j.cancelTask(ctx, task)
		return errSettled
	}
	return nil
}

func (j *prepare) cancelTask(ctx context.Context, task *agentruns.Task) {
	err := j.s.d.Runs.CancelTask(context.WithoutCancel(ctx), j.r.OrgID, task.ID)
	if err != nil && !errors.Is(err, agentruns.ErrNotCancelable) {
		j.s.warn("releaseprep: couldn't cancel a review whose report moved on", err)
	}
}
