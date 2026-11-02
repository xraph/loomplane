package orchestration

import (
	"fmt"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/releasehealth"
)

// The v4 workflows are v3's with a canary ramp between the last action and
// Finish. The approval loop, the effects and Finish are v3's own code, so
// their command IDs are the same on both builds. The ramp's IDs must stay
// as they are once released, like any other build's.

// canaryStopped is why a canary reverts when capacity stops its deployment.
const canaryStopped = "The deployment was cancelled"

func (d *Deployments) workflowV4(w *drt.Workflow, input []byte) ([]byte, error) {
	run := watchCanary(w)
	return awaitDecision(w, input, run.deploy)
}

func (d *Deployments) retryV4(w *drt.Workflow, raw []byte) ([]byte, error) {
	run := watchCanary(w)
	return retryWith(w, raw, run.steps)
}

func (d *Deployments) resumeReleaseV4(w *drt.Workflow, input []byte) ([]byte, error) {
	run := watchCanary(w)
	return resumeWith(w, input, run.deploy)
}

// canaryRun is how far a v4 run got with its deployment's canary, as its
// history shows, so a cancelled run knows what it has left to settle.
type canaryRun struct {
	// dep is the deployment, once it started.
	dep deployInput
	// promoting is set once every step passed.
	promoting bool
	// settled is set once the canary promoted or reverted, or there was
	// none.
	settled bool
}

// watchCanary registers what a v4 run does when it's cancelled. It must
// run before the run's first command, to handle a cancellation accepted
// before then.
func watchCanary(w *drt.Workflow) *canaryRun {
	run := &canaryRun{}
	w.SetCancellationHandler(run.cancelled)
	return run
}

func (run *canaryRun) deploy(w *drt.Workflow, in DeploymentInput) ([]byte, error) {
	return deployWith(w, in, run.steps)
}

func (run *canaryRun) steps(w *drt.Workflow, org string, started deployStarted, interval time.Duration) error {
	return runStepsV4(w, run, org, started, interval)
}

// runStepsV4 runs v3's effects, ramps the canary, and settles the
// deployment with the ramp's reason. When capacity stops the deployment,
// any candidate it left up is reverted and the deployment stays as
// capacity left it.
func runStepsV4(w *drt.Workflow, run *canaryRun, org string, started deployStarted, interval time.Duration) error {
	run.dep = deployInput{OrgID: org, DeploymentID: started.DeploymentID}
	reason, stopped, err := capacityEffects(w, org, started, interval)
	if err != nil {
		return err
	}
	if stopped {
		rampCanary(w, run, canaryStopped)
		return nil
	}
	return finishCapacity(w, org, started.DeploymentID, rampCanary(w, run, reason), interval)
}

// rampCanary runs once every action has run or one failed (reason). It
// reads the ramp; without a canary, it returns reason as it is. With one,
// it climbs the steps unless an action failed, then promotes when every
// step passed and reverts otherwise. It returns the deployment's reason
// to fail, "" when it promoted. Whatever happens, the canary ends settled:
// a promote or revert the ramp gave up on is recorded as such.
func rampCanary(w *drt.Workflow, run *canaryRun, reason string) string {
	dep := run.dep
	p := dep.DeploymentID + "-canary"
	var ramp CanaryRamp
	if err := callInto(w, p+"-ramp", ActivityCanaryRamp, dep, activityTimeout, 0, activityAttempts, &ramp); err != nil {
		// Whether there's a canary is unknown, and reverting a deployment
		// without one does nothing, so revert: no candidate stays up.
		if reason == "" {
			reason = "The canary's ramp couldn't be read: " + err.Error()
		}
		return revertCanary(w, run, p, reason)
	}
	if len(ramp.Steps) == 0 {
		run.settled = true
		return reason
	}
	if reason == "" {
		reason = climb(w, p, dep, ramp) // "" when every step passed
	}
	if reason == "" {
		run.promoting = true
		return promoteCanary(w, run, p)
	}
	return revertCanary(w, run, p, reason)
}

// promoteCanary promotes every candidate, and returns "" or why traffic
// may still be split.
func promoteCanary(w *drt.Workflow, run *canaryRun, p string) string {
	in := canaryInput{OrgID: run.dep.OrgID, DeploymentID: run.dep.DeploymentID}
	failed := settle(w, p+"-promote", ActivityCanaryPromote, in)
	if failed != "" {
		in.Reason = failed
		giveUp(w, p+"-promote", in)
	}
	run.settled = true
	return failed
}

// revertCanary reverts every candidate because of reason, and returns
// reason, with why traffic may still be split if the revert never
// succeeded.
func revertCanary(w *drt.Workflow, run *canaryRun, p, reason string) string {
	in := canaryInput{OrgID: run.dep.OrgID, DeploymentID: run.dep.DeploymentID, Reason: reason}
	if failed := settle(w, p+"-revert", ActivityCanaryRevert, in); failed != "" {
		reason += " " + failed
		in.Reason = reason
		giveUp(w, p+"-revert", in)
	}
	run.settled = true
	return reason
}

// giveUp ends a canary whose promote or revert never succeeded, with
// in.Reason, so it says traffic may still be split and stops holding its
// environment, whatever the deployment's status. If even that can't be
// stored for as long as settle tries, nothing else can be done.
func giveUp(w *drt.Workflow, p string, in canaryInput) {
	settle(w, p+"-settle", ActivityCanarySettle, in)
}

// climb weighs each step, holds it and judges it. An inconclusive step
// holds again, up to ramp.MaxInconclusive more times. It returns why the
// canary must revert, or "" when every step passed.
func climb(w *drt.Workflow, p string, dep deployInput, ramp CanaryRamp) string {
	for i, weight := range ramp.Steps {
		in := canaryInput{OrgID: dep.OrgID, DeploymentID: dep.DeploymentID, Step: i, Weight: weight}
		var applied time.Time
		if err := callInto(w, fmt.Sprintf("%s-weigh-%d", p, i), ActivityCanaryWeigh, in, canaryEffectTimeout, 0, activityAttempts, &applied); err != nil {
			return fmt.Sprintf("Traffic couldn't move to %d%%: %v", weight, err)
		}
		applied = applied.UTC().Truncate(time.Microsecond)
		for k := 0; ; k++ {
			wait := ramp.Hold
			if k == 0 {
				wait += ramp.Lag
			}
			if _, err := w.Timer(fmt.Sprintf("%s-hold-%d-%d", p, i, k), wait).Get(); err != nil {
				return "The canary stopped waiting: " + err.Error()
			}
			in.Attempt = k
			in.Window = releasehealth.Window{Start: applied.Add(time.Duration(k) * ramp.Hold), End: applied.Add(time.Duration(k+1) * ramp.Hold)}
			var j CanaryJudgement
			if err := callInto(w, fmt.Sprintf("%s-judge-%d-%d", p, i, k), ActivityCanaryJudge, in, canaryJudgeTimeout, 0, activityAttempts, &j); err != nil {
				return fmt.Sprintf("The canary couldn't be judged at %d%%: %v", weight, err)
			}
			if j.Verdict == string(releasehealth.Pass) {
				break
			}
			if j.Verdict == string(releasehealth.Fail) {
				return fmt.Sprintf("The canary failed at %d%%: %s", weight, j.Reason)
			}
			if k >= ramp.MaxInconclusive {
				return fmt.Sprintf("The canary couldn't be judged at %d%% after %d holds: %s", weight, k+1, j.Reason)
			}
		}
	}
	return ""
}

// settle runs a promote or revert until it succeeds, once a minute for
// canarySettleRounds, and returns "" or why it never did. Each round is one
// attempt: the minute between rounds is the retry.
func settle(w *drt.Workflow, p, activity string, in canaryInput) string {
	var last error
	for n := range canarySettleRounds {
		if n > 0 {
			if _, err := w.Timer(fmt.Sprintf("%s-wait-%d", p, n-1), canarySettleWait).Get(); err != nil {
				last = err
				break
			}
		}
		if last = callInto(w, fmt.Sprintf("%s-%d", p, n), activity, in, actionTimeout, 0, 1, nil); last == nil {
			return ""
		}
	}
	return "Traffic may still be split: " + last.Error() + "."
}

// cancelled settles what a cancelled run leaves of its canary: it finishes
// a promotion every step already passed, and otherwise reverts every
// candidate, ramping or still dark. A deployment without a canary has
// nothing to revert. Either way a promote or revert it gives up on is
// recorded, so the canary never stays in flight. The deployment itself
// stays as v3 would leave it.
func (run *canaryRun) cancelled(w *drt.Workflow, c durable.ExecutionCancellation) ([]byte, error) {
	stop := &drt.WorkflowCancelledError{RequestID: c.RequestID, Reason: c.Reason}
	if run.dep.DeploymentID == "" || run.settled {
		return nil, stop
	}
	p := run.dep.DeploymentID + "-canary-cancel"
	if run.promoting {
		promoteCanary(w, run, p)
		return nil, stop
	}
	reason := canaryStopped
	if c.Reason != "" {
		reason += ": " + c.Reason
	}
	revertCanary(w, run, p, reason)
	return nil, stop
}
