package releasewatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

// dueLimit is how many watches one tick claims.
const dueLimit = 50

// tickBudget is how long one tick works on the watches it claimed: well
// inside the lease Due claims them for, so the next tick can't take one up
// while this one still works on it.
const tickBudget = claimAfter * 4 / 5

// maxSpan is the longest window a release's health is read over.
const maxSpan = 24 * time.Hour

// automationCantCreate is why a person has to roll a breached release
// back when Loomplane automation may not plan deployments.
const automationCantCreate = "Loomplane automation can't create deployments in this project; roll the release back yourself"

// neverStarted is how long a rollback may take to finish planning, from
// the watch's bound, or to start deploying, from its approval, before the
// watch gives up on it.
const neverStarted = time.Hour

// rollbackStale is why a person has to roll a breached release back when
// its rollback plan went stale for a reason other than a newer release.
const rollbackStale = "the rollback plan went stale before it could deploy; roll the release back yourself"

// createTimeout bounds planning a rollback, apart from the tick's budget.
const createTimeout = 30 * time.Second

// automationDenied is why a rollback waits for a person when Loomplane
// automation may not approve it.
const automationDenied = "Loomplane automation can't approve deployments in this organization; approve the rollback plan yourself"

// Tick moves every due watch on: it judges a watching one, and follows a
// rolling_back one's rollback plan and deployment. Dispatch's
// loomplane.release-watch cron job runs it every minute.
func (s *Service) Tick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, tickBudget)
	defer cancel()
	now := s.now().UTC()
	due, err := s.store.Due(ctx, now, dueLimit)
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range due {
		// What's left rests out its claim and the next tick takes it up.
		if ctx.Err() != nil {
			break
		}
		var tickErr error
		switch w.Status {
		case StatusWatching:
			tickErr = s.judge(ctx, w, now)
		case StatusRollingBack:
			tickErr = s.rollBack(ctx, w, now)
		}
		// Another tick saved it first; the next one sees what it did.
		if tickErr != nil && !errors.Is(tickErr, ErrConflict) {
			errs = append(errs, fmt.Errorf("release watch %s: %w", w.ID, tickErr))
		}
	}
	return errors.Join(errs...)
}

// judge reads each watched service's health and decides: a newer release
// supersedes the watch, any failing service breaches it at once, every
// service passing once the window is over passes it, and a watch still
// undecided at its bound settles inconclusive without rolling back.
func (s *Service) judge(ctx context.Context, w *Watch, now time.Time) error {
	newer, err := s.superseded(ctx, w)
	if err != nil {
		return err
	}
	if newer != "" {
		return s.supersede(ctx, w, now, newer)
	}
	// Metrics arrive late; the first minute after the release is judged
	// once it can be read.
	if now.Before(w.StartsAt.Add(releasehealth.MetricsLag + time.Minute)) {
		return nil
	}
	rec, err := s.history.Deployment(ctx, w.OrgID, w.DeploymentID)
	if err != nil {
		return fmt.Errorf("read the watched deployment: %w", err)
	}
	end := now
	if w.BoundAt.Before(end) {
		end = w.BoundAt
	}
	end = end.Add(-releasehealth.MetricsLag)
	allPass := true
	var breach *WatchedService
	for i := range w.Services {
		svc := &w.Services[i]
		verdict, reason := s.judgeService(ctx, w, svc, rec.StartedAt, end)
		at := now
		svc.Verdict, svc.Reason, svc.CheckedAt = string(verdict), reason, &at
		allPass = allPass && verdict == releasehealth.Pass
		if verdict == releasehealth.Fail && breach == nil {
			breach = svc
		}
	}
	switch {
	case breach != nil:
		return s.breach(ctx, w, now, breach)
	case allPass && !end.Before(w.EndsAt):
		return s.settle(ctx, w, now, StatusPassed, string(releasehealth.Pass), "Every watched service passed.")
	case !now.Before(w.BoundAt):
		w.Unverified = true
		return s.settle(ctx, w, now, StatusInconclusive, string(releasehealth.Inconclusive), "The watch couldn't decide by its bound: "+undecided(w))
	}
	w.UpdatedAt = now
	return s.store.Save(ctx, w)
}

// undecided lists the services that didn't pass, with why.
func undecided(w *Watch) string {
	parts := []string{}
	for _, svc := range w.Services {
		if svc.Verdict != string(releasehealth.Pass) {
			parts = append(parts, svc.Name+": "+svc.Reason)
		}
	}
	return strings.Join(parts, "; ")
}

// superseded says why a newer release took over from w's, or "". A
// breach would roll back to before w's release, undoing any release that
// followed it, whatever that one changed.
func (s *Service) superseded(ctx context.Context, w *Watch) (string, error) {
	latest, err := s.history.Latest(ctx, w.OrgID, w.EnvironmentID)
	switch {
	case err == nil && latest.DeploymentID != w.DeploymentID:
		return "A newer release, " + latest.DeploymentID + ", started before the watch finished, so this one isn't judged or rolled back.", nil
	case err != nil && !errors.Is(err, planning.ErrNotFound):
		return "", err
	}
	bindings, err := s.plans.ListBindings(ctx, w.OrgID, w.EnvironmentID)
	if err != nil {
		return "", err
	}
	for _, svc := range w.Services {
		b := binding(bindings, string(planning.ServiceSubject)+":"+svc.Name)
		if b == nil || b.ID != svc.BindingID || b.PlanID != w.PlanID {
			return "A newer release changed " + svc.Name + " before the watch finished, so this one isn't judged or rolled back.", nil
		}
	}
	return "", nil
}

// judgeService judges one service over the watch so far, up to end,
// against the same stretch before its deployment started. A service the
// release created or moved has no history of its own, and a baseline that
// can't be read says nothing, so then it's judged on the thresholds
// alone. A read that fails, or a window that can't be read, is
// inconclusive.
func (s *Service) judgeService(ctx context.Context, w *Watch, svc *WatchedService, depStart, end time.Time) (verdict releasehealth.Verdict, reason string) {
	start := w.StartsAt
	if end.Sub(start) > maxSpan {
		start = end.Add(-maxSpan)
	}
	cw, err := metrics.ShortWindow(start, end)
	if err != nil {
		return releasehealth.Inconclusive, err.Error()
	}
	candidate, err := s.health.Health(ctx, w.OrgID, svc.BindingID, "", releasehealth.Window{Start: cw.Start, End: cw.End})
	if err != nil {
		return releasehealth.Inconclusive, err.Error()
	}
	baseline := candidate
	if svc.Op == string(planning.Update) {
		bw, windowErr := metrics.ShortWindow(depStart.Add(-cw.End.Sub(cw.Start)), depStart)
		if windowErr != nil {
			return releasehealth.Inconclusive, windowErr.Error()
		}
		before, readErr := s.health.Health(ctx, w.OrgID, svc.BindingID, "", releasehealth.Window{Start: bw.Start, End: bw.End})
		if readErr != nil {
			return releasehealth.Inconclusive, readErr.Error()
		}
		if before.Available {
			baseline = before
		}
	}
	return releasehealth.Judge(baseline, candidate, w.Thresholds)
}

// breach records that svc failed, then plans the rollback.
func (s *Service) breach(ctx context.Context, w *Watch, now time.Time, svc *WatchedService) error {
	w.Status, w.Verdict, w.Reason, w.UpdatedAt = StatusRollingBack, string(releasehealth.Fail), svc.Name+": "+svc.Reason, now
	if err := s.store.Save(ctx, w); err != nil {
		return err
	}
	return s.createRollback(ctx, w, now)
}

// createRollback plans the rollback of w's release as Loomplane
// automation. A refusal settles the watch rollback_blocked; anything else
// is tried again on the next tick.
func (s *Service) createRollback(ctx context.Context, w *Watch, now time.Time) error {
	// A tick that planned it and lost the save, or overlapped this one,
	// left the plan: take it up rather than plan another.
	existing, err := s.store.RollbackPlan(ctx, w.OrgID, w.ID)
	if err != nil {
		return err
	}
	var plan *planning.Plan
	if existing != "" {
		plan, err = s.plans.GetPlan(ctx, w.OrgID, existing)
		if err != nil {
			return err
		}
		// A dead plan (its planning couldn't start, or it was superseded)
		// is planned afresh, as a transient failure always was.
		if !livePlan(plan) {
			plan = nil
		}
	}
	if plan == nil {
		// The create runs to its end whatever is left of the tick: cut
		// off after planning stored the plan, it would leave a dead one.
		createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
		defer cancel()
		plan, err = s.rollbacks.CreateAutomaticRollback(createCtx, w.OrgID, w.DeploymentID, planning.AutomaticRollbackInput{
			Trigger: planning.TriggerWatch, Reason: w.Reason, WatchID: w.ID,
		})
		// Audited as planned, whatever becomes of the save below, so the
		// breach is recorded once.
		if err == nil || refused(err) {
			s.auditBreach(ctx, w, plan, err)
		}
	}
	if err != nil {
		if !refused(err) {
			return fmt.Errorf("plan the rollback: %w", err)
		}
		if newer := s.startedSince(ctx, w, err); newer != "" {
			return s.supersede(ctx, w, now, newer)
		}
		reason := "Loomplane couldn't roll the release back: " + err.Error()
		if errors.Is(err, security.ErrForbidden) {
			reason = automationCantCreate
		}
		return s.settle(ctx, w, now, StatusRollbackBlocked, w.Verdict, reason)
	}
	w.RollbackPlanID, w.UpdatedAt = plan.ID, now
	return s.store.Save(ctx, w)
}

// livePlan reports whether a rollback plan can still go ahead: the tick
// that made it audited the breach, and this one takes it up.
func livePlan(p *planning.Plan) bool {
	return p.Status == planning.StatusPlanning || p.Status == planning.StatusReady || p.Status == planning.StatusApproved
}

// startedSince says why a newer release that started after the tick
// judged supersedes w, when planning's refusal says one did: a running
// deployment, or one that isn't the newest any more. It's "" for any
// other refusal.
func (s *Service) startedSince(ctx context.Context, w *Watch, refusal error) string {
	if !errors.Is(refusal, planning.ErrReleaseRunning) && !errors.Is(refusal, planning.ErrInvalid) {
		return ""
	}
	if newer, err := s.superseded(ctx, w); err == nil && newer != "" {
		return newer
	}
	if errors.Is(refusal, planning.ErrReleaseRunning) {
		return "A newer release started before the rollback could be planned, so this one isn't rolled back."
	}
	return ""
}

// supersede settles w superseded. Its release was never fully judged, so
// it's unverified.
func (s *Service) supersede(ctx context.Context, w *Watch, now time.Time, reason string) error {
	w.Unverified = true
	return s.settle(ctx, w, now, StatusSuperseded, w.Verdict, reason)
}

// refused reports whether planning refused the rollback for good.
func refused(err error) bool {
	for _, target := range []error{
		planning.ErrInvalid, planning.ErrCanaryReverted, planning.ErrNoPreviousRelease, planning.ErrReleaseRunning,
		planning.ErrNotFound, security.ErrForbidden, projects.ErrEnvironmentNotFound,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// rollBack follows a breached watch's rollback: it approves the plan once
// it's ready, as Loomplane automation, then waits for its deployment.
func (s *Service) rollBack(ctx context.Context, w *Watch, now time.Time) error {
	if w.RollbackPlanID == "" {
		// Save first, so of two ticks that overlap only one goes on.
		w.UpdatedAt = now
		if err := s.store.Save(ctx, w); err != nil {
			return err
		}
		return s.createRollback(ctx, w, now)
	}
	plan, err := s.plans.GetPlan(ctx, w.OrgID, w.RollbackPlanID)
	if err != nil {
		return err
	}
	blocked := func(reason string) error {
		return s.settle(ctx, w, now, StatusRollbackBlocked, w.Verdict, "Loomplane couldn't roll the release back: "+reason)
	}
	switch plan.Status {
	case planning.StatusPlanning:
		if !now.Before(w.BoundAt.Add(neverStarted)) {
			return s.settle(ctx, w, now, StatusRollbackFailed, w.Verdict, "the rollback plan never finished planning")
		}
		return nil
	case planning.StatusReady:
		return s.approve(ctx, w, now, plan)
	case planning.StatusBlocked:
		reason := "its plan is blocked."
		if len(plan.Problems) > 0 {
			reason = plan.Problems[0].Message
		}
		return blocked(reason)
	case planning.StatusFailed:
		return blocked(plan.Error)
	case planning.StatusRejected, planning.StatusExpired:
		return blocked("its plan was " + plan.Status + ".")
	case planning.StatusSuperseded:
		// Someone planned the environment again; the release is theirs
		// to move.
		return s.supersede(ctx, w, now, "A newer plan for "+w.Environment+" superseded the rollback plan before it deployed.")
	case planning.StatusStale:
		// What's deployed changed after the rollback was planned. When a
		// newer release is why, the release is that one's.
		newer, err := s.superseded(ctx, w)
		if err != nil {
			return err
		}
		if newer != "" {
			return s.supersede(ctx, w, now, newer)
		}
		return s.settle(ctx, w, now, StatusRollbackBlocked, w.Verdict, rollbackStale)
	case planning.StatusApproved:
		// Approving a plan that can't deploy records the decision and
		// deploys nothing.
		if !plan.Executable {
			return blocked("its plan has nothing it can deploy.")
		}
		return s.followDeployment(ctx, w, now, plan)
	default:
		return blocked(fmt.Sprintf("its plan is %q, which Loomplane doesn't expect.", plan.Status))
	}
}

// approve approves a ready rollback plan once. When the automation may
// not, the plan is left ready for a person.
func (s *Service) approve(ctx context.Context, w *Watch, now time.Time, plan *planning.Plan) error {
	// The decision is on its way to the plan's workflow.
	if w.RollbackApprovedAt != nil {
		return nil
	}
	_, err := s.rollbacks.ApproveAutomatic(ctx, w.OrgID, plan.ID)
	switch {
	case errors.Is(err, security.ErrForbidden):
		return s.settle(ctx, w, now, StatusRollbackBlocked, w.Verdict, automationDenied)
	case errors.Is(err, planning.ErrNotReady), errors.Is(err, planning.ErrSuperseded):
		// The plan moved on; the next tick reads where to.
		return nil
	case errors.Is(err, planning.ErrInvalid):
		return s.settle(ctx, w, now, StatusRollbackBlocked, w.Verdict, "Loomplane couldn't roll the release back: "+err.Error())
	case err != nil:
		return fmt.Errorf("approve the rollback: %w", err)
	}
	at := now
	w.RollbackApprovedAt, w.UpdatedAt = &at, now
	return s.store.Save(ctx, w)
}

// followDeployment settles the watch once the rollback plan's deployment
// does.
func (s *Service) followDeployment(ctx context.Context, w *Watch, now time.Time, plan *planning.Plan) error {
	d, err := s.deployments.ForPlan(ctx, w.OrgID, plan.ID)
	if errors.Is(err, deployments.ErrNotFound) {
		since := w.BoundAt
		if w.RollbackApprovedAt != nil {
			since = *w.RollbackApprovedAt
		}
		if !now.Before(since.Add(neverStarted)) {
			return s.settle(ctx, w, now, StatusRollbackFailed, w.Verdict, "the rollback deployment never started")
		}
		return nil
	}
	if err != nil {
		return err
	}
	switch d.Status {
	case deployments.StatusSucceeded:
		return s.settle(ctx, w, now, StatusRolledBack, w.Verdict, w.Reason)
	case deployments.StatusFailed:
		// The runner refused it because a newer release deployed first:
		// the release is that one's now.
		if d.Error == deployments.StaleRollback {
			newer, err := s.superseded(ctx, w)
			if err != nil {
				return err
			}
			if newer == "" {
				newer = "A newer release deployed after the rollback was approved, so this one isn't rolled back."
			}
			return s.supersede(ctx, w, now, newer)
		}
		return s.settle(ctx, w, now, StatusRollbackFailed, w.Verdict, "The rollback deployment failed: "+d.Error)
	case deployments.StatusCancelled:
		return s.settle(ctx, w, now, StatusRollbackFailed, w.Verdict, "The rollback deployment was cancelled.")
	}
	return nil
}

// settle ends w with status, verdict and reason, and audits it.
func (s *Service) settle(ctx context.Context, w *Watch, now time.Time, status, verdict, reason string) error {
	at := now
	w.Status, w.Verdict, w.Reason, w.SettledAt, w.UpdatedAt = status, verdict, reason, &at, now
	if err := s.store.Save(ctx, w); err != nil {
		return err
	}
	s.auditSettle(ctx, w)
	return nil
}

// auditBreach records, as Loomplane automation, that w breached: the
// service and signal that failed, and the rollback planned and the
// release it restores, or why planning refused it.
func (s *Service) auditBreach(ctx context.Context, w *Watch, plan *planning.Plan, refusal error) {
	if s.audit == nil {
		return
	}
	attrs := map[string]any{
		"watch_id": w.ID, "deployment_id": w.DeploymentID, "environment": w.Environment, "reason": w.Reason,
	}
	for _, svc := range w.Services {
		if svc.Verdict == string(releasehealth.Fail) {
			attrs["service"], attrs["signal"] = svc.Name, svc.Reason
			break
		}
	}
	event := projects.AuditEvent{
		Action: "release_watch.breach", Severity: "warning", ProjectID: w.ProjectID, Principal: security.SystemPrincipal(w.OrgID), Attributes: attrs,
	}
	if plan != nil {
		attrs["rollback_plan_id"] = plan.ID
		if plan.Rollback != nil {
			attrs["restore_deployment_id"], attrs["restore_plan_id"] = plan.Rollback.RestoreDeploymentID, plan.Rollback.RestorePlanID
		}
	}
	if refusal != nil {
		event.Outcome, attrs["refused"] = "failure", refusal.Error()
	}
	// A tick whose budget ran out after the save still records it.
	s.audit.Record(context.WithoutCancel(ctx), event)
}
