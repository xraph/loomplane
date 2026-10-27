package cloudinfra

import (
	"context"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// HostnameUse is one route under a zone, with where it runs and its states.
type HostnameUse struct {
	RouteID       string      `json:"route_id"`
	Hostname      string      `json:"hostname"`
	ProjectID     string      `json:"project_id"`
	ProjectName   string      `json:"project_name"`
	EnvironmentID string      `json:"environment_id"`
	Service       string      `json:"service"`
	Port          int         `json:"port"`
	RunsOn        string      `json:"runs_on"`
	Status        RouteStatus `json:"status"`
}

type ProjectDomain struct {
	ZoneID    string        `json:"zone_id"`
	Name      string        `json:"name"`
	Mode      string        `json:"mode"`
	Provider  string        `json:"provider,omitempty"`
	Verified  bool          `json:"verified"`
	Wildcards bool          `json:"wildcards"`
	Hostnames []HostnameUse `json:"hostnames"`
}

type ZoneOverview struct {
	Zone           *Zone         `json:"zone"`
	State          DomainState   `json:"state"`
	Wildcards      bool          `json:"wildcards"`
	RegistrationID string        `json:"registration_id,omitempty"`
	Hostnames      []HostnameUse `json:"hostnames"`
}

type DomainOverview struct {
	Zones         []ZoneOverview  `json:"zones"`
	Registrations []*Registration `json:"registrations"`
}

type ProviderZoneList struct {
	Zones []provider.DNSZone `json:"zones"`
}

// hostnameUses builds each route's row once, keyed by zone ID.
func (s *Service) hostnameUses(ctx context.Context, st DomainStore, org string, zones []*Zone, routes []*Route) (map[string][]HostnameUse, error) {
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	latest, err := st.LatestRouteOperations(ctx, org, ids)
	if err != nil {
		return nil, err
	}
	projects, err := st.ProjectNames(ctx, org)
	if err != nil {
		return nil, err
	}
	runtimes, err := st.RuntimeNames(ctx, org)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Zone{}
	for _, zone := range zones {
		byID[zone.ID] = zone
	}
	now := s.now().UTC()
	out := map[string][]HostnameUse{}
	for _, route := range routes {
		// Server routes run on the server; Kubernetes routes on the cluster's connection.
		runtime := route.Binding.ServerID
		if route.Binding.BackendName() == RouteBackendKubernetes {
			runtime = route.Binding.ConnectionID
		}
		runsOn := runtimes[runtime]
		if runsOn == "" {
			runsOn = runtime
		}
		out[route.Input.ZoneID] = append(out[route.Input.ZoneID], HostnameUse{RouteID: route.ID, Hostname: route.Input.Hostname, ProjectID: route.ProjectID, ProjectName: projects[route.ProjectID],
			EnvironmentID: route.Input.EnvironmentID, Service: route.Input.Service, Port: route.Input.Port, RunsOn: runsOn, Status: DeriveRouteStatus(*route, byID[route.Input.ZoneID], latest[route.ID], now)})
	}
	return out, nil
}

func zoneState(zone *Zone) DomainState {
	if !zone.verified() {
		return DomainAwaitingVerification
	}
	return DomainReady
}

// DomainOverview is the organization's Domains page: every zone and the
// hostnames every project has under it.
func (s *Service) DomainOverview(ctx context.Context, p security.Principal) (*DomainOverview, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	LinkZoneParents(zones)
	routes, err := st.OrgRoutes(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	uses, err := s.hostnameUses(ctx, st, p.OrgID, zones, routes)
	if err != nil {
		return nil, err
	}
	out := &DomainOverview{Zones: make([]ZoneOverview, 0, len(zones)), Registrations: []*Registration{}}
	for _, zone := range zones {
		out.Zones = append(out.Zones, ZoneOverview{Zone: zone, State: zoneState(zone), Wildcards: zone.Wildcards(), RegistrationID: zone.RegistrationID, Hostnames: append([]HostnameUse{}, uses[zone.ID]...)})
	}
	// Without a registration store nothing can have been registered.
	if registrations, storeErr := s.registrationStore(); storeErr == nil {
		regs, listErr := registrations.Registrations(ctx, p.OrgID)
		if listErr != nil {
			return nil, listErr
		}
		for _, reg := range regs {
			out.Registrations = append(out.Registrations, shown(reg))
		}
	}
	return out, nil
}

// ProjectDomains lists the zones granted to a project with that project's
// own hostnames under each.
func (s *Service) ProjectDomains(ctx context.Context, p security.Principal, project string) ([]ProjectDomain, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	// Linked before the filter: a subdomain's domain may not be granted.
	LinkZoneParents(zones)
	zones = slices.DeleteFunc(zones, func(zone *Zone) bool { return !slices.Contains(zone.ProjectIDs, project) })
	routes, err := st.Routes(ctx, p.OrgID, project)
	if err != nil {
		return nil, err
	}
	routes = slices.DeleteFunc(routes, func(route *Route) bool { return route.Status == "removed" })
	uses, err := s.hostnameUses(ctx, st, p.OrgID, zones, routes)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectDomain, 0, len(zones))
	for _, zone := range zones {
		out = append(out, ProjectDomain{ZoneID: zone.ID, Name: zone.Name, Mode: zone.Mode, Provider: zone.Provider, Verified: zone.verified(), Wildcards: zone.Wildcards(), Hostnames: append([]HostnameUse{}, uses[zone.ID]...)})
	}
	return out, nil
}

// RouteStatus derives one route's states. A removed route is not found.
func (s *Service) RouteStatus(ctx context.Context, p security.Principal, project, id string) (RouteStatus, error) {
	if project == "" {
		return RouteStatus{}, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return RouteStatus{}, err
	}
	st, err := s.domainStore()
	if err != nil {
		return RouteStatus{}, err
	}
	route, err := st.Route(ctx, p.OrgID, project, id)
	if err != nil {
		return RouteStatus{}, err
	}
	if route.Status == "removed" {
		return RouteStatus{}, ErrNotFound
	}
	zone, err := s.linkedZone(ctx, st, p.OrgID, route.Input.ZoneID)
	if errors.Is(err, ErrNotFound) {
		zone, err = nil, nil
	}
	if err != nil {
		return RouteStatus{}, err
	}
	latest, err := st.LatestRouteOperations(ctx, p.OrgID, []string{route.ID})
	if err != nil {
		return RouteStatus{}, err
	}
	return DeriveRouteStatus(*route, zone, latest[route.ID], s.now().UTC()), nil
}

// RouteByHostname finds a project's route by its stored hostname, wildcard
// forms included. The match is exact.
func (s *Service) RouteByHostname(ctx context.Context, p security.Principal, project, hostname string) (*Route, error) {
	if project == "" || hostname == "" {
		return nil, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	routes, err := st.Routes(ctx, p.OrgID, project)
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		if route.Status != "removed" && route.Input.Hostname == hostname {
			return route, nil
		}
	}
	return nil, ErrNotFound
}

// DiscoverProviderZones lists the connection's zones the organization hasn't
// added yet.
func (s *Service) DiscoverProviderZones(ctx context.Context, p security.Principal, connectionID string) (*ProviderZoneList, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	if connectionID == "" {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	conn, raw, err := s.Connections.Credential(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, err
	}
	cred, err := Credential(conn, raw)
	if err != nil {
		return nil, err
	}
	adapter, err := s.Adapters.DNS(conn.Cloud, provider.DNSZoneDiscovery)
	if err != nil {
		return nil, err
	}
	listed, err := adapter.ListZones(ctx, cred)
	if err != nil {
		return nil, err
	}
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	added := map[string]bool{}
	for _, zone := range zones {
		added[zone.Name] = true
	}
	out := &ProviderZoneList{Zones: []provider.DNSZone{}}
	for _, zone := range listed {
		name, nameErr := NormalizeHostname(zone.Name)
		if nameErr != nil {
			name = zone.Name
		}
		if !added[name] {
			out.Zones = append(out.Zones, zone)
		}
	}
	return out, nil
}
