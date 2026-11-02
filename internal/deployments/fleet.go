package deployments

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

func (r *Runner) checkInstancePlan(ctx context.Context, p *planning.Plan) error {
	if p.ManagedInstanceID == "" && p.Kind != planning.KindInstance && p.FleetInput == nil {
		return nil
	}
	if p.Kind != planning.KindInstance || p.FleetInput == nil {
		return projects.ErrFleetManaged
	}
	in := p.FleetInput
	if r.instances == nil {
		return errors.New("instance authorization is unavailable")
	}
	if p.ManagedInstanceID != in.Snapshot.InstanceID || p.EnvironmentID != in.Snapshot.EnvironmentID || p.OrgID != in.Authority.Scope.OrgID || p.ProjectID != in.Authority.Scope.ProjectID {
		return projects.ErrFleetManaged
	}
	_, err := r.instances.VerifyInstanceSnapshot(ctx, in.Authority, in.Snapshot)
	return err
}

// deploymentVariables excludes inherited variables from sealed instance plans.
// All effect entry points first call checkInstancePlan, which rejects instance
// plans when their verifier is unavailable.
func (r *Runner) deploymentVariables(ctx context.Context, d *Deployment, service string) (resolved map[string]string, sensitive map[string]bool, failure error) {
	if r.instances == nil {
		return r.variables.Values(ctx, d.OrgID, d.ProjectID, d.Environment, service)
	}
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return nil, nil, err
	}
	if err := r.checkInstancePlan(ctx, plan); err != nil {
		return nil, nil, err
	}
	if plan.FleetInput == nil {
		return r.variables.Values(ctx, d.OrgID, d.ProjectID, d.Environment, service)
	}
	snapshot := plan.FleetInput.Snapshot
	if plan.ProjectID != d.ProjectID || snapshot.EnvironmentID != d.EnvironmentID {
		return nil, nil, projects.ErrFleetManaged
	}
	name, ok := strings.CutPrefix(service, "svc:artifact/")
	if !ok {
		return nil, nil, projects.ErrFleetManaged
	}
	values, ok := snapshot.Values[name]
	if !ok {
		return nil, nil, projects.ErrFleetManaged
	}
	values = maps.Clone(values)
	secrets := map[string]bool{}
	resolver, ok := r.variables.(fleets.PinnedSecrets)
	for key, ref := range snapshot.Secrets[name] {
		if !ok {
			return nil, nil, errors.New("pinned instance secrets are unavailable")
		}
		value, err := resolver.ResolvePinnedSecret(ctx, environments.PinnedSecretScope{OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, InstanceID: snapshot.InstanceID, Service: name}, ref)
		if err != nil {
			return nil, nil, err
		}
		values[key] = string(value)
		secrets[key] = true
		clear(value)
	}
	return values, secrets, nil
}

// requireUnmanagedDeployment protects operator APIs as well as runner effects.
func (s *Service) requireUnmanagedDeployment(ctx context.Context, d *Deployment) error {
	if s.Projects == nil {
		return nil
	}
	reader, ok := s.Projects.(interface {
		GetEnvironment(context.Context, string, string, string) (*projects.Environment, error)
	})
	if !ok {
		return errors.New("environment ownership lookup is unavailable")
	}
	env, err := reader.GetEnvironment(ctx, d.OrgID, d.ProjectID, d.EnvironmentID)
	if err != nil {
		return err
	}
	if env.InstanceID != "" {
		return projects.ErrFleetManaged
	}
	return nil
}
