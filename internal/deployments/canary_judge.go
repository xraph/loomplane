package deployments

import (
	"context"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/releasehealth"
)

var _ orchestration.CanaryDeployer = (*Runner)(nil)

// CanaryJudge implements orchestration.CanaryDeployer. It judges window w
// of a step's attempt: for each service still ramping it reads the
// baseline's and the candidate's health over w, judges them against the
// canary's thresholds and stores the verdict, then returns the step's
// overall judgement. Any fail fails the step, else any inconclusive holds
// it, else it passes; the reason is the deciding service's, led by its
// name. A service an earlier attempt promoted already runs the release and
// isn't judged. A verdict already stored for this step and attempt is
// kept and used as it is, so a repeated judgement answers as it did; the
// answer is always what's stored. Verdicts of an earlier run of a retried
// deployment don't count.
func (r *Runner) CanaryJudge(ctx context.Context, orgID, deploymentID string, step, attempt int, w releasehealth.Window) (orchestration.CanaryJudgement, error) {
	st, ok := r.store.(CanaryStore)
	if !ok {
		return orchestration.CanaryJudgement{}, fmt.Errorf("this deployment store can't keep a canary")
	}
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return orchestration.CanaryJudgement{}, err
	}
	c := d.Canary
	switch {
	case c == nil:
		return orchestration.CanaryJudgement{}, fmt.Errorf("deployment %s has no canary", deploymentID)
	case step < 0 || step >= len(c.Steps):
		return orchestration.CanaryJudgement{}, fmt.Errorf("the canary has no step %d", step)
	}
	stored, err := st.Verdicts(ctx, orgID, deploymentID)
	if err != nil {
		return orchestration.CanaryJudgement{}, err
	}
	// A retried deployment ramps again, so only this run's verdicts count.
	stored = slices.DeleteFunc(stored, func(v CanaryVerdict) bool { return v.Retry != d.Retries })
	var ids []string
	recorded := false
	for _, svc := range c.Services {
		if promoted(d, svc) {
			continue
		}
		if svc.Candidate == "" {
			return orchestration.CanaryJudgement{}, fmt.Errorf("%s has no candidate up, so it can't be judged", svc.Name)
		}
		id := VerdictID(deploymentID, d.Retries, svc.ActionID, step, attempt)
		ids = append(ids, id)
		if slices.ContainsFunc(stored, func(s CanaryVerdict) bool { return s.ID == id }) {
			continue
		}
		v := r.judgeService(ctx, d, svc, step, attempt, w, stored)
		if err = st.RecordVerdict(ctx, &v); err != nil {
			return orchestration.CanaryJudgement{}, fmt.Errorf("record %s's verdict: %w", svc.Name, err)
		}
		recorded = true
	}
	if len(ids) == 0 {
		return orchestration.CanaryJudgement{Verdict: string(releasehealth.Pass), Reason: "every service is already promoted"}, nil
	}
	if recorded {
		// Answer with what's stored, which an overlapping attempt may have
		// written first.
		if stored, err = st.Verdicts(ctx, orgID, deploymentID); err != nil {
			return orchestration.CanaryJudgement{}, err
		}
	}
	decided := make([]CanaryVerdict, 0, len(ids))
	for _, id := range ids {
		i := slices.IndexFunc(stored, func(s CanaryVerdict) bool { return s.ID == id })
		if i < 0 {
			return orchestration.CanaryJudgement{}, fmt.Errorf("verdict %s wasn't stored", id)
		}
		decided = append(decided, stored[i])
	}
	return overall(decided), nil
}

// judgeService judges one service's window.
func (r *Runner) judgeService(ctx context.Context, d *Deployment, svc CanaryService, step, attempt int, w releasehealth.Window, stored []CanaryVerdict) CanaryVerdict {
	c := d.Canary
	baseline := releasehealth.Health{Reason: svc.Name + "'s baseline revision isn't known."}
	if svc.Baseline != "" {
		baseline = r.readHealth(ctx, d.OrgID, svc, svc.Baseline, w)
	}
	candidate := r.readHealth(ctx, d.OrgID, svc, svc.Candidate, w)
	minRequests := max(c.Thresholds.MinRequests, 1)
	note, fallback := "", false
	if baseline.Available && baseline.Requests < minRequests {
		// At full weight the baseline serves nothing, so the last earlier
		// step's own baseline that served enough stands in for it.
		for i := len(stored) - 1; i >= 0; i-- {
			s := stored[i]
			if s.ActionID == svc.ActionID && s.Step < step && !s.BaselineFallback && s.Baseline.Available && s.Baseline.Requests >= minRequests {
				baseline, note, fallback = s.Baseline, fmt.Sprintf(" (baseline from %d%%)", s.Weight), true
				break
			}
		}
	}
	verdict, reason := releasehealth.Judge(baseline, candidate, c.Thresholds)
	return CanaryVerdict{
		OrgID: d.OrgID, DeploymentID: d.ID, Retry: d.Retries, ActionID: svc.ActionID, Service: svc.Name, Step: step, Attempt: attempt,
		Weight: c.Steps[step], Verdict: verdict, Reason: reason + note, BaselineFallback: fallback, Baseline: baseline, Candidate: candidate,
		Thresholds: c.Thresholds, WindowStart: w.Start, WindowEnd: w.End, DecidedAt: r.now().UTC(),
	}
}

// readHealth reads one revision's health. A read that fails is a reading
// nobody can judge, not a failed step.
func (r *Runner) readHealth(ctx context.Context, orgID string, svc CanaryService, revision string, w releasehealth.Window) releasehealth.Health {
	if r.health == nil {
		return releasehealth.Health{Reason: "Loomplane has no way to read " + svc.Name + "'s metrics."}
	}
	h, err := r.health.Health(ctx, orgID, svc.BindingID, revision, w)
	if err != nil {
		return releasehealth.Health{Reason: fmt.Sprintf("Loomplane couldn't read %s's metrics: %v", svc.Name, err)}
	}
	return h
}

// overall folds services' verdicts into the step's: the first fail, else
// the first inconclusive, else pass.
func overall(vs []CanaryVerdict) orchestration.CanaryJudgement {
	for _, want := range []releasehealth.Verdict{releasehealth.Fail, releasehealth.Inconclusive} {
		for _, v := range vs {
			if v.Verdict == want {
				return orchestration.CanaryJudgement{Verdict: string(want), Reason: v.Service + ": " + v.Reason}
			}
		}
	}
	reason := ""
	for i, v := range vs {
		if i > 0 {
			reason += " "
		}
		reason += v.Service + ": " + v.Reason
	}
	return orchestration.CanaryJudgement{Verdict: string(releasehealth.Pass), Reason: reason}
}
