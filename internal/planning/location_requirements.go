package planning

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type locationRequirements struct {
	Workload     provider.Workload
	Dependencies []locationDependency
	Unverified   bool
}

type locationDependency struct {
	Config   ServiceConfig
	Cloud    string
	Resource bool
}

// locationWorkload uses the same source matching and explicit kind override as
// desired. A previous binding never determines what a new plan asks to run.
func locationWorkload(m *manifest.Manifest, name string, g *graph.Graph) (provider.Workload, error) {
	svc := m.Services[name]
	kind := svc.Kind
	if g != nil {
		p := &planner{in: Input{Graph: g}}
		repo, ok := p.repoOf(name, "services."+name, svc)
		if !ok {
			return "", fmt.Errorf("location placement cannot resolve service repository")
		}
		gs := p.match(name, "services."+name, repo, svc)
		if gs == nil {
			return "", fmt.Errorf("location placement cannot resolve analyzed service")
		}
		if kind == "" {
			kind = string(gs.Kind)
		}
	}
	if !graph.ServiceKind(kind).Deployable() {
		return "", fmt.Errorf("location placement requires a resolved deployable workload")
	}
	return provider.Workload(kind), nil
}

// locationRequirements reads only this plan's scoped bindings and declared
// destinations. Unknown dependency destinations are not network evidence.
func (s *Service) locationRequirements(plan *Plan, m *manifest.Manifest, name string, g *graph.Graph, a *cloudprofiles.Assignment, bindings []*Binding, options []ConnectionOption, prior map[string]locationSelection, held map[string]bool, chosen map[string]string, pools map[string]*cloudinfra.Placement) (locationRequirements, error) {
	out := locationRequirements{}
	var err error
	out.Workload, err = locationWorkload(m, name, g)
	if err != nil {
		return out, err
	}
	for _, need := range m.Services[name].Needs {
		var cfg ServiceConfig
		var resource bool
		var known bool
		for _, b := range bindings {
			if b.Name != need {
				continue
			}
			if b.OrgID != "" && b.OrgID != plan.OrgID || b.ProjectID != "" && b.ProjectID != plan.ProjectID || b.EnvironmentID != "" && b.EnvironmentID != plan.EnvironmentID {
				out.Unverified = true
				continue
			}
			switch b.Subject {
			case ServiceSubject:
				if _, declared := m.Services[need]; !declared {
					continue
				}
				if err := json.Unmarshal(b.Config, &cfg); err != nil {
					return out, err
				}
				if cfg.Target == "" {
					cfg.Target = b.ConnectionID
				}
				if cfg.Provider == "" {
					cfg.Provider = b.Provider
				}
				known = true
			case ResourceSubject:
				if _, declared := m.Resources[need]; !declared {
					continue
				}
				var r ResourceConfig
				if err := json.Unmarshal(b.Config, &r); err != nil {
					return out, err
				}
				if r.ExistingResource != nil {
					out.Unverified = true
					continue
				}
				cfg = ServiceConfig{Provider: r.Provider, Target: r.Target, Region: r.Region, TargetSettings: r.TargetSettings, CloudPlacement: r.CloudPlacement}
				if cfg.Target == "" {
					cfg.Target = b.ConnectionID
				}
				if cfg.Provider == "" {
					cfg.Provider = b.Provider
				}
				resource, known = true, true
			}
		}
		if held[need] {
			if !known {
				out.Unverified = true
				continue
			}
			if !resource && cfg.Public {
				continue
			}
		} else if r, exists := m.Resources[need]; exists {
			resource = true
			if r.GatewayIn(plan.Environment) {
				continue
			}
			set := r.For(plan.Environment, m.Defaults)
			if !manifest.On(set.Enabled) {
				out.Unverified = true
				continue
			}
			if set.Provider != "" {
				cfg.Provider = set.Provider
			}
			if set.Target != "" {
				cfg.Target = set.Target
			}
			if set.Region != "" {
				cfg.Region = set.Region
			}
		} else if dep, exists := m.Services[need]; exists {
			set := dep.For(plan.Environment, m.Defaults)
			placed := pools[need]
			appliedMove := known && set.Placement == nil && placed != nil && cfg.CloudPlacement != nil && cfg.CloudPlacement.PoolID != "" && placed.ServerID != cfg.CloudPlacement.ServerID
			if !manifest.On(set.Enabled) {
				out.Unverified = true
				continue
			}
			if set.Provider != "" {
				cfg.Provider = set.Provider
			}
			if set.Target != "" {
				cfg.Target = set.Target
			}
			if set.Region != "" {
				cfg.Region = set.Region
			}
			if !appliedMove && (!known || dep.Kind != "" || g != nil) {
				workload, e := locationWorkload(m, need, g)
				if e != nil {
					out.Unverified = true
					continue
				}
				cfg.Workload = workload
				cfg.Public = workload != provider.Worker
			}
			if !appliedMove && set.Public != nil {
				cfg.Public = *set.Public
			}
			if cfg.Workload == provider.Worker {
				out.Unverified = true
				continue
			}
			if cfg.Public {
				continue
			}
			if selection, selected := prior[need]; selected && selection.Review != nil {
				for _, pin := range selection.Review.Candidates {
					if pin.TargetID == selection.Review.SelectedTargetID {
						cfg.Target = pin.RuntimeConnectionID
					}
				}
				for _, target := range a.Targets {
					if target.ID == selection.Review.SelectedTargetID {
						cfg.Provider = target.Provider
						cfg.Region = target.Region
					}
				}
			} else if set.CloudTarget != "" || len(set.CloudTags) > 0 {
				target, e := SelectCloudTarget(set, a, need)
				if e != nil || target == nil {
					out.Unverified = true
					continue
				}
				cfg.Provider, cfg.Region = target.Provider, target.Region
				if target.PoolID == "" {
					cfg.Target = target.ConnectionID
				} else if placed := pools[need]; placed != nil && placed.PoolID == target.PoolID {
					cfg.Target = placed.ConnectionID
				} else if cfg.CloudPlacement == nil || cfg.CloudPlacement.TargetID != target.ID {
					out.Unverified = true
					continue
				}
			} else if set.Placement != nil {
				out.Unverified = true
				continue
			}
		} else {
			out.Unverified = true
			continue
		}
		prov, e := s.providers.Get(cfg.Provider)
		if e != nil || !prov.Available {
			out.Unverified = true
			continue
		}
		if cfg.Target == "" {
			cfg.Target = chosen[prov.Cloud]
		}
		option := locationConnection(options, cfg.Target)
		if option == nil {
			out.Unverified = true
			continue
		}
		cfg.TargetSettings = ReviewedConnectionSettings(option.Cloud, option.Details)
		cfg.TargetAccountIdentity = connections.AccountIdentity(option.Cloud, option.Account, option.Details)
		out.Dependencies = append(out.Dependencies, locationDependency{Config: cfg, Cloud: prov.Cloud, Resource: resource})
	}
	return out, nil
}

func locationConnection(options []ConnectionOption, id string) *ConnectionOption {
	i := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == id && c.Ready })
	if i < 0 {
		return nil
	}
	return &options[i]
}

func locationDependenciesReachable(req locationRequirements, destination *ServiceConfig, cloud string) bool {
	if req.Unverified {
		return false
	}
	for _, dep := range req.Dependencies {
		if OwnedCloud(dep.Cloud) {
			if !sameTargetNetwork(destination, &dep.Config, cloud, dep.Cloud) {
				return false
			}
			continue
		}
		if cloud != dep.Cloud || destination.TargetAccountIdentity == "" || destination.TargetAccountIdentity != dep.Config.TargetAccountIdentity {
			return false
		}
		if !dep.Resource && dep.Config.Provider != renderTarget && dep.Config.Provider != cloudRun && dep.Config.Provider != ecsFargate && dep.Config.Provider != azureApps {
			return false
		}
		if (dep.Config.Provider == ecsFargate || dep.Config.Provider == azureApps) && destination.Region != dep.Config.Region {
			return false
		}
	}
	return true
}

// locationServiceOrder resolves constrained dependencies before consumers while
// retaining the historical lexical order when there are no location constraints.
func locationServiceOrder(m *manifest.Manifest, environment string) []string {
	names := m.ServiceNames()
	constrained := false
	for _, name := range names {
		constrained = constrained || m.Services[name].For(environment, m.Defaults).Placement != nil
	}
	if !constrained {
		return names
	}
	visited := map[string]bool{}
	var out []string
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		needs := slices.Clone(m.Services[name].Needs)
		slices.Sort(needs)
		for _, need := range needs {
			if _, ok := m.Services[need]; ok {
				visit(need)
			}
		}
		out = append(out, name)
	}
	for _, name := range names {
		visit(name)
	}
	return out
}
