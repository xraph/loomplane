package planning

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// farpReservedPrefix starts the names of the upstreams Loomplane registers
// with Octopus for manual routes, so no service that feeds FARP can use it.
const farpReservedPrefix = "lp-"

// FarpMetadata is the FARP metadata a service registers with: one key for
// each path or flag the farp block sets, and the port Octopus reaches the
// service on. A field left empty makes no key.
func FarpMetadata(f manifest.Farp, port int) map[string]string {
	out := map[string]string{}
	for key, value := range map[string]string{
		"farp.openapi.path":  f.OpenAPI,
		"farp.asyncapi.path": f.AsyncAPI,
		"farp.graphql.path":  f.GraphQL,
		"farp.health.path":   f.Health,
		"farp.metrics.path":  f.Metrics,
		"farp.capabilities":  strings.Join(f.Capabilities, ","),
	} {
		if value != "" {
			out[key] = value
		}
	}
	if f.GRPCReflection {
		out["farp.grpc.reflection"] = "true"
	}
	if port > 0 {
		out["farp.port"] = strconv.Itoa(port)
	}
	return out
}

// planFarp resolves each service's opt-in to FARP: the Octopus resource it
// feeds, the port Octopus reaches it on, and the metadata it registers
// with. It runs once every service and resource has its action, so it can
// compare where the two run. A held service keeps the configuration it was
// deployed with.
func (p *planner) planFarp() {
	m := p.in.Manifest
	gateways := p.octopusResources()
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		a := p.services[name]
		if _, held := p.held[name]; held || a == nil || a.Service == nil {
			continue
		}
		set := m.Services[name].For(p.in.Environment, m.Defaults)
		f := set.Farp
		if f == nil || !manifest.On(f.Enabled) {
			continue
		}
		base := "services." + name + ".farp"
		if strings.HasPrefix(name, farpReservedPrefix) {
			p.problem(Finding{
				Code: "farp-name-reserved", Subject: ServiceSubject, Name: name, Path: "services." + name,
				Message: "Services that opt in to FARP can't have names starting with lp-, which Loomplane's Octopus upstreams use.",
			})
			continue
		}
		gateway, ok := p.farpGateway(name, base, f, gateways)
		if !ok {
			continue
		}
		port := f.Port
		if port == 0 {
			port = a.Service.Port
		}
		if port == 0 {
			p.problem(Finding{
				Code: "farp-port-missing", Subject: ServiceSubject, Name: name, Path: base + ".port",
				Message: fmt.Sprintf("%s opts in to FARP, but nothing says which port it listens on. Set services.%s.port, or farp.port.", name, name),
			})
			continue
		}
		// The gateway's own action isn't there when its resource failed to
		// plan, which has its own problem.
		run := p.resources[ComponentActionName(gateway, recipes.OctopusGateway)]
		if run == nil {
			continue
		}
		if run.ConnectionID != a.ConnectionID {
			p.problem(Finding{
				Code: "farp-target-mismatch", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s and %s run on different servers or clusters. FARP needs them on the same one.", name, gateway),
			})
			continue
		}
		a.Service.Farp = &FarpConfig{Gateway: gateway, Port: port, Metadata: FarpMetadata(*f, port)}
	}
}

// octopusResources are the names of the manifest's Octopus resources that
// are on in this environment, sorted.
func (p *planner) octopusResources() []string {
	m := p.in.Manifest
	var out []string
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		if r.Kind == recipes.OctopusKind && manifest.On(r.For(p.in.Environment, m.Defaults).Enabled) {
			out = append(out, name)
		}
	}
	return out
}

// farpGateway is the Octopus resource a service's farp block feeds: the one
// it names, else the manifest's only one.
func (p *planner) farpGateway(name, base string, f *manifest.Farp, gateways []string) (string, bool) {
	switch {
	case f.Gateway != "":
		if slices.Contains(gateways, f.Gateway) {
			return f.Gateway, true
		}
		p.problem(Finding{
			Code: "farp-gateway-unknown", Subject: ServiceSubject, Name: name, Path: base + ".gateway",
			Message: fmt.Sprintf("farp.gateway names %s, which isn't an Octopus resource.", f.Gateway),
		})
	case len(gateways) == 0:
		p.problem(Finding{
			Code: "farp-gateway-missing", Subject: ServiceSubject, Name: name, Path: base,
			Message: fmt.Sprintf("%s opts in to FARP, but this manifest has no Octopus resource. Add one, or drop the farp block.", name),
		})
	case len(gateways) > 1:
		p.problem(Finding{
			Code: "farp-gateway-ambiguous", Subject: ServiceSubject, Name: name, Path: base + ".gateway",
			Message: fmt.Sprintf("%s opts in to FARP and the manifest has %s Octopus resources (%s). Set farp.gateway to pick one.", name, count(len(gateways)), strings.Join(gateways, ", ")),
		})
	default:
		return gateways[0], true
	}
	return "", false
}

// count writes small numbers out as words, for messages.
func count(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	if n >= 0 && n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}
