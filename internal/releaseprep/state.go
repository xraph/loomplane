package releaseprep

import (
	"strings"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// A report's reasons. They travel in realtime events and notices, so they
// are Loomplane's own words only: never a task's error, a plan's error or
// decision note, or anything the model wrote. Names in them are user IDs
// and environment names, which Loomplane keeps.
const (
	waitsForAPerson     = "the plan waits for a person in Needs approval"
	reasonNoPlan        = "the plan this release was preparing no longer exists"
	reasonNoProject     = "the project this release was for no longer exists"
	reasonNoEnvironment = "an environment this release was for no longer exists"
	reasonPromoted      = "the agent approved the plan; its canary or post-deploy watch takes it from here"
	reasonStale         = "the plan changed after it was reviewed; prepare the release again"
	reasonExpired       = "the plan expired before anyone decided"
	reasonBlocked       = "the plan has problems that block it; open it to see them"
	reasonPlanFailed    = "planning the promotion failed; open the plan to see why"
	reasonUnknownPlan   = "the plan is in a state Loomplane doesn't know"
	reasonWaiting       = "the agent asked a person; the plan waits in Needs approval"
	reasonUndecided     = "the agent finished without deciding; " + waitsForAPerson
	reasonRunCancelled  = "the agent's run was cancelled; " + waitsForAPerson
	reasonRunFailed     = "the agent's run failed; " + waitsForAPerson
	// reasonCantApprove ends a review for someone who can't approve
	// production (owner Q1): the job starts their run without the release
	// fields, so the agent can review but never approve.
	reasonCantApprove = "the agent reviewed it; it waits for someone who can approve production in Needs approval"
	reasonFailed      = "the release preparation failed"
	// reasonJobNeverRan stops a report the prepare job never moved on.
	reasonJobNeverRan   = "the preparation job never ran"
	reasonCancelled     = "the release preparation was cancelled"
	approvalDidntGoThru = "the agent's approval didn't go through"
)

// causes are the reasons a task ends that Loomplane wrote itself and that
// someone can act on: 3a stores them as the task's error.
var causes = []error{agentsettings.ErrDisabled, agentsettings.ErrBudgetExhausted, agentsettings.ErrKeyUnavailable}

// cause is the Loomplane sentence a task's error is, or "" when it's
// anything else, which may hold a provider's or a model's words.
func cause(taskError string) string {
	for _, c := range causes {
		if strings.Contains(taskError, c.Error()) {
			return c.Error()
		}
	}
	return ""
}

// DeriveState says where a report stands from its plan and the agent's
// task, and why in Loomplane's own words. A report still planning, or
// already failed or cancelled, keeps its own state. Otherwise the plan
// decides: the agent approving it is promoted, a person approving it is
// approved, and a plan still ready reads from the task (reviewing or
// promoting while it runs, waiting for a person while it waits, stopped
// once it ended without a decision, the plan then waiting for a person).
func DeriveState(r *ReleaseReport, plan *planning.Plan, task *agentruns.Task) (state, reason string) {
	switch r.Status {
	case StatusPlanning:
		return StatePlanning, ""
	case StatusFailed:
		return StateFailed, orDefault(r.StateReason, reasonFailed)
	case StatusCancelled:
		return StateCancelled, orDefault(r.StateReason, reasonCancelled)
	}
	if plan == nil {
		return StateFailed, reasonNoPlan
	}
	switch plan.Status {
	case planning.StatusApproved:
		if plan.DecidedBy == security.AgentSubject(r.OrgID) {
			return StatePromoted, reasonPromoted
		}
		return StateApproved, "approved by " + actorName(plan.DecidedBy)
	case planning.StatusRejected:
		return StateRejected, "rejected by " + actorName(plan.DecidedBy)
	case planning.StatusStale:
		return StateStale, reasonStale
	case planning.StatusExpired:
		return StateExpired, reasonExpired
	case planning.StatusSuperseded:
		return StateSuperseded, "a newer plan for " + orDefault(r.ToEnvironment, orDefault(plan.Environment, "this environment")) + " replaced it"
	case planning.StatusBlocked:
		return StateBlocked, reasonBlocked
	case planning.StatusFailed:
		return StateBlocked, reasonPlanFailed
	case planning.StatusPlanning:
		return StatePlanning, ""
	case planning.StatusReady:
		return fromTask(r, task)
	}
	return StateFailed, reasonUnknownPlan
}

// fromTask is the state of a report whose plan is ready: what the agent's
// task is doing.
func fromTask(r *ReleaseReport, task *agentruns.Task) (state, reason string) {
	switch {
	case task == nil, task.Status == agentruns.StatusQueued, task.Status == agentruns.StatusRunning:
		if r.Decision == DecisionPromote {
			return StatePromoting, ""
		}
		return StateReviewing, ""
	case task.Status == agentruns.StatusWaiting:
		return StateWaiting, reasonWaiting
	case agentruns.Terminal(task.Status):
		return StateStopped, stopped(r, task)
	}
	// A task status Loomplane doesn't know settles nothing.
	return StateReviewing, ""
}

// stopped says why a run ended with the plan still ready.
func stopped(r *ReleaseReport, task *agentruns.Task) string {
	why := cause(task.Error)
	switch {
	case r.Decision == DecisionPromote && why != "":
		return approvalDidntGoThru + ": " + why + "; " + waitsForAPerson
	case r.Decision == DecisionPromote:
		return approvalDidntGoThru + "; " + waitsForAPerson
	case why != "":
		return "the agent stopped: " + why + "; " + waitsForAPerson
	case task.Status == agentruns.StatusCancelled:
		return reasonRunCancelled
	case task.Status == agentruns.StatusFailed:
		return reasonRunFailed
	case task.ReleasePlanID == "":
		return reasonCantApprove
	}
	return reasonUndecided
}

// actorName is how a reason names whoever decided a plan.
func actorName(id string) string {
	if _, ok := security.IsAgentSubject(id); ok {
		return security.AgentActorName
	}
	if _, ok := security.IsSystemSubject(id); ok {
		return security.SystemActorName
	}
	if id == "" {
		return "someone"
	}
	return id
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
