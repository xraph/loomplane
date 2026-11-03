package agentops

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// errSecretNeedsAPerson is set_variable asked for a secret. The tool
// authorizer refuses such a call first (its risk is secret_or_external);
// this is the second line.
var errSecretNeedsAPerson = fmt.Errorf("%w: secret values are typed by a person: call request_secret", errNeedsAPerson)

// errEveryEnvironment is set_variable for every environment in a run
// scoped to one.
var errEveryEnvironment = fmt.Errorf("%w: this conversation is about one environment; name it", errOutOfScope)

// scopedEnvironmentName is the name of the environment the run is scoped
// to, or "" when it isn't.
func (d *Deps) scopedEnvironmentName(ctx context.Context, call Call, projectID string) (string, error) {
	if call.Scope.EnvironmentID == "" {
		return "", nil
	}
	envs, err := d.environments(ctx, call, projectID)
	if err != nil {
		return "", err
	}
	if len(envs) != 1 {
		return "", errOutOfScope
	}
	return envs[0].Name, nil
}

// variableView is a variable as the agent sees it: never a value, secret
// or not. Set says whether it has one.
type variableView struct {
	Key         string `json:"key"`
	Scope       string `json:"scope"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	Secret      bool   `json:"secret"`
	Set         bool   `json:"set"`
}

func viewOf(v *environments.Variable) variableView {
	return variableView{
		Key: v.Key, Scope: v.Scope, Environment: v.Environment, Service: v.Service, Secret: v.Secret,
		// A secret's value is never read back, so one that exists is set.
		Set: v.Secret || v.Value != "",
	}
}

type variablesOutput struct {
	Variables []variableView `json:"variables"`
}

func registerVariables(r *Registry, d *Deps) {
	Register(r, def("list_variables"), security.VariableRead, func(ctx context.Context, call Call, in defs.ProjectInput) (variablesOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return variablesOutput{}, err
		}
		list, err := d.Environments.ListProject(ctx, call.Principal, p.ID)
		if err != nil {
			return variablesOutput{}, err
		}
		scoped, err := d.scopedEnvironmentName(ctx, call, p.ID)
		if err != nil {
			return variablesOutput{}, err
		}
		out := variablesOutput{Variables: make([]variableView, 0, len(list))}
		for _, v := range list {
			// A run scoped to one environment sees its variables and the
			// ones set for every environment.
			if scoped != "" && v.Environment != "" && v.Environment != scoped {
				continue
			}
			out.Variables = append(out.Variables, viewOf(v))
		}
		return out, nil
	})

	Register(r, def("set_variable"), security.VariableManage, func(ctx context.Context, call Call, in defs.SetVariableInput) (variableView, error) {
		if in.Secret {
			return variableView{}, errSecretNeedsAPerson
		}
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return variableView{}, err
		}
		// A run scoped to one environment writes only there: neither to
		// another nor to every environment at once.
		if in.Environment != "" {
			if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
				return variableView{}, err
			}
		} else if call.Scope.EnvironmentID != "" {
			return variableView{}, errEveryEnvironment
		}
		v, err := d.Environments.CreateProject(ctx, call.Principal, p.ID, environments.CreateVariableInput{
			Key: in.Key, Value: in.Value, Environment: in.Environment, Service: in.Service,
		})
		if err != nil {
			return variableView{}, err
		}
		return viewOf(v), nil
	}, RiskFrom(func(_ context.Context, _ Call, in defs.SetVariableInput) (defs.Risk, error) {
		if in.Secret {
			return defs.RiskSecretOrExternal, nil
		}
		return defs.RiskChange, nil
	}))
}
