package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// ReleaseRoutes returns the exact route review used by a release plan.
func (s *PgStore) ReleaseRoutes(ctx context.Context, org, project, environment string) ([]cloudinfra.Route, error) {
	routes, err := cloudinfra.NewPgStore(s.db).Routes(ctx, org, project)
	if err != nil {
		return nil, err
	}
	out := []cloudinfra.Route{}
	for _, route := range routes {
		if route.Input.EnvironmentID != environment || route.Status == "removed" {
			continue
		}
		var held bool
		if checkErr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released)`, org, route.ID).Scan(&held); checkErr != nil {
			return nil, checkErr
		}
		if held {
			return nil, fmt.Errorf("finish the retained route handoff for %s before another release", route.Input.Hostname)
		}
		out = append(out, *route)
	}
	return out, nil
}

func (s *Service) releaseRoutes(ctx context.Context, plan *Plan) ([]cloudinfra.Route, error) {
	reader, ok := s.store.(interface {
		ReleaseRoutes(context.Context, string, string, string) ([]cloudinfra.Route, error)
	})
	if !ok {
		return nil, nil
	}
	return reader.ReleaseRoutes(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID)
}

// ProjectZones returns the verified DNS zones granted to a project, which
// the database gateway names its hosts in.
func (s *PgStore) ProjectZones(ctx context.Context, org, project string) ([]cloudinfra.ProjectZone, error) {
	zones, err := cloudinfra.NewPgStore(s.db).Zones(ctx, org)
	if err != nil {
		return nil, err
	}
	out := []cloudinfra.ProjectZone{}
	for _, z := range zones {
		if slices.Contains(z.ProjectIDs, project) && !z.VerifiedAt.IsZero() {
			out = append(out, cloudinfra.ProjectZone{ID: z.ID, Name: z.Name, Mode: z.Mode, Verified: true})
		}
	}
	return out, nil
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
// keep its source instance until every consumer and route has moved.
func (p *planner) routedReplacements() bool {
	changed := false
	for _, route := range p.in.Routes {
		a := p.services[route.Input.Service]
		if a == nil || a.Op == Keep || a.Op == Delete || slices.ContainsFunc(a.Routes, func(r cloudinfra.Route) bool { return r.ID == route.ID }) {
			continue
		}
		a.Routes = append(a.Routes, route)
		b := p.bindings[a.ID]
		if err := routedReplacementAllowed(a, b, route); err != nil {
			p.problem(Finding{Code: "route-release", Subject: ServiceSubject, Name: a.Name, Message: err.Error()})
			continue
		}
		a.Op, a.Reason = Replace, "Retain the serving instance until the reviewed routes and consumers move."
		changed = true
	}
	return changed
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
		if r.Input.EnvironmentID == environment && r.Input.Service == service && r.Status != "removed" {
			out = append(out, *r)
		}
	}
	return out, nil
}
