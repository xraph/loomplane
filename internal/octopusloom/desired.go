package octopusloom

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
)

// DesiredInput is everything the desired state of one gateway is built from.
type DesiredInput struct {
	Gateway Gateway
	// ConnectionID is the gateway component's connection. A service on
	// another connection can't be reached from the gateway.
	ConnectionID string
	// Bindings are the environment's bindings. Only the services are route
	// targets.
	Bindings []*planning.Binding
	Routes   []*Route
	// Hostnames are the hostnames attached to the environment's services.
	Hostnames []string
}

// Desired is what Octopus should be running: the config file it starts with
// and what Task 6's syncer pushes to its admin API.
type Desired struct {
	Provider, Namespace, EnvironmentID, Resource string
	// FarpHostnames are the hostnames FARP routes answer on. Empty means any.
	FarpHostnames []string
	Upstreams     []Upstream     // sorted by Name
	Routes        []OctopusRoute // sorted by ID
	Registrations []Registration // Docker only, sorted by Service
	// Skipped maps a manual route's id to why it isn't pushed.
	Skipped map[string]string
}

// Upstream is a named set of instances Octopus load-balances over.
type Upstream struct {
	Name      string
	Instances []Instance
}

// Instance is one address behind an upstream.
type Instance struct {
	ID, Host string
	Port     int
}

// OctopusRoute is one route as Octopus holds it. A manual route whose path
// ends in "/*" is two of these.
type OctopusRoute struct {
	ID, Path, Upstream, StripPrefix string
	Methods, Hosts                  []string
	Priority                        int // always 0
}

// Registration is a service's FARP registration on a Docker gateway.
type Registration struct {
	InstanceID, Service, Address, Revision string
	Port                                   int
	Metadata                               map[string]string // farp.* keys, without farp.port
}

// farpPortKey is the metadata key that carries the port. Octopus is given the
// port as a field, so it isn't repeated in the metadata.
const farpPortKey = "farp.port"

// ServiceAddress is where the service b is reached from other workloads of
// its environment: the container's name on a server, the Service's cluster
// address on Kubernetes. b.ExternalID is the Ctrl Plane instance id.
func ServiceAddress(provider, namespace string, b *planning.Binding) (string, error) {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return "", fmt.Errorf("parse %s's instance id: %w", b.Name, err)
	}
	switch provider {
	case ProviderServer:
		return server.ContainerName(iid), nil
	case ProviderKubernetes:
		if namespace == "" {
			return "", fmt.Errorf("the gateway's namespace is unknown")
		}
		return kubernetes.WorkloadName(iid) + "." + namespace + ".svc", nil
	}
	return "", fmt.Errorf("octopus doesn't run on %q", provider)
}

// BuildDesired works out what Octopus should hold. It is pure and
// deterministic: the same input gives the same Desired, in the same order.
// A manual route that can't be served is left out and explained in Skipped.
// Routes on a hostname that isn't attached are kept, so the board can show
// them on a detached card.
func BuildDesired(in DesiredInput) Desired {
	g := in.Gateway
	d := Desired{
		Provider: g.Provider, Namespace: g.Namespace,
		EnvironmentID: g.EnvironmentID, Resource: g.Resource,
		FarpHostnames: attachedFarpHostnames(g.FarpHostnames, in.Hostnames),
		Skipped:       map[string]string{},
	}

	services := map[string]*serviceTarget{}
	for _, b := range in.Bindings {
		if b == nil || b.Subject != planning.ServiceSubject {
			continue
		}
		var cfg planning.ServiceConfig
		if json.Unmarshal(b.Config, &cfg) != nil {
			continue
		}
		services[b.Name] = &serviceTarget{binding: b, cfg: cfg}
	}

	upstreams := map[string]*Upstream{}
	for _, r := range in.Routes {
		if r == nil {
			continue
		}
		svc := services[r.Service]
		if svc == nil {
			d.Skipped[r.ID] = r.Service + " isn't deployed in this environment."
			continue
		}
		if svc.binding.ConnectionID != in.ConnectionID {
			d.Skipped[r.ID] = r.Service + " runs on a different server or cluster, so Octopus can't reach it."
			continue
		}
		if svc.cfg.Port > 0 && svc.cfg.Port != r.Port {
			d.Skipped[r.ID] = fmt.Sprintf("%s now listens on %d. Edit the route to send it there.", r.Service, svc.cfg.Port)
			continue
		}
		address, err := ServiceAddress(g.Provider, g.Namespace, svc.binding)
		if err != nil {
			d.Skipped[r.ID] = r.Service + " isn't deployed in this environment."
			continue
		}
		name := UpstreamName(r.Service, r.Port)
		if upstreams[name] == nil {
			upstreams[name] = &Upstream{Name: name, Instances: []Instance{{ID: "lp-" + r.Service + "-0", Host: address, Port: r.Port}}}
		}
		d.Routes = append(d.Routes, octopusRoutes(r, name)...)
	}
	for _, name := range slices.Sorted(maps.Keys(upstreams)) {
		d.Upstreams = append(d.Upstreams, *upstreams[name])
	}
	slices.SortFunc(d.Routes, func(a, b OctopusRoute) int { return strings.Compare(a.ID, b.ID) })

	if g.Provider == ProviderServer {
		for _, name := range slices.Sorted(maps.Keys(services)) {
			if reg, ok := registrationFor(g, in.ConnectionID, services[name]); ok {
				d.Registrations = append(d.Registrations, reg)
			}
		}
	}
	return d
}

type serviceTarget struct {
	binding *planning.Binding
	cfg     planning.ServiceConfig
}

// registrationFor is the FARP registration of a service that feeds this
// gateway and runs on its server.
func registrationFor(g Gateway, connectionID string, svc *serviceTarget) (Registration, bool) {
	farp := svc.cfg.Farp
	if farp == nil || farp.Gateway != g.Resource || svc.binding.ConnectionID != connectionID {
		return Registration{}, false
	}
	address, err := ServiceAddress(g.Provider, g.Namespace, svc.binding)
	if err != nil {
		return Registration{}, false
	}
	metadata := maps.Clone(farp.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	delete(metadata, farpPortKey)
	return Registration{
		InstanceID: FarpInstanceID(svc.binding.Name), Service: svc.binding.Name,
		Address: address, Port: farp.Port, Revision: svc.binding.Revision, Metadata: metadata,
	}, true
}

// octopusRoutes is the entries a manual route becomes in Octopus: the route,
// and for a wildcard path a second entry for the bare prefix.
func octopusRoutes(r *Route, upstream string) []OctopusRoute {
	var hosts []string
	if r.Hostname != "" {
		hosts = []string{r.Hostname}
	}
	strip := ""
	if r.StripPrefix && r.Prefix() != "/" {
		strip = r.Prefix()
	}
	ids := OctopusRouteIDs(r.ID, r.Wildcard())
	paths := []string{r.Path, r.Prefix()}
	out := make([]OctopusRoute, 0, len(ids))
	for i, id := range ids {
		out = append(out, OctopusRoute{
			ID: id, Path: paths[i], Upstream: upstream, StripPrefix: strip,
			Methods: slices.Clone(r.Methods), Hosts: slices.Clone(hosts),
		})
	}
	return out
}

// attachedFarpHostnames keeps the FARP hostnames that are attached, sorted.
func attachedFarpHostnames(farp, attached []string) []string {
	var out []string
	for _, h := range farp {
		h = strings.ToLower(h)
		if slices.ContainsFunc(attached, func(a string) bool { return strings.EqualFold(a, h) }) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}
