package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Gate is the promotion gate release preparation installs in 3a's
// authorizer. It decides only from Loomplane's records and evidence it
// reads again at every evaluation, never from the stored report's copy of
// it, so nothing a model wrote or waited out can pass a gate.
type Gate struct {
	Store     Store
	Collector *Collector
	Audit     projects.Auditor
	Now       func() time.Time
}

var _ agentauthz.PromotionGate = (*Gate)(nil)

// noReport is the line for a plan no release preparation built.
var noReport = fmt.Sprintf("gate %d (%s): no release readiness report covers this plan; ask the agent to prepare the release",
	GateFromStaging, GateNames[GateFromStaging])

// finishedLine is the line for a report that stopped reviewing while it was
// evaluated.
var finishedLine = fmt.Sprintf("gate %d (%s): the release preparation finished while it was checked",
	GateFromStaging, GateNames[GateFromStaging])

// Phrases for a requester the gate couldn't check. They're Loomplane's
// own words, since gate 1 quotes them.
const (
	requesterNoPerson   = "the release preparation names no person"
	requesterUnreadable = "their permissions couldn't be read"
)

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// Evaluate decides whether the agent may approve req.Plan without a
// person. 3a's authorizer calls it, and approve_plan's handler calls it
// again just before approving, so a report can see two evaluations per
// call; each is recorded and audited.
//
// It finds the report that built the plan, reads the project, its two
// environments and whether the requester may approve production, reads the
// evidence again (reusing only the report's diffs of the same commits),
// and checks the seven gates. Only the preparation's own run records the
// evaluation on the report. The only error is the store's, which 3a treats
// as not allowed.
func (g *Gate) Evaluate(ctx context.Context, req agentauthz.PromotionRequest) (agentauthz.GateDecision, error) {
	if req.Plan == nil {
		return agentauthz.GateDecision{Failed: []string{noReport}}, nil
	}
	orgID := req.Principal.OrgID
	report, err := g.Store.ForPlan(ctx, orgID, req.Plan.ID)
	if errors.Is(err, ErrNotFound) {
		d := agentauthz.GateDecision{Failed: []string{noReport}}
		g.audit(ctx, req, nil, d, []int{GateFromStaging})
		return d, nil
	}
	if err != nil {
		return agentauthz.GateDecision{}, fmt.Errorf("read the release readiness report: %w", err)
	}
	now := g.now()
	in := CheckInput{Report: report, TaskID: req.TaskID, Plan: req.Plan, Now: now}
	if person, ok := req.Principal.Person(); ok {
		in.Requester = person.UserID
	}
	var src Sources
	if g.Collector != nil {
		src = g.Collector.Sources
	}
	if src.Projects != nil {
		if project, err := src.Projects.Get(ctx, req.Principal, report.ProjectID); err == nil {
			in.Project = project
		}
		if envs, err := src.Projects.Environments(ctx, req.Principal, report.ProjectID); err == nil {
			for _, e := range envs {
				switch {
				case e == nil:
				case e.ID == report.FromEnvironmentID:
					in.From = e
				case e.ID == report.ToEnvironmentID:
					in.To = e
				}
			}
		}
	}
	in.RequesterCanApprove, in.RequesterError = g.requester(ctx, src.Authz, report)
	if in.From != nil && in.To != nil && g.Collector != nil {
		var diffs []SourceDiff
		if report.Evidence != nil {
			diffs = report.Evidence.Sources
		}
		in.Evidence = g.Collector.Collect(ctx, CollectInput{Principal: req.Principal, Plan: req.Plan, From: in.From, To: in.To, Diffs: diffs})
	}

	checks := Checks(in)
	d := agentauthz.GateDecision{Failed: Failed(checks, report.Concerns)}
	d.Allowed = len(d.Failed) == 0
	var failedGates []int
	for _, c := range checks {
		if !c.Passed {
			failedGates = append(failedGates, c.Number)
		}
	}

	var recordErr error
	if req.TaskID != "" && req.TaskID == report.TaskID {
		decision := DecisionAsk
		if d.Allowed {
			decision = DecisionPromote
		}
		recordErr = g.Store.RecordEvaluation(ctx, orgID, report.ID, checks, decision, in.Evidence, now)
		if errors.Is(recordErr, ErrMovedOn) || errors.Is(recordErr, ErrNotFound) {
			// The report stopped reviewing between the read and the
			// write, such as a person cancelling it: never allowed.
			recordErr = nil
			if d.Allowed {
				d = agentauthz.GateDecision{Failed: []string{finishedLine}}
				failedGates = []int{GateFromStaging}
			}
		}
	}
	if recordErr != nil {
		d = agentauthz.GateDecision{}
	}
	g.audit(ctx, req, report, d, failedGates)
	if recordErr != nil {
		return agentauthz.GateDecision{}, fmt.Errorf("record the gate evaluation: %w", recordErr)
	}
	return d, nil
}

// requester reports whether the person who asked for the release holds
// deployment:approve on the project, checked as that person, never as the
// agent. A report naming no person, or a check that fails, never passes.
func (g *Gate) requester(ctx context.Context, authz Permissions, r *ReleaseReport) (canApprove bool, why string) {
	by := r.RequestedBy
	if _, ok := security.IsAgentSubject(by); ok || by == "" {
		return false, requesterNoPerson
	}
	if _, ok := security.IsSystemSubject(by); ok {
		return false, requesterNoPerson
	}
	p := security.ActorPrincipal(r.OrgID, by)
	if p.IsServiceAccount() {
		return false, requesterNoPerson
	}
	if authz == nil {
		return false, requesterUnreadable
	}
	switch err := authz.Require(ctx, p, security.DeploymentApprove, r.ProjectID); {
	case err == nil:
		return true, ""
	case errors.Is(err, security.ErrForbidden):
		return false, ""
	default:
		return false, requesterUnreadable
	}
}

// audit records one evaluation: IDs, the decision, the failing gates by
// number and how many concerns the report has. Never their text.
func (g *Gate) audit(ctx context.Context, req agentauthz.PromotionRequest, r *ReleaseReport, d agentauthz.GateDecision, failed []int) {
	if g.Audit == nil {
		return
	}
	if failed == nil {
		failed = []int{}
	}
	attrs := map[string]any{"report_id": "", "plan_id": "", "allowed": d.Allowed, "failed_gates": failed, "concerns": 0}
	projectID := req.ProjectID
	if req.Plan != nil {
		attrs["plan_id"] = req.Plan.ID
	}
	if r != nil {
		attrs["report_id"], attrs["concerns"], projectID = r.ID, len(r.Concerns), r.ProjectID
	}
	severity := "info"
	if !d.Allowed {
		severity = "warning"
	}
	g.Audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: "release.gate.evaluate", Severity: severity, ProjectID: projectID, Principal: req.Principal, Attributes: attrs,
	})
}
