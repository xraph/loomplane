package agentops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// How create_plan waits: it reads the plan every planPoll until it's
// planned, for at most planWait.
var (
	planPoll = time.Second
	planWait = 10 * time.Minute
)

// errNeedsAPerson is a call only a person may make: the agent asks one
// instead.
var errNeedsAPerson = errors.New("needs a person")

// The reasons approve_plan gives for leaving a plan to a person.
var (
	errDestroyNeedsAPerson = fmt.Errorf("%w: the plan removes or replaces production data or resources, which always needs a person; ask one to approve it", errNeedsAPerson)
	errBackupNeedsAPerson  = fmt.Errorf("%w: the plan sets a default backup schedule, which a person who manages backups approves; ask one to approve it", errNeedsAPerson)
)

// SetsBackupSchedule reports whether approving plan sets a default backup
// schedule, which needs backup:manage. The agent's role never holds it.
func SetsBackupSchedule(plan *planning.Plan) bool {
	return slices.ContainsFunc(plan.Actions, func(a planning.Action) bool { return a.BackupEveryHours != 0 })
}

// NonProduction reports whether an environment of kind is one the agent
// may deploy to within its permissions: staging, preview or development.
// Any other kind, an empty or unknown one included, counts as production.
func NonProduction(kind string) bool {
	switch kind {
	case projects.KindStaging, projects.KindPreview, projects.KindDevelopment:
		return true
	}
	return false
}

// PlanRisk is the risk of approving plan in an environment of envKind
// (projects.Kind*). In a non-production environment (NonProduction) it's
// deploy_nonproduction. Anywhere else it's destroy_production when any
// action deletes, or replaces something that holds data
// (planning.DataBearing); otherwise promote_production. Replacing a
// stateless service is never a destroy.
func PlanRisk(plan *planning.Plan, envKind string) defs.Risk {
	if NonProduction(envKind) {
		return defs.RiskDeployNonProduction
	}
	for _, a := range plan.Actions {
		if a.Op == planning.Delete || (a.Op == planning.Replace && planning.DataBearing(a)) {
			return defs.RiskDestroyProduction
		}
	}
	return defs.RiskPromoteProduction
}

// Outputs.
type (
	manifestOutput struct {
		Revision int    `json:"revision,omitempty"`
		YAML     string `json:"yaml"`
	}
	plansOutput struct {
		Plans []*planning.Plan `json:"plans"`
	}
	deployedOutput struct {
		Sources []planning.DeployedRepository `json:"sources"`
	}
	// decisionOutput is a plan's decision: who made it, on which
	// revision.
	decisionOutput struct {
		PlanID    string `json:"plan_id"`
		Status    string `json:"status"`
		DecidedBy string `json:"decided_by,omitempty"`
		Revision  string `json:"revision"`
		// Decision says who decided a plan someone else decided, by name,
		// such as "approved by Ada", quoted: a name is what the person
		// typed.
		Decision string `json:"decision,omitempty"`
	}
)

func decisionOf(p *planning.Plan) decisionOutput {
	return decisionOutput{PlanID: p.ID, Status: p.Status, DecidedBy: p.DecidedBy, Revision: p.Revision}
}

func registerPlans(r *Registry, d *Deps) {
	Register(r, def("get_manifest"), security.ProjectRead, func(ctx context.Context, call Call, in defs.ProjectInput) (manifestOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return manifestOutput{}, err
		}
		rev, err := d.Manifests.Current(ctx, call.Principal, p.ID)
		if err != nil {
			return manifestOutput{}, err
		}
		return manifestOutput{Revision: rev.Revision, YAML: rev.YAML}, nil
	})

	Register(r, def("draft_manifest"), security.ProjectRead, func(ctx context.Context, call Call, in defs.ProjectInput) (manifestOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return manifestOutput{}, err
		}
		draft, err := d.Plans.DraftManifest(ctx, call.Principal, p.ID)
		if err != nil {
			return manifestOutput{}, err
		}
		// Drafted from what the analysis read in the repositories.
		return manifestOutput{YAML: d.quote(ctx, agentsafety.SourceManifest, draft.YAML)}, nil
	})

	Register(r, def("save_manifest"), security.ProjectUpdate, func(ctx context.Context, call Call, in defs.SaveManifestInput) (manifestOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return manifestOutput{}, err
		}
		rev, _, err := d.Manifests.Save(ctx, call.Principal, p.ID, manifests.SaveInput{YAML: in.YAML})
		if err != nil {
			return manifestOutput{}, err
		}
		return manifestOutput{Revision: rev.Revision, YAML: rev.YAML}, nil
	})

	Register(r, def("list_plans"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.ProjectInput) (plansOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return plansOutput{}, err
		}
		list, _, err := d.Plans.List(ctx, call.Principal, p.ID, "", page.Cursor{}, page.Default)
		if err != nil {
			return plansOutput{}, err
		}
		out := plansOutput{Plans: make([]*planning.Plan, 0, len(list))}
		for _, pl := range list {
			if call.Scope.EnvironmentID != "" && pl.EnvironmentID != call.Scope.EnvironmentID {
				continue
			}
			out.Plans = append(out.Plans, d.quotePlan(ctx, pl))
		}
		return out, nil
	})

	Register(r, def("get_plan"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.PlanInput) (*planning.Plan, error) {
		plan, err := d.plan(ctx, call, in.PlanID)
		if err != nil {
			return nil, err
		}
		return d.quotePlan(ctx, plan), nil
	})

	Register(r, def("create_plan"), security.DeploymentCreate, func(ctx context.Context, call Call, in defs.CreatePlanInput) (*planning.Plan, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
			return nil, err
		}
		plan, err := d.Plans.Create(ctx, call.Principal, p.ID, planning.CreatePlanInput{Environment: in.Environment, Scope: in.Scope, From: in.From})
		if err != nil {
			return nil, err
		}
		if plan, err = d.waitPlan(ctx, call, plan); err != nil {
			return nil, err
		}
		return d.quotePlan(ctx, plan), nil
	})

	Register(r, def("reject_plan"), security.DeploymentApprove, func(ctx context.Context, call Call, in defs.RejectInput) (*planning.Plan, error) {
		if _, err := d.plan(ctx, call, in.PlanID); err != nil {
			return nil, err
		}
		note, err := d.outbound(ctx, in.Note)
		if err != nil {
			return nil, err
		}
		plan, err := d.Plans.Reject(ctx, call.Principal, in.PlanID, planning.DecideInput{Note: note})
		if err != nil {
			return nil, err
		}
		return d.quotePlan(ctx, plan), nil
	})

	// The tool authorizer decides first, and pauses a production plan for a
	// person unless the promotion gate allows it. A call reaches this
	// handler either allowed, or resumed after a person decided, so it
	// checks again here: a plan someone else decided is reported, not
	// approved, and a ready one is approved only if its risk and the gate
	// still allow it, at the revision the agent read.
	Register(r, def("approve_plan"), security.DeploymentApprove, func(ctx context.Context, call Call, in defs.ApproveInput) (decisionOutput, error) {
		plan, err := d.plan(ctx, call, in.PlanID)
		if err != nil {
			return decisionOutput{}, err
		}
		if plan.Status != planning.StatusReady {
			if plan.DecidedBy != "" {
				out := decisionOf(plan)
				out.Decision = d.decidedBy(ctx, plan)
				return out, nil
			}
			return decisionOutput{}, fmt.Errorf("plan %s is %s: %w", plan.ID, plan.Status, planning.ErrNotReady)
		}
		// The risk below is about this revision, so this revision is the
		// one approved: a plan that changed since the agent read it is
		// stale.
		if in.Revision != plan.Revision {
			return decisionOutput{}, planning.ErrStale
		}
		if SetsBackupSchedule(plan) {
			return decisionOutput{}, errBackupNeedsAPerson
		}
		kind, err := d.environmentKind(ctx, call, plan.ProjectID, plan.EnvironmentID)
		if err != nil {
			return decisionOutput{}, err
		}
		switch PlanRisk(plan, kind) {
		case defs.RiskDestroyProduction:
			return decisionOutput{}, errDestroyNeedsAPerson
		case defs.RiskPromoteProduction:
			if promoteErr := d.promote(ctx, call, plan); promoteErr != nil {
				return decisionOutput{}, promoteErr
			}
		}
		note, err := d.outbound(ctx, in.Note)
		if err != nil {
			return decisionOutput{}, err
		}
		approved, err := d.Plans.Approve(ctx, call.Principal, plan.ID, planning.DecideInput{Revision: plan.Revision, Note: note})
		if err != nil {
			return decisionOutput{}, err
		}
		return decisionOf(approved), nil
	}, RiskFrom(func(ctx context.Context, call Call, in defs.ApproveInput) (defs.Risk, error) {
		plan, err := d.plan(ctx, call, in.PlanID)
		if err != nil {
			return "", err
		}
		kind, err := d.environmentKind(ctx, call, plan.ProjectID, plan.EnvironmentID)
		if err != nil {
			return "", err
		}
		return PlanRisk(plan, kind), nil
	}))

	Register(r, def("deployed"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.EnvironmentInput) (deployedOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return deployedOutput{}, err
		}
		if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
			return deployedOutput{}, err
		}
		list, err := d.Plans.Deployed(ctx, call.Principal, p.ID, in.Environment)
		if err != nil {
			return deployedOutput{}, err
		}
		out := deployedOutput{Sources: make([]planning.DeployedRepository, 0, len(list))}
		for _, r := range list {
			out.Sources = append(out.Sources, *r)
		}
		return out, nil
	})
}

// plan reads a plan through the planning service and checks it's in the
// run's scope.
func (d *Deps) plan(ctx context.Context, call Call, id string) (*planning.Plan, error) {
	plan, err := d.Plans.Get(ctx, call.Principal, id)
	if err != nil {
		return nil, err
	}
	if scopeErr := call.inScope(ctx, plan.ProjectID, plan.EnvironmentID); scopeErr != nil {
		return nil, scopeErr
	}
	return plan, nil
}

// personText is the source a person's own words are quoted with, as
// agentrequests quotes their answers.
const personText = "person_answer"

// decidedBy is "approved by <name>" or "rejected by <name>" for a plan a
// person decided, quoted, or "" when nobody can name them or no person
// decided it (the agent or Loomplane automation did).
func (d *Deps) decidedBy(ctx context.Context, plan *planning.Plan) string {
	by := plan.DecidedBy
	if d.People == nil || by == "" || strings.Contains(by, ":") {
		return ""
	}
	if _, agent := security.IsAgentSubject(by); agent {
		return ""
	}
	verb := "approved"
	if plan.Status == planning.StatusRejected {
		verb = "rejected"
	}
	return d.quote(ctx, personText, verb+" by "+d.People.Name(ctx, by))
}

// promote asks Promote whether the agent may approve a production plan
// alone, and refuses with the reasons when it may not.
func (d *Deps) promote(ctx context.Context, call Call, plan *planning.Plan) error {
	if d.Promote == nil {
		return fmt.Errorf("%w: nothing decides production promotions here; ask a person to approve the plan", errNeedsAPerson)
	}
	allowed, failed, err := d.Promote(ctx, call, plan)
	switch {
	case err != nil:
		return fmt.Errorf("%w: the promotion checks couldn't run; ask a person to approve the plan", errNeedsAPerson)
	case !allowed && len(failed) > 0:
		return fmt.Errorf("%w: %s; ask a person to approve the plan", errNeedsAPerson, strings.Join(failed, "; "))
	case !allowed:
		return fmt.Errorf("%w: ask a person to approve the plan", errNeedsAPerson)
	}
	return nil
}

// outbound is text the agent writes for people, such as a decision's
// note, as the outbound scan lets it go out. Empty text stays empty.
func (d *Deps) outbound(ctx context.Context, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	out, err := d.Safety.Outbound(ctx, text)
	if err != nil {
		return "", fmt.Errorf("the note: %w; leave it out or write another", err)
	}
	return out, nil
}

// environmentKind is the kind of one of a project's environments, by ID.
func (d *Deps) environmentKind(ctx context.Context, call Call, projectID, environmentID string) (string, error) {
	envs, err := d.Projects.Environments(ctx, call.Principal, projectID)
	if err != nil {
		return "", err
	}
	for _, e := range envs {
		if e.ID == environmentID {
			return e.Kind, nil
		}
	}
	return "", projects.ErrEnvironmentNotFound
}

// waitPlan reads the plan until it's planned, at most planWait.
func (d *Deps) waitPlan(ctx context.Context, call Call, plan *planning.Plan) (*planning.Plan, error) {
	if plan.Status != planning.StatusPlanning {
		return plan, nil
	}
	ctx, cancel := context.WithTimeout(ctx, planWait)
	defer cancel()
	tick := time.NewTicker(planPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("plan %s was still planning after %s; read it with get_plan later: %w", plan.ID, planWait, ctx.Err())
		case <-tick.C:
		}
		next, err := d.Plans.Get(ctx, call.Principal, plan.ID)
		if err != nil {
			return nil, err
		}
		if next.Status != planning.StatusPlanning {
			return next, nil
		}
	}
}
