package agentops

import (
	"context"
	"errors"
	"fmt"
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

// errNotAvailable answers an operation whose handler lands later.
var errNotAvailable = errors.New("not available yet")

// PlanRisk is the risk of approving plan in an environment of envKind
// (projects.Kind*). Outside production it's deploy_nonproduction. In
// production it's destroy_production when any action deletes, or replaces
// something that holds data (planning.DataBearing); otherwise
// promote_production. Replacing a stateless service is never a destroy.
func PlanRisk(plan *planning.Plan, envKind string) defs.Risk {
	if envKind != projects.KindProduction {
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
)

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
		if in.From != "" {
			return nil, fmt.Errorf("%w: promoting from another environment isn't available yet", errInvalid)
		}
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
			return nil, err
		}
		plan, err := d.Plans.Create(ctx, call.Principal, p.ID, planning.CreatePlanInput{Environment: in.Environment, Scope: in.Scope})
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
		plan, err := d.Plans.Reject(ctx, call.Principal, in.PlanID, planning.DecideInput{Note: in.Note})
		if err != nil {
			return nil, err
		}
		return d.quotePlan(ctx, plan), nil
	})

	// The handler lands with the tool authorizer and the promotion gate
	// (Task 14). Until then it approves nothing.
	Register(r, def("approve_plan"), security.DeploymentApprove, func(context.Context, Call, defs.ApproveInput) (*planning.Plan, error) {
		return nil, fmt.Errorf("approve_plan is %w: ask a person to approve the plan", errNotAvailable)
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
