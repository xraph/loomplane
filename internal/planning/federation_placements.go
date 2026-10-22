package planning

import (
	"slices"
	"strconv"

	"github.com/xraph/loomplane/internal/graph"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// reviewFederationPlacements compares service intent with verified destination
// records. Its policy actions describe prerequisites, not deployable workloads.
func reviewFederationPlacements(in FederationInput, destinations map[string]*domain.Destination, changes []Change) ([]Action, []Finding) {
	var actions []Action
	var findings []Finding
	if in.Manifest == nil || in.Manifest.Federation == nil {
		return actions, findings
	}
	intent := in.Manifest.Federation
	sourceGraph := in.Graph
	if sourceGraph == nil {
		sourceGraph = &graph.Graph{}
	}
	sourcePlanner := &planner{in: Input{Graph: sourceGraph, Held: in.Held}}
	bindings := map[string]*Binding{}
	for _, binding := range in.Bindings {
		if binding != nil && binding.Subject == ServiceSubject {
			bindings[binding.Name] = binding
		}
	}
	for _, name := range federationKeys(intent.Placements) {
		placement := intent.Placements[name]
		path := "federation.placements." + name
		fail := func(code, message string) {
			findings = append(findings, Finding{Code: code, Subject: PolicySubject, Name: "placement:" + name, Path: path, Message: message})
		}
		svc, ok := in.Manifest.Services[placement.Service]
		if !ok {
			fail("federation-placement-service", "The placement must name an existing service.")
			continue
		}
		set := svc.For(in.Environment, in.Manifest.Defaults)
		if !manifest.On(set.Enabled) {
			fail("federation-placement-disabled", "This service is disabled in the selected environment.")
		}
		_, held := sourcePlanner.heldRepo(ServiceConfig{Repo: svc.Repo})
		_, deployedHeld := sourcePlanner.heldBinding(bindings[placement.Service])
		held = held || deployedHeld
		if held {
			fail("federation-placement-held", "This placement includes a held or unidentified repository. Review it in a plan that includes its source before changing federation.")
		}
		d := destinations[placement.Destination]
		if d == nil {
			// Destination findings already explain missing scope or identity.
			continue
		}
		if in.Providers == nil {
			fail("federation-placement-provider", "The service provider has not been verified against this destination.")
		} else if prov, err := in.Providers.Get(set.Provider); err != nil || prov.Cloud != d.Identity.Provider || !prov.Available {
			fail("federation-placement-provider", "The service needs an available provider for this destination's cloud.")
		}
		if set.CloudTarget != "" || len(set.CloudTags) > 0 {
			fail("federation-placement-cloud", "Resolve the assigned Cloud target before reviewing this placement.")
		}
		cloud, hasCloud := in.CloudPlacements[placement.Service]
		if d.ConnectionID == "" || (set.Target != "" && set.Target != d.ConnectionID) || (hasCloud && cloud.ConnectionID != d.ConnectionID) {
			fail("federation-placement-target", "The service target and federation destination must use the same checked connection.")
		}
		placementChanges := slices.Clone(changes)
		placementChanges = append(placementChanges,
			Change{Field: "service", To: placement.Service}, Change{Field: "source", To: svc.Source}, Change{Field: "repository", To: svc.Repo},
			Change{Field: "provider", To: set.Provider}, Change{Field: "destination_id", To: d.ID}, Change{Field: "destination_identity", To: d.Identity.Revision()},
			Change{Field: "destination_generation", To: strconv.FormatInt(d.Generation, 10)})
		actions = append(actions, Action{ID: "policy:placement:" + name, Subject: PolicySubject, Name: "placement:" + name, Op: Keep, Provider: "federation", Engine: provider.Loomplane, ConnectionID: d.ConnectionID, Region: set.Region, Changes: placementChanges, Reason: "Review the service and destination before installing federation policy.", DependsOn: []string{"policy:destination:" + placement.Destination}})
	}
	for _, name := range federationKeys(intent.ServiceExports) {
		export := intent.ServiceExports[name]
		svc, ok := in.Manifest.Services[export.Service]
		if !ok {
			continue
		}
		set := svc.For(in.Environment, in.Manifest.Defaults)
		path := "federation.service_exports." + name
		if set.Public == nil || *set.Public {
			findings = append(findings, Finding{Code: "federation-export-public", Subject: PolicySubject, Name: "export:" + name, Path: path, Message: "Set public: false explicitly for an exported service in this environment."})
		}
		if set.Port == 0 || set.Port != export.Port {
			findings = append(findings, Finding{Code: "federation-export-port", Subject: PolicySubject, Name: "export:" + name, Path: path, Message: "Set the service port explicitly to match the exported port before reviewing access."})
		}
	}
	return actions, findings
}

func federationPlacementDependencies(intent *manifest.Federation, service, destination string, prerequisites ...string) []string {
	ids := make([]string, 0, len(intent.Placements)+len(prerequisites))
	ids = append(ids, prerequisites...)
	for _, name := range federationKeys(intent.Placements) {
		p := intent.Placements[name]
		if p.Service == service && p.Destination == destination {
			ids = append(ids, "policy:placement:"+name)
		}
	}
	return ids
}
