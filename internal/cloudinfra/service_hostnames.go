package cloudinfra

import (
	"context"
	"slices"
	"strings"
)

type ServiceHostname struct {
	RouteID  string `json:"route_id"`
	ZoneID   string `json:"zone_id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

// ServiceHostnames lists every route of one service that isn't removed, exact
// and wildcard alike, sorted by hostname. It checks no principal: it is for
// Loomplane's own background jobs and is never served over HTTP.
func (s *Service) ServiceHostnames(ctx context.Context, orgID, projectID, environmentID, service string) ([]ServiceHostname, error) {
	if orgID == "" || projectID == "" || environmentID == "" || service == "" {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	routes, err := st.ServiceRoutes(ctx, orgID, projectID, environmentID, service)
	if err != nil {
		return nil, err
	}
	zones, err := st.Zones(ctx, orgID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Zone{}
	for _, zone := range zones {
		byID[zone.ID] = zone
	}
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	latest, err := st.LatestRouteOperations(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := make([]ServiceHostname, 0, len(routes))
	for _, route := range routes {
		status := DeriveRouteStatus(*route, byID[route.Input.ZoneID], latest[route.ID], now)
		out = append(out, ServiceHostname{RouteID: route.ID, ZoneID: route.Input.ZoneID, Hostname: route.Input.Hostname, Status: string(status.Route)})
	}
	slices.SortFunc(out, func(a, b ServiceHostname) int { return strings.Compare(a.Hostname, b.Hostname) })
	return out, nil
}
