package manifest

import "strings"

// FederationPlacementGroup declares one bounded generation of an HTTP service.
type FederationPlacementGroup struct {
	Service  string                     `json:"service" yaml:"service"`
	Members  map[string]PlacementMember `json:"members" yaml:"members"`
	MinReady int                        `json:"min_ready" yaml:"min_ready"`
	Routing  GroupRouting               `json:"routing" yaml:"routing"`
}

type PlacementMember struct {
	Destination          string `json:"destination" yaml:"destination"`
	Replicas             int    `json:"replicas" yaml:"replicas"`
	PrerequisiteID       string `json:"prerequisite_id" yaml:"prerequisite_id"`
	PrerequisiteRevision int64  `json:"prerequisite_revision" yaml:"prerequisite_revision"`
}

type GroupRouting struct {
	Mode             string `json:"mode" yaml:"mode"`
	Hostname         string `json:"hostname" yaml:"hostname"`
	ZoneConnectionID string `json:"zone_connection_id,omitempty" yaml:"zone_connection_id,omitempty"`
}

func validatePlacementGroups(m *Manifest) Problems {
	var out Problems
	f := m.Federation
	for _, name := range sortedKeys(f.PlacementGroups) {
		g := f.PlacementGroups[name]
		path := "federation.placement_groups." + name
		add := func(message string) { out = append(out, Problem{Path: path, Message: message}) }
		if _, ok := m.Services[g.Service]; !ok {
			add("must name a service in this manifest")
		}
		for _, p := range f.Placements {
			if p.Service == g.Service {
				add("service cannot use both placements and placement_groups")
			}
		}
		if len(g.Members) < 2 || len(g.Members) > 20 {
			add("requires between 2 and 20 members")
		}
		if g.MinReady != len(g.Members) {
			add("min_ready must equal the member count")
		}
		if g.Routing.Mode != "dns-equal" && g.Routing.Mode != "external" {
			add("routing mode must be dns-equal or external")
		}
		if g.Routing.Hostname == "" || len(g.Routing.Hostname) > 253 || strings.ContainsAny(g.Routing.Hostname, " /:*\n\r") {
			add("routing requires an exact hostname")
		}
		if g.Routing.Mode == "dns-equal" && g.Routing.ZoneConnectionID == "" {
			add("managed DNS requires zone_connection_id")
		}
		if g.Routing.Mode == "external" && g.Routing.ZoneConnectionID != "" {
			add("external routing cannot select a managed DNS connection")
		}
		seen := map[string]bool{}
		for _, member := range sortedKeys(g.Members) {
			p := g.Members[member]
			if !nameRE.MatchString(member) || p.Replicas < 1 || p.PrerequisiteID == "" || p.PrerequisiteRevision < 1 {
				add("members require a valid name, positive replicas and a saved prerequisite revision")
			}
			if _, ok := f.Destinations[p.Destination]; !ok {
				add("member must name a declared destination")
			}
			if seen[p.Destination] {
				add("members must use distinct destinations")
			}
			seen[p.Destination] = true
		}
	}
	return out
}
