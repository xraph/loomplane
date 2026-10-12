package planning

import (
	"fmt"
	"slices"
	"sort"

	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// gatewayTargets are where Loomplane runs gateways: the targets that
// build a container from a Dockerfile and give it a public address.
var gatewayTargets = []string{cloudRun, ecsFargate}

// planGateways plans each gateway as a service Loomplane generates: an
// HTTP service on Bastion, with a variable for each service it routes to,
// one step after the last of them.
func (p *planner) planGateways() {
	m := p.in.Manifest
	for _, name := range m.GatewayNames() {
		g := m.Gateways[name]
		base := "gateways." + name
		set := g.For(p.in.Environment, m.Defaults)
		if !manifest.On(set.Enabled) {
			continue
		}
		var able []*provider.Provider
		for _, prov := range p.in.Providers.Running(provider.HTTPService) {
			if slices.Contains(gatewayTargets, prov.Name) {
				able = append(able, prov)
			}
		}
		prov := p.lookup(ServiceSubject, name, base+".provider", set.Provider, able)
		if prov == nil {
			continue
		}
		runs, ok := prov.Running(provider.HTTPService)
		if !ok || !slices.Contains(gatewayTargets, prov.Name) {
			p.problem(Finding{
				Code: "gateway-unsupported", Subject: ServiceSubject, Name: name, Path: base + ".provider",
				Message: fmt.Sprintf("Loomplane runs gateways on Cloud Run and ECS, not on %s.", prov.Title),
			})
			continue
		}
		cfg := &ServiceConfig{
			Provider: prov.Name, Region: p.place(ServiceSubject, name, base+".region", prov, set.Region),
			Workload: runs.Workload, Execution: runs.Execution,
			Source: ".", Ecosystem: "go", Image: gateway.Dockerfile, Port: gateway.Port, Public: true,
		}
		a := &Action{
			ID: actionID(ServiceSubject, name), Subject: ServiceSubject, Name: name,
			Provider: prov.Name, Region: cfg.Region, Engine: prov.Engine(runs), Creates: runs.Creates,
			Service: cfg,
		}
		keys := map[string]string{}
		for i, r := range g.Routes {
			path := fmt.Sprintf("%s.routes[%d]", base, i)
			key, ok := keys[r.To]
			if !ok {
				if key, ok = p.route(name, path, a, r.To); !ok {
					continue
				}
				keys[r.To] = key
			}
			cfg.Gateway = append(cfg.Gateway, gateway.Route{Path: r.Path, To: r.To, Env: key, StripPrefix: r.StripPrefix})
		}
		if len(cfg.Gateway) == 0 {
			continue
		}
		sort.Slice(cfg.Env, func(i, j int) bool { return cfg.Env[i].Key < cfg.Env[j].Key })
		sort.Strings(a.DependsOn)
		a.Step = 2
		for _, id := range a.DependsOn {
			for _, dep := range p.services {
				if dep.ID == id {
					a.Step = max(a.Step, dep.Step+1)
				}
			}
		}
		p.diff(a, a.Service, a.Provider, a.Region)
		p.services[name] = a
		p.actions = append(p.actions, a)
	}
}

// route links a gateway to a service it routes to, and returns the
// variable that holds the service's address. The gateway forwards
// requests as they come, so it can reach a public service anywhere, and a
// private one only on ECS, inside the environment's network.
func (p *planner) route(name, path string, a *Action, to string) (string, bool) {
	dep := p.services[to]
	if dep == nil {
		// It couldn't be planned, and a problem says why, or it's off.
		if s := p.in.Manifest.Services[to]; !manifest.On(s.For(p.in.Environment, p.in.Manifest.Defaults).Enabled) {
			p.problem(Finding{
				Code: "need-off", Subject: ServiceSubject, Name: name, Path: path,
				Message: fmt.Sprintf("%s routes to %s, which is off in %s.", name, to, p.in.Environment),
			})
		}
		return "", false
	}
	if dep.Service.Workload == provider.Worker {
		p.problem(Finding{
			Code: "need-worker", Subject: ServiceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s routes to %s, but %s is a worker: it serves nothing.", name, to, to),
		})
		return "", false
	}
	key := envName(to) + "_URL"
	link := EnvLink{Key: key, From: to, Subject: ServiceSubject}
	switch {
	case dep.Provider == ecsFargate && a.Provider == ecsFargate:
		link.Address = fmt.Sprintf("http://%s:%d", to, dep.Service.Port)
	case dep.Service.Public:
	case p.cloud(dep.Provider) != p.cloud(a.Provider):
		p.problem(Finding{
			Code: "private-across-clouds", Subject: ServiceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s runs on %s, but %s is private on %s, and nothing on another cloud can reach a private service. Make %s public, or run them on the same cloud.", name, cloudTitle(p.cloud(a.Provider)), to, cloudTitle(p.cloud(dep.Provider)), to),
		})
		return "", false
	default:
		p.problem(Finding{
			Code: "gateway-cant-reach", Subject: ServiceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s is private on %s, which only lets in callers with an ID token, and a gateway forwards requests as they come. Make %s public, or run both on ECS.", to, cloudTitle(p.cloud(dep.Provider)), to),
		})
		return "", false
	}
	a.Service.Env = append(a.Service.Env, link)
	a.DependsOn = append(a.DependsOn, dep.ID)
	return key, true
}
