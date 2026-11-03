package agentrequests

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/id"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// settleLimit bounds the requests one sweep looks at.
const settleLimit = 100

// Settle is the sweep's half of requests, run every 15 seconds with the
// run driver's sweep. It settles what a person did elsewhere and what ran
// out of time:
//   - a plan approval whose plan has left ready decides its checkpoint:
//     approved when a person approved the plan, rejected otherwise;
//   - a GitHub or cloud request whose connection now exists is answered;
//   - anything else past its expiry is expired, and the run hears nobody
//     answered. A plan approval's checkpoint is left to cortex's own
//     expiry.
//
// Each step is safe to repeat, and each request is read through its own
// workspace.
func (s *Service) Settle(ctx context.Context) error {
	refs, err := s.d.Store.Due(ctx, s.now().UTC(), settleLimit)
	if err != nil {
		return err
	}
	var errs []error
	for _, ref := range refs {
		r, gerr := s.d.Store.Get(ctx, ref.OrgID, ref.ID)
		if gerr != nil {
			if !errors.Is(gerr, ErrNotFound) {
				errs = append(errs, gerr)
			}
			continue
		}
		if r.Status != StatusPending {
			continue
		}
		if serr := s.settleOne(ctx, r); serr != nil {
			errs = append(errs, fmt.Errorf("settle agent request %s: %w", r.ID, serr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) settleOne(ctx context.Context, r *Request) error {
	switch r.Kind {
	case KindApprovePlan:
		return s.settleApproval(ctx, r)
	case KindConnectGitHub, KindCreateConnection:
		st, done, err := s.connected(ctx, security.AgentPrincipal(r.OrgID, nil), r)
		if err != nil {
			return err
		}
		if done {
			return s.settleQuietly(ctx, r, st)
		}
	}
	if s.now().After(r.ExpiresAt) {
		return s.settleQuietly(ctx, r, Settlement{Status: StatusExpired, Outcome: OutcomeExpired})
	}
	return nil
}

// settleQuietly settles r for the sweep and resumes its run when its pause
// is done. A run that can't be resumed for good is left as it is; one that
// failed to resume for now gets the request back, for the next sweep.
func (s *Service) settleQuietly(ctx context.Context, r *Request, st Settlement) error {
	st.At = s.now().UTC()
	moved, err := s.d.Store.Settle(ctx, r.OrgID, r.ID, st)
	if err != nil || !moved {
		return err
	}
	if err = s.resume(ctx, r, false); err != nil {
		if _, rerr := s.d.Store.Reopen(context.WithoutCancel(ctx), r.OrgID, r.ID, st); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return err
	}
	s.audit(ctx, security.SystemPrincipal(r.OrgID), r, st)
	return nil
}

// planVerdict is how a plan that left ready settles its request and its
// checkpoint.
type planVerdict struct {
	approved bool
	by       string // who decided the plan; "" when nobody did
	reason   string
	st       Settlement
}

// verdict reads plan's decision. Only a person's approval approves: one by
// the agent or Loomplane automation while the run waited is not a person
// approving this run's action.
func (s *Service) verdict(ctx context.Context, plan *planning.Plan) planVerdict {
	by := plan.DecidedBy
	name := s.name(ctx, by)
	v := planVerdict{by: by, st: Settlement{AnsweredBy: by}}
	switch plan.Status {
	case planning.StatusApproved:
		if isPerson(by) {
			v.approved, v.reason = true, "approved by "+name
			v.st.Status, v.st.Outcome = StatusAnswered, OutcomeApproved
			return v
		}
		v.reason = "the plan was approved by " + name + ", not by a person"
	case planning.StatusRejected:
		v.reason = "the plan was rejected by " + name
		v.st.Status, v.st.Outcome = StatusDeclined, OutcomeRejected
		return v
	case planning.StatusStale:
		v.reason = "the plan went stale: it changed after it was planned"
	case planning.StatusExpired:
		v.reason = "the plan expired before anyone decided it"
		v.st.Status, v.st.Outcome = StatusExpired, OutcomeExpired
		return v
	case planning.StatusSuperseded:
		v.reason = "a newer plan for the environment superseded it"
	case planning.StatusFailed:
		v.reason = "the plan failed"
	case planning.StatusBlocked:
		v.reason = "the plan is blocked by problems"
	default:
		v.reason = "the plan is " + plan.Status
	}
	v.st.Status, v.st.Outcome = StatusExpired, OutcomeStale
	return v
}

// isPerson reports whether a user ID is a person's, not Loomplane
// automation's or the agent's.
func isPerson(userID string) bool {
	if userID == "" || strings.Contains(userID, ":") {
		return false
	}
	_, agent := security.IsAgentSubject(userID)
	return !agent
}

// settleApproval decides the checkpoint of r's pause once every plan it
// waits on has left ready: approved only when a person approved each one.
// The run resumes only after that, so the resumed approve_plan call reads
// the person's decision, never a plan still waiting (and never asks the
// gate again).
func (s *Service) settleApproval(ctx context.Context, r *Request) error {
	if s.d.Plans == nil {
		return errors.New("agentrequests: the planning service isn't configured")
	}
	pause, err := s.d.Store.Pause(ctx, r.OrgID, r.RunID, r.SuspensionID)
	if err != nil {
		return err
	}
	type item struct {
		req *Request
		v   planVerdict
	}
	var items []item
	agent := security.AgentPrincipal(r.OrgID, nil)
	for _, other := range pause {
		if other.Kind != KindApprovePlan || other.Status != StatusPending {
			continue
		}
		plan, gerr := s.d.Plans.Get(ctx, agent, other.Target.PlanID)
		switch {
		case errors.Is(gerr, planning.ErrNotFound):
			items = append(items, item{other, planVerdict{reason: "the plan no longer exists", st: Settlement{Status: StatusExpired, Outcome: OutcomeStale}}})
			continue
		case gerr != nil:
			return gerr
		}
		if plan.OrgID != r.OrgID {
			return fmt.Errorf("agentrequests: plan %s isn't the workspace's", plan.ID)
		}
		if plan.Status == planning.StatusReady || plan.Status == planning.StatusPlanning {
			if s.now().After(other.ExpiresAt) {
				// Cortex's own expiry decides the checkpoint.
				if _, xerr := s.d.Store.Settle(ctx, other.OrgID, other.ID, Settlement{Status: StatusExpired, Outcome: OutcomeExpired, At: s.now().UTC()}); xerr != nil {
					return xerr
				}
			}
			return nil
		}
		items = append(items, item{other, s.verdict(ctx, plan)})
	}
	if len(items) == 0 {
		return nil
	}
	decision := checkpoint.Decision{Approved: true, DecidedAt: s.now().UTC()}
	decider := ""
	for _, it := range items {
		if !it.v.approved {
			decision.Approved, decision.Reason, decider = false, it.v.reason, it.v.by
			break
		}
		if decider == "" {
			decision.Reason, decider = it.v.reason, it.v.by
		}
	}
	task, err := s.d.Runs.ByRun(ctx, r.OrgID, r.RunID)
	if err != nil {
		return err
	}
	bound, err := s.d.Runs.Bind(ctx, task)
	if err != nil {
		return err
	}
	decision.Reason = s.untrusted(bound, decision.Reason)
	decision.DecidedBy = "loomplane"
	decideCtx := bound
	if isPerson(decider) {
		// Cortex records the person who decided, through the codec; the
		// run continues as its own principal.
		decision.DecidedBy = decider
		decideCtx = cortex.WithPrincipal(bound, agentauthz.Decider{Person: security.Principal{OrgID: r.OrgID, UserID: decider}})
	}
	cpID, err := id.ParseCheckpointID(r.CheckpointID)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	// A checkpoint already decided (by an earlier pass whose request write
	// was lost, or by another replica) keeps its first decision.
	if err = eng.DecideCheckpoint(decideCtx, cpID, decision); err != nil && !errors.Is(err, cortex.ErrInvalidState) {
		return err
	}
	now := s.now().UTC()
	for _, it := range items {
		st := it.v.st
		st.At = now
		moved, serr := s.d.Store.Settle(ctx, it.req.OrgID, it.req.ID, st)
		if serr != nil {
			return serr
		}
		if moved {
			actor := security.SystemPrincipal(r.OrgID)
			if isPerson(st.AnsweredBy) {
				actor = security.Principal{OrgID: r.OrgID, UserID: st.AnsweredBy}
			}
			s.audit(ctx, actor, it.req, st)
		}
	}
	return nil
}
