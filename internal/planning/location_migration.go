package planning

import (
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/pkg/provider"
)

// appliedPoolMoves supplies only the unconstrained services changing hosts.
// Their source, variables and runtime settings retain the existing move contract.
func appliedPoolMoves(bindings []*Binding, assignment *cloudprofiles.Assignment, options []ConnectionOption, registry *provider.Registry, placements map[string]CloudPlacement, routes []cloudinfra.Route, selections map[string]locationSelection) (map[string]Action, []Finding) {
	names := map[string]bool{}
	for _, b := range bindings {
		if b.Subject != ServiceSubject {
			continue
		}
		if _, constrained := selections[b.Name]; constrained {
			continue
		}
		var old ServiceConfig
		next, found := placements[b.Name]
		if found && next.PoolID != "" && json.Unmarshal(b.Config, &old) == nil && old.CloudPlacement != nil && old.CloudPlacement.PoolID != "" && old.CloudPlacement.ServerID != "" && old.CloudPlacement.ServerID != next.ServerID {
			names[b.Name] = true
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	applied := appliedCloudPlan(bindings, assignment, "", options, registry, placements, routes)
	actions := map[string]Action{}
	for _, a := range applied.Actions {
		if names[a.Name] && a.Subject == ServiceSubject {
			actions[a.Name] = a
		}
	}
	var problems []Finding
	for _, f := range applied.Problems {
		if names[f.Name] {
			problems = append(problems, f)
		}
	}
	return actions, problems
}

// linkAppliedMove keeps the saved dependency declarations and checks them
// against this combined plan before its common route and capacity checks.
func (p *planner) linkAppliedMove(a *Action) {
	p.linkHeld(a)
	for _, link := range a.Service.Env {
		dep := p.planned(link.Subject, link.From)
		if dep == nil {
			p.problem(Finding{Code: "cloud-sync-blocked", Subject: ServiceSubject, Name: a.Name, Message: "The applied dependency is missing: " + link.From})
			continue
		}
		cfg := dep.Service
		if dep.Resource != nil {
			cfg = &ServiceConfig{Target: dep.Resource.Target, TargetSettings: dep.Resource.TargetSettings, Region: dep.Region}
		}
		reachable := cfg != nil && cfg.Public
		if cfg != nil && !reachable {
			if OwnedCloud(p.cloud(a.Provider)) || OwnedCloud(p.cloud(dep.Provider)) {
				reachable = sameTargetNetwork(a.Service, cfg, p.cloud(a.Provider), p.cloud(dep.Provider))
			} else {
				reachable = a.ConnectionID == dep.ConnectionID && a.Region == dep.Region && (dep.Resource != nil || a.Provider == dep.Provider)
			}
		}
		if !reachable {
			p.problem(Finding{Code: "cloud-sync-blocked", Subject: ServiceSubject, Name: a.Name, Message: fmt.Sprintf("The applied dependency %s needs a verified network path in the destination.", link.From)})
		}
	}
}
