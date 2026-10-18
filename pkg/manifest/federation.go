package manifest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// FederationVersion adds explicit federation intent while Version remains the
// default for generated manifests and existing deployment workflows.
const FederationVersion = 2

// Federation is optional version 2 intent. Declaring it grants no runtime access.
type Federation struct {
	Destinations   map[string]DestinationRef      `json:"destinations,omitempty" yaml:"destinations,omitempty"`
	NetworkLinks   map[string]FederationLink      `json:"network_links,omitempty" yaml:"network_links,omitempty"`
	ServiceExports map[string]FederationExport    `json:"service_exports,omitempty" yaml:"service_exports,omitempty"`
	AccessGrants   map[string]FederationGrant     `json:"access_grants,omitempty" yaml:"access_grants,omitempty"`
	Placements     map[string]FederationPlacement `json:"placements,omitempty" yaml:"placements,omitempty"`
}

// DestinationRef names an existing authorized destination, with explicit per-environment IDs.
type DestinationRef struct {
	ID           string            `json:"id" yaml:"id"`
	Environments map[string]string `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// For selects the destination ID for one environment.
func (d DestinationRef) For(environment string) string {
	if id := d.Environments[environment]; id != "" {
		return id
	}
	return d.ID
}

// FederationLink declares a directed imported or managed network path.
type FederationLink struct {
	From      string `json:"from" yaml:"from"`
	To        string `json:"to" yaml:"to"`
	Transport string `json:"transport" yaml:"transport"`
}

// FederationExport declares an explicitly private service port.
type FederationExport struct {
	Service     string                 `json:"service" yaml:"service"`
	Destination string                 `json:"destination" yaml:"destination"`
	Exposure    string                 `json:"exposure" yaml:"exposure"`
	Port        int                    `json:"port" yaml:"port"`
	Protocol    domain.Protocol        `json:"protocol" yaml:"protocol"`
	Identity    domain.IdentityProfile `json:"identity" yaml:"identity"`
}

// FederationGrant proposes access from an exact workload to an exported port.
// The management approval flow supplies authority, never this manifest alone.
type FederationGrant struct {
	SourceService     string   `json:"source_service" yaml:"source_service"`
	SourceDestination string   `json:"source_destination" yaml:"source_destination"`
	Export            string   `json:"export" yaml:"export"`
	ExpiresAt         string   `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	Methods           []string `json:"methods,omitempty" yaml:"methods,omitempty"`
	Paths             []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	GRPCMethods       []string `json:"grpc_methods,omitempty" yaml:"grpc_methods,omitempty"`
}

// FederationPlacement assigns a service to a destination. Replication and
// traffic weights require the later multi-placement implementation.
type FederationPlacement struct {
	Service     string `json:"service" yaml:"service"`
	Destination string `json:"destination" yaml:"destination"`
}

var destinationRE = regexp.MustCompile(`^dest_[0-7][0-9a-hjkmnp-tv-z]{25}$`)
var grpcMethodRE = regexp.MustCompile(`^/[A-Za-z_][A-Za-z0-9_.]*/[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateFederation checks bounded references and protocol-specific selectors.
// Cross-environment and cross-organization grants are unavailable in this format;
// destination overrides select the current environment's separately scoped ID.
func ValidateFederation(m *Manifest) Problems {
	var ps Problems
	add := func(path, message string) { ps = append(ps, Problem{Path: path, Message: message}) }
	f := m.Federation
	if f == nil {
		return nil
	}
	if m.Version != FederationVersion {
		add("federation", "requires version 2")
		return ps
	}
	if len(m.Links) > 0 {
		add("links", "cannot mix legacy links with federation intent")
	}
	for _, section := range []struct {
		name  string
		names []string
	}{
		{"destinations", sortedKeys(f.Destinations)}, {"network_links", sortedKeys(f.NetworkLinks)}, {"service_exports", sortedKeys(f.ServiceExports)}, {"access_grants", sortedKeys(f.AccessGrants)}, {"placements", sortedKeys(f.Placements)},
	} {
		if len(section.names) > 100 {
			add("federation."+section.name, "at most 100 entries are allowed")
		}
		for _, name := range section.names {
			if !nameRE.MatchString(name) {
				add("federation."+section.name+"."+name, "invalid name")
			}
		}
	}
	destination := func(path, name string) {
		if _, ok := f.Destinations[name]; !ok {
			add(path, "must name a declared destination")
		}
	}
	service := func(path, name string) {
		if _, ok := m.Services[name]; !ok {
			add(path, "must name a service in this manifest")
		}
	}
	for _, name := range sortedKeys(f.Destinations) {
		d := f.Destinations[name]
		path := "federation.destinations." + name
		if !destinationRE.MatchString(d.ID) {
			add(path+".id", "must be a destination ID")
		}
		if len(d.Environments) > 100 {
			add(path+".environments", "at most 100 overrides are allowed")
		}
		for _, env := range sortedKeys(d.Environments) {
			if !envRE.MatchString(env) || !destinationRE.MatchString(d.Environments[env]) {
				add(path+".environments."+env, "must name an environment and destination ID")
			}
		}
	}
	for _, name := range sortedKeys(f.NetworkLinks) {
		l := f.NetworkLinks[name]
		path := "federation.network_links." + name
		destination(path+".from", l.From)
		destination(path+".to", l.To)
		if l.From == l.To {
			add(path, "must join different destinations")
		}
		if l.Transport != "imported" && l.Transport != "ipsec" {
			add(path+".transport", "must be imported or ipsec")
		}
	}
	seen := map[string]bool{}
	for _, name := range sortedKeys(f.ServiceExports) {
		e := f.ServiceExports[name]
		path := "federation.service_exports." + name
		service(path+".service", e.Service)
		destination(path+".destination", e.Destination)
		if e.Exposure != "private" {
			add(path+".exposure", "must explicitly be private")
		}
		if e.Port < 1 || e.Port > MaxPort {
			add(path+".port", "must be between 1 and 65535")
		}
		if !slices.Contains([]domain.Protocol{domain.HTTP, domain.GRPC, domain.TCP}, e.Protocol) {
			add(path+".protocol", "must be http, grpc or tcp")
		}
		if e.Identity != domain.WorkloadIdentity && e.Identity != domain.GatewayIdentity {
			add(path+".identity", "must be workload or gateway")
		}
		key := fmt.Sprintf("%s/%s/%d/%s", e.Service, e.Destination, e.Port, e.Protocol)
		if seen[key] {
			add(path, "duplicates another exported service port")
		}
		seen[key] = true
		svc := m.Services[e.Service]
		if svc.Endpoint != nil && *svc.Endpoint != (Endpoint{}) {
			add(path, "private export cannot keep a public endpoint")
		}
		for _, env := range sortedKeys(svc.Environments) {
			if ep := svc.Environments[env].Endpoint; ep != nil && *ep != (Endpoint{}) {
				add(path, "private export cannot have a public endpoint override")
			}
		}
		for _, gw := range m.Gateways {
			for _, route := range gw.Routes {
				if route.To == e.Service {
					add(path, "private export cannot have a public gateway route")
				}
			}
		}
	}
	for _, name := range sortedKeys(f.AccessGrants) {
		g := f.AccessGrants[name]
		path := "federation.access_grants." + name
		service(path+".source_service", g.SourceService)
		destination(path+".source_destination", g.SourceDestination)
		e, ok := f.ServiceExports[g.Export]
		if !ok {
			add(path+".export", "must name a service export")
		}
		if g.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, g.ExpiresAt); err != nil {
				add(path+".expires_at", "must be an RFC3339 timestamp")
			}
		}
		if len(g.Methods) > 16 || len(g.Paths) > 32 || len(g.GRPCMethods) > 32 {
			add(path, "too many protocol selectors")
		}
		if e.Protocol != domain.HTTP && (len(g.Methods) > 0 || len(g.Paths) > 0) {
			add(path, "HTTP selectors require protocol http")
		}
		if e.Protocol != domain.GRPC && len(g.GRPCMethods) > 0 {
			add(path, "gRPC selectors require protocol grpc")
		}
		for _, method := range g.Methods {
			if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}, method) {
				add(path+".methods", "invalid HTTP method")
			}
		}
		for _, p := range g.Paths {
			if len(p) > 1024 || !probePathRE.MatchString(p) || strings.HasPrefix(p, "//") {
				add(path+".paths", "must be exact absolute paths of at most 1024 bytes")
			}
		}
		for _, method := range g.GRPCMethods {
			if len(method) > 256 || !grpcMethodRE.MatchString(method) {
				add(path+".grpc_methods", "must be exact /package.Service/Method names")
			}
		}
	}
	placements := map[string]bool{}
	for _, name := range sortedKeys(f.Placements) {
		p := f.Placements[name]
		path := "federation.placements." + name
		service(path+".service", p.Service)
		destination(path+".destination", p.Destination)
		key := p.Service + "/" + p.Destination
		if placements[key] {
			add(path, "duplicates a service placement")
		}
		placements[key] = true
	}
	for _, name := range sortedKeys(f.ServiceExports) {
		e := f.ServiceExports[name]
		if !placements[e.Service+"/"+e.Destination] {
			add("federation.service_exports."+name, "requires a matching placement")
		}
	}
	for _, name := range sortedKeys(f.AccessGrants) {
		g := f.AccessGrants[name]
		if !placements[g.SourceService+"/"+g.SourceDestination] {
			add("federation.access_grants."+name, "requires a matching source placement")
		}
	}
	return ps
}
