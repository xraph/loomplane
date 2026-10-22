package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// AppliedCloudPlan constructs migration actions from serving bindings only.
// It never reads a current analysis, manifest, branch or environment variable.
func AppliedCloudPlan(bindings []*Binding, assignment *cloudprofiles.Assignment, targetID string, options []ConnectionOption, registry *provider.Registry, _ ...*cloudprofiles.Assignment) *Result {
	return appliedCloudPlan(bindings, assignment, targetID, options, registry, nil)
}

func appliedCloudPlan(bindings []*Binding, assignment *cloudprofiles.Assignment, targetID string, options []ConnectionOption, registry *provider.Registry, pools map[string]CloudPlacement, routes ...[]cloudinfra.Route) *Result {
	p := &planner{in: Input{Providers: registry, Connections: options, CloudPlacements: map[string]CloudPlacement{}, Chosen: map[string]string{}}, services: map[string]*Action{}, resources: map[string]*Action{}, bindings: map[string]*Binding{}, clouds: map[string]bool{}, unready: map[string]string{}, held: map[string]string{}, removing: map[string]bool{}}
	if len(routes) > 0 {
		p.in.Routes = routes[0]
	}
	blocked := func(name, message string) {
		p.problem(Finding{Code: "cloud-sync-blocked", Subject: ServiceSubject, Name: name, Message: message})
	}
	for _, b := range bindings {
		p.bindings[actionID(b.Subject, b.Name)] = b
		if b.Subject != ServiceSubject {
			continue
		}
		var cfg ServiceConfig
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			blocked(b.Name, "The applied service configuration cannot be read.")
			continue
		}
		a := &Action{ID: actionID(b.Subject, b.Name), Subject: b.Subject, Name: b.Name, Provider: b.Provider, ConnectionID: b.ConnectionID, Region: b.Region, Engine: b.Engine, Op: Keep, Service: &cfg, Reason: "Keep the applied service."}
		p.actions = append(p.actions, a)
		p.services[b.Name] = a
		pin := cfg.CloudPlacement
		if pin == nil || targetID != "" && pin.TargetID != targetID {
			continue
		}
		if assignment == nil {
			blocked(b.Name, "This deployed service needs a named Cloud target; remove or adopt its reference before unassigning the Cloud.")
			continue
		}
		var t cloudprofiles.Target
		if pin.Default {
			d := assignment.DefaultPlacement
			if d == nil {
				blocked(b.Name, "The proposed Cloud has no default placement this project may use.")
				continue
			}
			t = cloudprofiles.Target{Runtime: "managed", Provider: d.Provider, ConnectionID: d.ConnectionID, Region: d.Region}
		} else {
			ti := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.Name == pin.TargetName })
			if ti < 0 {
				blocked(b.Name, "The proposed Cloud is missing target "+pin.TargetName+".")
				continue
			}
			t = assignment.Targets[ti]
		}
		if t.PoolID != "" {
			placement, found := pools[b.Name]
			if !found || placement.PoolID != t.PoolID {
				blocked(b.Name, "Pool placement needs a reviewed capacity selection.")
				continue
			}
			t.ConnectionID = placement.ConnectionID
		}
		ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == t.ConnectionID })
		prov, err := registry.Get(t.Provider)
		if err != nil {
			blocked(b.Name, err.Error())
			continue
		}
		if ci < 0 || !options[ci].Ready || options[ci].Cloud != prov.Cloud {
			blocked(b.Name, "The exact target connection must be ready for this provider.")
			continue
		}
		c := options[ci]
		identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
		if identity == "" {
			blocked(b.Name, "The target account identity must be verified.")
			continue
		}
		p.use(prov)
		runs, ok := prov.Running(cfg.Workload)
		if !ok || !prov.Available {
			blocked(b.Name, "The destination cannot run the applied workload.")
			continue
		}
		region := t.Region
		// Historical bindings without provenance keep their applied region.
		// Only an explicitly recorded default follows the target's region.
		if pin.RegionSource != "default" {
			region = cfg.Region
		}
		if region == "" {
			region = prov.DefaultRegion()
		}
		if prov.Placed() && !prov.HasRegion(region) {
			blocked(b.Name, "The destination does not support the applied region.")
		}
		if !prov.Scaling && prov.Cloud != "server" && (cfg.Scale != nil || cfg.Size != nil) {
			blocked(b.Name, "The destination cannot preserve the applied scale and size settings.")
		}
		cfg.Provider, cfg.Region, cfg.Execution = prov.Name, region, runs.Execution
		cfg.CloudPlacement = &CloudPlacement{Default: pin.Default, GrantRevision: assignment.GrantRevision, CloudID: assignment.CloudID, CloudRevision: assignment.CloudRevision, AssignmentRevision: assignment.Revision, TargetID: t.ID, TargetRevision: t.Revision, TargetName: t.Name, Provider: t.Provider, ConnectionID: t.ConnectionID, AccountIdentity: identity, Region: region, RegionSource: pin.RegionSource}
		if placement, found := pools[b.Name]; found && t.PoolID != "" {
			cfg.CloudPlacement = &placement
		}
		p.in.CloudPlacements[b.Name] = *cfg.CloudPlacement
		set := manifest.Settings{Provider: cfg.Provider, Region: cfg.Region, Endpoint: cfg.Endpoint, HealthPath: cfg.HealthPath, Scale: cfg.Scale, Size: cfg.Size}
		if OwnedCloud(prov.Cloud) {
			set.Target = t.ConnectionID
		}
		if cfg.BuildTarget != nil {
			set.Build = &manifest.Build{Builder: cfg.BuildTarget.Builder, Registry: cfg.BuildTarget.Registry, Image: cfg.BuildTarget.Image, Context: cfg.Context}
		}
		cfg.Target = ""
		p.configureTarget(b.Name, "services."+b.Name, prov, set, &cfg)
		p.checkRender(b.Name, "services."+b.Name, &cfg)
		p.checkAzure(b.Name, "services."+b.Name, &cfg)
		a.Provider, a.Region, a.ConnectionID, a.Engine, a.Creates = prov.Name, region, t.ConnectionID, prov.Engine(runs), runs.Creates
		if b.Provider != a.Provider || b.ConnectionID != a.ConnectionID || b.Region != a.Region {
			a.Op = Replace
			a.Reason = "Recreate the applied service on the reviewed target."
		} else {
			a.AdoptCloudPlacement = true
			a.CloudAdoption = ReviewCloudAdoption(b)
			a.Reason = "Adopt the reviewed Cloud placement."
		}
	}
	p.routedReplacements()
	// Follow the applied dependency graph transitively, including generated gateways.
	for changed := true; changed; {
		changed = p.routedReplacements()
		for _, a := range p.actions {
			for i, link := range a.Service.Env {
				if link.Subject == ResourceSubject {
					var resource *Binding
					for _, b := range bindings {
						if b.Subject == ResourceSubject && b.Name == link.From {
							resource = b
						}
					}
					if a.Op != Keep && (resource == nil || resource.ConnectionID != a.ConnectionID) {
						blocked(a.Name, "The applied database or storage dependency needs a verified network path in the destination; automatic data movement is unavailable.")
					}
					continue
				}
				dep := p.services[link.From]
				if dep == nil {
					blocked(a.Name, "An applied managed dependency is missing: "+link.From)
					continue
				}
				a.DependsOn = appendUnique(a.DependsOn, dep.ID)
				if !dep.Service.Public && (a.ConnectionID != dep.ConnectionID || a.Region != dep.Region || a.Provider != dep.Provider) {
					blocked(a.Name, "The private dependency "+dep.Name+" would cross an unverified account or network boundary.")
				}
				if dep.Op == Replace || dep.Op == Update {
					if a.Op == Keep {
						a.Op = Update
						a.AdoptCloudPlacement = false
						a.CloudAdoption = nil
						a.Reason = "Refresh the applied dependency endpoints."
						changed = true
					}
					if dep.Op == Replace && (dep.Provider != ecsFargate || a.Provider != ecsFargate || a.ConnectionID != dep.ConnectionID) {
						a.Service.Env[i].Address = ""
					}
				}
			}
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var order func(*Action) int
	order = func(a *Action) int {
		if done[a.ID] {
			return a.Step
		}
		if visiting[a.ID] {
			blocked(a.Name, "The applied dependency graph contains a cycle.")
			return 0
		}
		visiting[a.ID] = true
		step := 0
		for _, id := range a.DependsOn {
			for _, dep := range p.actions {
				if dep.ID == id {
					step = max(step, order(dep)+1)
				}
			}
		}
		visiting[a.ID] = false
		done[a.ID] = true
		a.Step = step
		return step
	}
	for _, a := range p.actions {
		order(a)
		if a.Op == Keep {
			continue
		}
		b := p.bindings[a.ID]
		if b.Drift != "" {
			blocked(a.Name, "Resolve the serving resource's drift before migration.")
		}
		if a.Service.Gateway != nil {
			continue
		}
		inputs := a.Service.AppliedInputs
		if inputs == nil || inputs.Variables == "" {
			blocked(a.Name, "The applied variable snapshot is unavailable; deploy once with snapshot capture before switching.")
		}
		if inputs == nil || inputs.Source == nil || inputs.ArtifactKey == "" || inputs.Source.CommitSHA != b.CommitSHA || inputs.Source.CommitSHA != a.Service.CommitSHA || inputs.Source.Alias != a.Service.Repo {
			blocked(a.Name, "The exact applied source artifact is unavailable; migration cannot use a newer analysis or branch head.")
		}
	}
	p.checkTargetPlacements()
	// Reuse the planner's account, region and HTTPS checks for these ordinary actions.
	result := p.finish()
	return result
}

func appendUnique(values []string, value string) []string {
	if !slices.Contains(values, value) {
		values = append(values, value)
	}
	return values
}

func snapshotConnections(bindings []*Binding, plan *Plan) []string {
	refs := map[string]bool{}
	for _, b := range bindings {
		refs[b.ConnectionID] = true
		for _, old := range b.Replaced {
			refs[old.ConnectionID] = true
		}
	}
	for _, a := range plan.Actions {
		refs[a.ConnectionID] = true
		if a.Service != nil && a.Service.BuildTarget != nil {
			refs[a.Service.BuildTarget.Builder] = true
			refs[a.Service.BuildTarget.Registry] = true
		}
	}
	delete(refs, "")
	return slices.Sorted(maps.Keys(refs))
}

func migrationPlanID(syncID, environmentID string) string {
	return fmt.Sprintf("%s.%s", syncID, environmentID)
}
