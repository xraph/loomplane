package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/recipes"
)

// ReleaseRoutes returns the exact route review used by a release plan. A
// route whose handoff a release still holds blocks it, unless the handoff
// is one plan allowHeldBy prepared: a rollback of that plan may plan while
// it holds them. "" allows none.
func (s *PgStore) ReleaseRoutes(ctx context.Context, org, project, environment, allowHeldBy string) ([]cloudinfra.Route, error) {
	routes, err := cloudinfra.NewPgStore(s.db).Routes(ctx, org, project)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	if allowHeldBy != "" {
		held, getErr := s.GetPlan(ctx, org, allowHeldBy)
		if getErr != nil {
			return nil, fmt.Errorf("read the plan of the release to roll back: %w", getErr)
		}
		allowed = heldBy(held)
	}
	out := []cloudinfra.Route{}
	for _, route := range routes {
		if route.Input.EnvironmentID != environment || route.Status == "removed" {
			continue
		}
		var handoff string
		if checkErr := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released LIMIT 1), '')`, org, route.ID).Scan(&handoff); checkErr != nil {
			return nil, checkErr
		}
		if handoff != "" && !allowed[handoff] {
			return nil, fmt.Errorf("finish the retained route handoff for %s before another release", route.Input.Hostname)
		}
		out = append(out, *route)
	}
	return out, nil
}

// heldBy returns the route handoffs plan's routed releases prepare, by ID.
func heldBy(plan *Plan) map[string]bool {
	out := map[string]bool{}
	for _, a := range plan.Actions {
		for _, r := range a.Routes {
			out[cloudinfra.ReleasePrepareID(plan.ID+"/"+a.ID, r.ID)] = true
		}
	}
	return out
}

func (s *Service) releaseRoutes(ctx context.Context, plan *Plan) ([]cloudinfra.Route, error) {
	reader, ok := s.store.(interface {
		ReleaseRoutes(ctx context.Context, org, project, environment, allowHeldBy string) ([]cloudinfra.Route, error)
	})
	if !ok {
		return nil, nil
	}
	// A rollback waits for the handoffs the release it undoes holds, like
	// any plan, until a rollback can restore a held route release; it
	// passes Rollback.TargetPlanID then.
	return reader.ReleaseRoutes(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID, "")
}

// ProjectZones returns the verified DNS zones granted to a project, which
// the database gateway names its hosts in.
func (s *PgStore) ProjectZones(ctx context.Context, org, project string) ([]cloudinfra.ProjectZone, error) {
	zones, err := cloudinfra.NewPgStore(s.db).Zones(ctx, org)
	if err != nil {
		return nil, err
	}
	return cloudinfra.VerifiedProjectZones(zones, project), nil
}

func (s *Service) projectZones(ctx context.Context, plan *Plan) ([]cloudinfra.ProjectZone, error) {
	reader, ok := s.store.(interface {
		ProjectZones(context.Context, string, string) ([]cloudinfra.ProjectZone, error)
	})
	if !ok {
		return nil, nil
	}
	return reader.ProjectZones(ctx, plan.OrgID, plan.ProjectID)
}

// routedReplacements runs before dependency readdressing. A routed update must
// keep its source instance until every consumer and route has moved. A
// routed composed component is released the same way as a service.
func (p *planner) routedReplacements() bool {
	changed := false
	for _, route := range p.in.Routes {
		a := p.routedTarget(route.Input.Service)
		if a == nil || a.Op == Keep || a.Op == Delete || slices.ContainsFunc(a.Routes, func(r cloudinfra.Route) bool { return r.ID == route.ID }) {
			continue
		}
		// A Kubernetes route reaches its Service by name, so an update in
		// place keeps serving it with no handoff. A new runtime would leave
		// the route on a Service that goes away.
		if route.Binding.BackendName() == cloudinfra.RouteBackendKubernetes || route.Binding.BackendName() == cloudinfra.RouteBackendFirecracker {
			if a.Op != Update {
				p.problem(routeRelease(a, fmt.Sprintf("%s would get a new runtime, which %s doesn't follow on this connection. Remove that route, then plan again.", a.Name, route.Input.Hostname)))
			}
			continue
		}
		// An App Platform route reaches the service's app by its address, and
		// an update in place keeps that app and its domains. A new app would
		// leave the route on one that goes away.
		if route.Binding.BackendName() == cloudinfra.RouteBackendPlatform {
			if a.Op != Update {
				p.problem(routeRelease(a, fmt.Sprintf("%s would get a new App Platform app, which %s doesn't follow. Remove that route, then plan again.", a.Name, route.Input.Hostname)))
			}
			continue
		}
		a.Routes = append(a.Routes, route)
		b := p.bindings[a.ID]
		allowed, reason := routedReplacementAllowed, "Retain the serving instance until the reviewed routes and consumers move."
		if a.Subject == ResourceSubject {
			allowed, reason = componentReplacementAllowed, "Retain the serving instance until its routes move."
		}
		if err := allowed(a, b, route); err != nil {
			p.problem(routeRelease(a, err.Error()))
			continue
		}
		a.Op, a.Reason = Replace, reason
		changed = true
	}
	return changed
}

// routedTarget is the action a route names: a service, or else a composed
// resource's process component, which is the only resource that takes a
// route (see routableComponentOn in cloudinfra).
func (p *planner) routedTarget(name string) *Action {
	if a := p.services[name]; a != nil {
		return a
	}
	a := p.resources[name]
	if a == nil || a.Resource == nil || a.Resource.Component == nil || a.Resource.Component.Component.Role != recipes.RoleProcess {
		return nil
	}
	return a
}

// routeRelease is a route-release problem on what a routed release is
// for: a service, or the composed resource a component is part of.
func routeRelease(a *Action, message string) Finding {
	f := Finding{Code: "route-release", Subject: a.Subject, Name: a.Name, Message: message}
	if a.Subject == ResourceSubject && a.Resource != nil && a.Resource.Component != nil {
		f.Path = "resources." + a.Resource.Component.Resource
	}
	return f
}

// componentReplacementAllowed is routedReplacementAllowed for a composed
// component. A component is never public and has no host port, so the
// overlap can't collide on one. It refuses one whose new instance would
// start without the old one's data, or would need a second set of GPUs on
// the host.
func componentReplacementAllowed(a *Action, b *Binding, route cloudinfra.Route) error {
	if _, err := cloudinfra.ReviewedRouteDrain(route, 60); err != nil {
		return err
	}
	if b == nil || a.Resource == nil || a.Resource.Component == nil || a.Provider != "server" || b.Provider != "server" {
		return fmt.Errorf("%s needs a reviewed server destination for its route", a.Name)
	}
	if route.Status != "ready" || route.Binding.InstanceID != b.ExternalID || route.Binding.BindingID != b.ID || len(b.Replaced) > 0 {
		return fmt.Errorf("finish the current route or retained instance cleanup for %s before another release", a.Name)
	}
	c := a.Resource.Component
	if c.Component.Volume != nil || c.Component.WeightCache != nil {
		return fmt.Errorf("%s keeps local data, so a separate instance would start empty. Remove its routes, then plan again", a.Name)
	}
	if c.GPU != nil {
		return fmt.Errorf("%s uses GPUs, and two instances would need two sets on one host. Remove its routes, then plan again", a.Name)
	}
	var old ResourceConfig
	if err := json.Unmarshal(b.Config, &old); err != nil {
		return err
	}
	return routedPlacementAllowed(a.Name, old.CloudPlacement, a.Resource.CloudPlacement)
}

// planRouteRemovals gives each delete of something a domain route points
// at those routes to remove first, and says so by hostname: a service, or
// a composed resource's component, deleted alone or with the resource it's
// part of. The runtime can't go while a route names it
// (ValidateRuntimeRemoval), so the delete removes them before it.
func (p *planner) planRouteRemovals() {
	if len(p.in.Routes) == 0 {
		return
	}
	composed := p.composedBindings()
	for _, a := range p.actions {
		if a.Op != Delete {
			continue
		}
		for _, r := range p.in.Routes {
			if r.Input.Service == a.Name {
				a.RemovesRoutes = append(a.RemovesRoutes, r)
			}
		}
		if len(a.RemovesRoutes) == 0 {
			continue
		}
		path := "services." + a.Name
		if a.Subject == ResourceSubject {
			path = "resources." + a.Name
			if of, part := composed[a.ID]; part {
				path = "resources." + of.parent
			}
		}
		p.warn(Finding{Code: "route-removal", Subject: a.Subject, Name: a.Name, Path: path, Message: routeRemovalMessage(a.Name, a.RemovesRoutes)})
	}
}

// routeRemovalMessage says which hostnames deleting name removes.
func routeRemovalMessage(name string, routes []cloudinfra.Route) string {
	hosts := make([]string, 0, len(routes))
	for _, r := range routes {
		hosts = append(hosts, r.Input.Hostname)
	}
	if len(hosts) == 1 {
		return fmt.Sprintf("Deleting %s removes %s and its DNS record.", name, hosts[0])
	}
	return fmt.Sprintf("Deleting %s removes %s and %s and their DNS records.", name, strings.Join(hosts[:len(hosts)-1], ", "), hosts[len(hosts)-1])
}

func routedReplacementAllowed(a *Action, b *Binding, route cloudinfra.Route) error {
	if _, err := cloudinfra.ReviewedRouteDrain(route, 60); err != nil {
		return err
	}
	if b == nil || a.Service == nil || a.Provider != "server" || b.Provider != "server" {
		return fmt.Errorf("%s needs a reviewed server destination for its route", a.Name)
	}
	if route.Status != "ready" || route.Binding.InstanceID != b.ExternalID || route.Binding.BindingID != b.ID || len(b.Replaced) > 0 {
		return fmt.Errorf("finish the current route or retained instance cleanup for %s before another release", a.Name)
	}
	var old ServiceConfig
	if err := json.Unmarshal(b.Config, &old); err != nil {
		return err
	}
	if err := routedPlacementAllowed(a.Name, old.CloudPlacement, a.Service.CloudPlacement); err != nil {
		return err
	}
	if old.Catalog != nil || a.Service.Catalog != nil {
		return fmt.Errorf("%s has local data; review data migration before retaining a separate candidate", a.Name)
	}
	sameHost := old.TargetSettings["docker_id"] != "" && old.TargetSettings["docker_id"] == a.Service.TargetSettings["docker_id"]
	if old.CloudPlacement != nil && a.Service.CloudPlacement != nil {
		sameHost = sameHost || (old.CloudPlacement.ServerID != "" && old.CloudPlacement.ServerID == a.Service.CloudPlacement.ServerID)
	}
	if sameHost && old.Public && a.Service.Public && old.Endpoint != nil && a.Service.Endpoint != nil && old.Endpoint.HostPort == a.Service.Endpoint.HostPort {
		return fmt.Errorf("%s cannot retain two instances on host port %d; review a move to another server, a distinct port, or an ingress-only private manifest", a.Name, old.Endpoint.HostPort)
	}
	return nil
}

// routedPlacementAllowed refuses a routed release whose new instance has
// nowhere its routes can follow: no reviewed server destination, a direct
// target that runs no single Cloud server, or a move from a pool onto a
// direct target, which holds no reservation for the overlap. A pool
// candidate gets its reservation when the plan is created
// (reserveRoutedPlacements); a direct target needs none.
func routedPlacementAllowed(name string, old, next *CloudPlacement) error {
	switch {
	case next == nil:
		return fmt.Errorf("%s needs a reviewed server destination for its route", name)
	case next.PoolID != "":
		return nil
	case next.ServerID == "":
		return fmt.Errorf("%s's target runs no single Cloud server, so its routes can't follow a new instance", name)
	case old != nil && old.PoolID != "":
		return fmt.Errorf("%s moves from a server pool to a direct target. Remove its routes, release it, then add them again", name)
	}
	return nil
}

type routePlacementReview struct {
	Prior  []cloudinfra.PlannedPlacement
	Before *cloudprofiles.Target
}

func (s *Service) reserveRoutedPlacements(ctx context.Context, plan *Plan, result *Result, bindings []*Binding, reviews ...routePlacementReview) error {
	resolver, ok := s.store.(poolResolver)
	var review routePlacementReview
	if len(reviews) > 0 {
		review = reviews[0]
	}
	planned := slices.Clone(review.Prior)
	for i := range result.Actions {
		a := &result.Actions[i]
		if a.Service == nil || a.Op == Keep || a.Op == Delete {
			continue
		}
		pin := a.Service.CloudPlacement
		if pin == nil || pin.PoolID == "" {
			continue
		}
		if len(a.Routes) > 0 {
			if !ok {
				return fmt.Errorf("routed overlap capacity resolver is unavailable")
			}
			req := cloudinfra.PlacementRequest{PlanID: plan.ID, Allocation: plan.ID, Planned: planned, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: pin.CloudID, TargetID: pin.TargetID, TargetRevision: pin.TargetRevision, PoolID: pin.PoolID, Service: a.Name, CPUMillis: pin.CPUMillis, MemoryBytes: pin.MemoryBytes, GPUKind: pin.GPUKind, GPUs: pin.GPUs, Replicas: 1, DestinationServerID: pin.ServerID}
			for _, b := range bindings {
				if b.Subject == ServiceSubject && b.Name == a.Name {
					var old ServiceConfig
					if err := json.Unmarshal(b.Config, &old); err != nil {
						return err
					}
					if old.CloudPlacement != nil && old.CloudPlacement.PoolID != "" {
						req.StickyServerID = old.CloudPlacement.ServerID
					}
				}
			}
			if review.Before != nil && review.Before.ID == pin.TargetID {
				req.TargetChange = true
				req.TargetRevision = review.Before.Revision
			}
			allocation, err := resolver.PreviewPlacement(ctx, req)
			if err != nil {
				return fmt.Errorf("%s retained overlap: %w", a.Name, err)
			}
			pin.Allocation = allocation.Allocation
		}
		planned = append(planned, cloudinfra.PlannedPlacement{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Service: a.Name, Placement: pin.PoolPlacement()})
	}
	return nil
}

func (s *PgStore) CurrentReleaseRoutes(ctx context.Context, org, project, environment, service string) ([]cloudinfra.Route, error) {
	routes, err := cloudinfra.NewPgStore(s.db).Routes(ctx, org, project)
	if err != nil {
		return nil, err
	}
	var out []cloudinfra.Route
	for _, r := range routes {
		// Releases only carry server routes; Kubernetes and App Platform
		// routes stay with their service. See routedReplacements.
		if r.Input.EnvironmentID == environment && r.Input.Service == service && r.Status != "removed" && r.Binding.BackendName() == cloudinfra.RouteBackendServer {
			out = append(out, *r)
		}
	}
	return out, nil
}
