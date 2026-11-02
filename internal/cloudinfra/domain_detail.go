package cloudinfra

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ManagedRecord is a DNS record Loomplane wrote and holds the receipt for:
// a zone's TXT proof, or a route's record.
type ManagedRecord struct {
	Kind   string `json:"kind"` // "proof" or "route"
	ZoneID string `json:"zone_id"`
	Type   string `json:"type"`
	// Name is the record's full name: the receipt's name, relative to the
	// hosting zone, joined to it.
	Name        string `json:"name"`
	Value       string `json:"value"`
	TTL         int    `json:"ttl"`
	RouteID     string `json:"route_id,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
}

// DomainDetail is one zone's page: the zone, the zones around it, every
// hostname under it and its subdomains, and the records Loomplane wrote.
type DomainDetail struct {
	Domain ZoneOverview `json:"domain"`
	// Parent and ProofZone carry no hostnames. ProofZone is set only when
	// another zone's record proves Domain.
	Parent     *ZoneOverview  `json:"parent,omitempty"`
	ProofZone  *ZoneOverview  `json:"proof_zone,omitempty"`
	Subdomains []ZoneOverview `json:"subdomains"`
	// Records are Domain's and its subdomains', by name then type.
	Records      []ManagedRecord `json:"records"`
	Registration *Registration   `json:"registration,omitempty"`
}

// DomainDetail reads one of the organization's zones in full. It only
// reads: nothing is looked up in DNS and nothing is saved.
func (s *Service) DomainDetail(ctx context.Context, p security.Principal, zoneID string) (*DomainDetail, error) {
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
	i := slices.IndexFunc(zones, func(z *Zone) bool { return z.ID == zoneID })
	if i < 0 {
		return nil, ErrNotFound
	}
	zone := zones[i]
	routes, err := st.OrgRoutes(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	routes = slices.DeleteFunc(routes, func(route *Route) bool { return route.Status == "removed" })
	uses, err := s.hostnameUses(ctx, st, p.OrgID, zones, routes)
	if err != nil {
		return nil, err
	}
	subdomains := subdomainsOf(zone, zones)
	out := &DomainDetail{Domain: overviewOf(zone, zones, uses), Subdomains: make([]ZoneOverview, 0, len(subdomains)), Records: []ManagedRecord{}}
	if zone.parent != nil {
		parent := overviewOf(zone.parent, zones, nil)
		out.Parent = &parent
	}
	if proof := zone.proofZone(); proof != zone {
		proofZone := overviewOf(proof, zones, nil)
		out.ProofZone = &proofZone
	}
	family := map[string]*Zone{zone.ID: zone}
	for _, sub := range subdomains {
		out.Subdomains = append(out.Subdomains, overviewOf(sub, zones, uses))
		family[sub.ID] = sub
	}
	out.Records = managedRecords(family, routes, uses)
	if zone.RegistrationID != "" {
		reg, regErr := s.zoneRegistration(ctx, p.OrgID, zone.RegistrationID)
		if regErr != nil {
			return nil, regErr
		}
		out.Registration = reg
	}
	return out, nil
}

// managedRecords lists the proof receipts of the zones in family and every
// receipt of the routes under them, by name then type.
func managedRecords(family map[string]*Zone, routes []*Route, uses map[string][]HostnameUse) []ManagedRecord {
	out := []ManagedRecord{}
	for _, zone := range family {
		if r := zone.ProofRecord; r != nil {
			out = append(out, ManagedRecord{Kind: "proof", ZoneID: zone.ID, Type: r.Type, Name: recordName(r, zone), Value: r.Value, TTL: r.TTL})
		}
	}
	for _, route := range routes {
		zone, ok := family[route.Input.ZoneID]
		if !ok {
			continue
		}
		project := ""
		for _, use := range uses[zone.ID] {
			if use.RouteID == route.ID {
				project = use.ProjectName
			}
		}
		for _, r := range route.receipts() {
			out = append(out, ManagedRecord{Kind: "route", ZoneID: zone.ID, Type: r.Type, Name: recordName(&r, zone), Value: r.Value, TTL: r.TTL, RouteID: route.ID, Hostname: route.Input.Hostname, ProjectID: route.ProjectID, ProjectName: project})
		}
	}
	slices.SortFunc(out, func(a, b ManagedRecord) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Type, b.Type), cmp.Compare(a.ZoneID, b.ZoneID), cmp.Compare(a.RouteID, b.RouteID))
	})
	return out
}

// recordName joins a receipt's zone-relative name to the zone's hosting
// zone. An empty relative name is the hosting zone's apex.
func recordName(r *provider.DNSRecord, zone *Zone) string {
	hosting := zone.ProviderZone.Name
	switch {
	case hosting == "":
		return r.Name
	case r.Name == "":
		return hosting
	}
	return r.Name + "." + hosting
}

// zoneRegistration is the registration that bought a zone, as the owner
// sees it. A missing registration, or no registration store, is nil.
func (s *Service) zoneRegistration(ctx context.Context, org, id string) (*Registration, error) {
	st, err := s.registrationStore()
	if err != nil {
		return nil, nil
	}
	reg, err := st.Registration(ctx, org, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return shown(reg), nil
}
