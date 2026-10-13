package planning

import (
	"fmt"

	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// LinkProvider is the target a link between clouds is made with, and
// LinkKind the kind of resource it is. A link is planned and deployed
// like a resource: a binding records it, the next plan keeps it, and
// drift checks read it back.
const (
	LinkProvider = "cross-cloud-vpn"
	LinkKind     = "private-link"
)

// linkCloud is the cloud a link reaches from: Google Cloud, whose
// services get a route into the other cloud's private network.
const linkCloud = "gcp"

// planLinks plans each link the manifest asks for in this environment.
// Both clouds' connections are settled for it, since the link is made on
// both.
func (p *planner) planLinks() {
	m := p.in.Manifest
	p.links = map[[2]string]*Action{}
	for _, name := range m.LinkNames() {
		l := m.Links[name]
		base := "links." + name
		set := l.For(p.in.Environment)
		if !manifest.On(set.Enabled) {
			continue
		}
		prov, err := p.in.Providers.Get(LinkProvider)
		if err != nil {
			p.problem(Finding{
				Code: "unknown-provider", Subject: ResourceSubject, Name: name, Path: base,
				Message: "This Loomplane has no cross-cloud VPN target to make links with.",
			})
			continue
		}
		p.use(prov)
		for _, c := range l.Between {
			if prov.Available {
				p.clouds[c] = true
			}
		}
		offer, _ := prov.Provisioning(LinkKind)
		cfg := &ResourceConfig{
			Provider: prov.Name, Region: p.place(ResourceSubject, name, base+".region", prov, set.Region), Kind: LinkKind,
		}
		a := &Action{
			ID: actionID(ResourceSubject, name), Subject: ResourceSubject, Name: name,
			Provider: prov.Name, Region: cfg.Region, Engine: provider.ResourceEngine, Creates: offer.Creates,
			Resource: cfg, Step: 1,
		}
		p.diff(a, cfg, cfg.Provider, cfg.Region)
		p.resources[name] = a
		p.actions = append(p.actions, a)
		p.links[l.Pair()] = a
	}
}

// linked is the link between two clouds, if this plan has one.
func (p *planner) linked(a, b string) *Action {
	if a > b {
		a, b = b, a
	}
	return p.links[[2]string{a, b}]
}

// reachOver says whether a service on cloud from can reach something
// private on cloud to over a link, and returns the link. Only Google
// Cloud reaches across one: AWS has a route back, but a private Cloud Run
// service checks Google identity tokens, not addresses.
func (p *planner) reachOver(from, to string) *Action {
	if from != linkCloud || from == to {
		return nil
	}
	return p.linked(from, to)
}

// throughLink makes a's service use a link: it orders the service after
// the link, and sends its private traffic into the network.
func (p *planner) throughLink(a, link *Action) {
	a.Service.PrivateEgress = true
	if !containsString(a.DependsOn, link.ID) {
		a.DependsOn = append(a.DependsOn, link.ID)
	}
}

// markInternal gives each private ECS service that a Google Cloud
// service needs over a link an address on the environment's internal load
// balancer: Service Connect names only resolve inside ECS. It runs before
// any service is compared with what's deployed, so the comparison sees it.
func (p *planner) markInternal() {
	m := p.in.Manifest
	for _, name := range m.ServiceNames() {
		a := p.services[name]
		if a == nil || a.Service == nil {
			continue
		}
		for _, need := range m.Services[name].Needs {
			dep := p.services[need]
			if dep == nil || dep.Service == nil || dep.Service.Public || dep.Provider != ecsFargate {
				continue
			}
			if p.reachOver(p.cloud(a.Provider), p.cloud(dep.Provider)) != nil {
				dep.Service.Internal = true
			}
		}
	}
}

// noLinkKind refuses a resource that asks for a link's kind directly: a
// link is declared under links, which knows its two clouds.
func (p *planner) noLinkKind(name, base string, r manifest.Resource) bool {
	if r.Kind != LinkKind {
		return false
	}
	p.problem(Finding{
		Code: "link-as-resource", Subject: ResourceSubject, Name: name, Path: base + ".kind",
		Message: fmt.Sprintf("%s is a link between clouds. Declare it under links, with the two clouds it joins.", name),
	})
	return true
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
