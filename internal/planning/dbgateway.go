package planning

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// dbGatewayName is the service the database gateway runs as.
const dbGatewayName = "db-gateway"

// planDatabaseGateway plans the database gateway when a Postgres resource
// opts into it here: a service Loomplane generates, on Cloud Run or ECS,
// that serves each opted-in resource at <resource>.<region>.db.<domain>.
// Every service that needs one of them runs after it.
func (p *planner) planDatabaseGateway() {
	m, env := p.in.Manifest, p.in.Environment
	opted := dbGatewayResources(m, env)
	if len(opted) == 0 {
		return
	}
	if path := dbGatewayTaken(m); path != "" {
		p.problem(Finding{
			Code: "db-gateway-name-taken", Subject: ServiceSubject, Name: dbGatewayName, Path: path,
			Message: fmt.Sprintf("The database gateway runs as a service called %s, and %s already has that name. Rename it.", dbGatewayName, path),
		})
		return
	}
	set := dbGatewaySettings(m, env)
	if m.DatabaseGateway == nil || !manifest.On(set.Enabled) {
		for _, name := range opted {
			msg := fmt.Sprintf("%s opts into the database gateway, but the manifest has no database_gateway to say where it runs. Add one, or turn gateway off.", name)
			if m.DatabaseGateway != nil {
				msg = fmt.Sprintf("%s opts into the database gateway, but database_gateway is off in %s.", name, env)
			}
			p.problem(Finding{Code: "db-gateway-missing", Subject: ResourceSubject, Name: name, Path: dbGatewayOptPath(m, name, env), Message: msg})
		}
		return
	}
	var names []string
	for _, name := range opted {
		if err := dbGatewayLabel(name); err != nil {
			p.problem(Finding{
				Code: "db-gateway-name-invalid", Subject: ResourceSubject, Name: name, Path: "resources." + name,
				Message: fmt.Sprintf("%s can't name a host behind the database gateway: %v. Use lowercase letters, digits and single hyphens, start with a letter, and don't end with a hyphen.", name, err),
			})
			continue
		}
		names = append(names, name)
	}
	const base = "database_gateway"
	if len(set.CloudTags) > 0 {
		p.problem(Finding{Code: "cloud-target-unresolved", Subject: ServiceSubject, Name: dbGatewayName, Path: base + ".cloud_tags", Message: "Resolve the database gateway's Cloud target before planning."})
		return
	}
	var able []*provider.Provider
	for _, prov := range p.in.Providers.Running(provider.HTTPService) {
		if slices.Contains(gatewayTargets, prov.Name) {
			able = append(able, prov)
		}
	}
	prov := p.lookup(ServiceSubject, dbGatewayName, base+".provider", set.Provider, able)
	if prov == nil {
		return
	}
	runs, ok := prov.Running(provider.HTTPService)
	if !ok || !slices.Contains(gatewayTargets, prov.Name) {
		p.problem(Finding{
			Code: "db-gateway-unsupported", Subject: ServiceSubject, Name: dbGatewayName, Path: base + ".provider",
			Message: fmt.Sprintf("Loomplane runs the database gateway on Cloud Run and ECS, not on %s.", prov.Title),
		})
		return
	}
	cfg := &ServiceConfig{
		Provider: prov.Name, Region: p.place(ServiceSubject, dbGatewayName, base+".region", prov, set.Region),
		Workload: runs.Workload, Execution: runs.Execution,
		Source: ".", Ecosystem: "go", Image: dbgateway.Dockerfile, Port: dbgateway.Port, Public: true,
	}
	if placement, pinned := p.in.CloudPlacements[dbGatewayName]; pinned {
		placement.Region = cfg.Region
		cfg.CloudPlacement = &placement
	}
	connection := serviceConnection(cfg)
	if connection == "" {
		connection = p.connectionFor(prov.Cloud)
	}
	var served, deps []string
	for _, name := range names {
		dep := p.resources[name]
		if dep == nil || dep.Resource == nil {
			continue // it couldn't be planned, and a problem says why
		}
		// The resource deploys with its managed profile's connection, or
		// else the one the environment uses for its cloud.
		backend := *dep.Resource
		if backend.Managed == nil && backend.Target == "" {
			backend.Target = p.connectionFor(p.cloud(backend.Provider))
		}
		if reaches, why := gatewayReaches(prov.Name, connection, cfg.Region, &backend); !reaches {
			p.problem(Finding{
				Code: "db-gateway-backend-unsupported", Subject: ResourceSubject, Name: name, Path: "resources." + name + ".provider",
				Message: fmt.Sprintf("%s can't go behind the database gateway: %s", name, why),
			})
			continue
		}
		if backend.Region != "" && !strings.EqualFold(backend.Region, cfg.Region) {
			p.warn(Finding{
				Code: "db-gateway-region-mismatch", Subject: ResourceSubject, Name: name, Path: base + ".region",
				Message: fmt.Sprintf("The gateway runs in %s and %s runs in %s, so every query crosses regions.", cfg.Region, name, backend.Region),
			})
		}
		served = append(served, name)
		deps = append(deps, dep.ID)
	}
	domain, ok := p.dbGatewayDomain(base)
	if !ok || len(served) == 0 {
		return
	}
	cfg.DatabaseGateway = &DatabaseGatewayConfig{Name: dbGatewayName, Suffix: strings.ToLower(cfg.Region + ".db." + domain), Resources: served}
	sort.Strings(deps)
	a := &Action{
		ID: actionID(ServiceSubject, dbGatewayName), Subject: ServiceSubject, Name: dbGatewayName,
		Provider: prov.Name, Region: cfg.Region, Engine: prov.Engine(runs), Creates: runs.Creates,
		Service: cfg, ConnectionID: serviceConnection(cfg), DependsOn: deps, Step: 2,
	}
	p.diff(a, a.Service, a.Provider, a.Region)
	if prov.Name == ecsFargate {
		p.checkDatabaseGatewayHTTPS(a, connection)
	}
	p.services[dbGatewayName] = a
	p.actions = append(p.actions, a)
	p.dependOnDatabaseGateway(a, served)
}

// checkDatabaseGatewayHTTPS reports a database gateway on ECS that
// wouldn't answer at an https address, which is the only kind an app is
// handed for it. ECS serves HTTPS only through a certificate on the
// connection, and keeps the plain HTTP port a gateway was first given.
// Where the connection isn't settled, connect says so.
func (p *planner) checkDatabaseGatewayHTTPS(a *Action, connection string) {
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == connection })
	if i < 0 {
		return
	}
	c := p.in.Connections[i]
	var msg string
	switch b := p.bindings[a.ID]; {
	case !c.HTTPS:
		msg = fmt.Sprintf("Apps reach the database gateway only at an https address, and on ECS it gets one only when its connection has a certificate. %s has none. Add an ACM certificate to it, or run database_gateway on Cloud Run.", c.Name)
	case b != nil && strings.HasPrefix(b.URL, "http://") && a.Op != Create && a.Op != Replace:
		msg = fmt.Sprintf("Apps reach the database gateway only at an https address, and it answers at %s, a port ECS opened before %s had a certificate and keeps for it. Move database_gateway to another region or connection, or remove it and deploy it again, to get an https one.", b.URL, c.Name)
	default:
		return
	}
	p.problem(Finding{Code: "db-gateway-needs-https", Subject: ServiceSubject, Name: dbGatewayName, Path: "database_gateway.provider", Message: msg})
}

// linkTargets are the actions whose addresses a link hands a service:
// what it names, and for a resource behind the database gateway, the
// gateway too.
func (p *planner) linkTargets(e EnvLink) []*Action {
	var out []*Action
	if dep := p.planned(e.Subject, e.From); dep != nil {
		out = append(out, dep)
	}
	if gw := p.services[dbGatewayName]; e.Gateway && e.Subject == ResourceSubject && gw != nil && gw.Service != nil && gw.Service.DatabaseGateway != nil {
		out = append(out, gw)
	}
	return out
}

// dbGatewayDomain is the DNS zone the database gateway names its hosts
// in: the only verified zone granted to the project, or else the only one
// of them this environment's domain routes use.
func (p *planner) dbGatewayDomain(path string) (string, bool) {
	var zones []cloudinfra.ProjectZone
	for _, z := range p.in.Zones {
		if z.Verified {
			zones = append(zones, z)
		}
	}
	if len(zones) > 1 {
		var routed []cloudinfra.ProjectZone
		for _, z := range zones {
			if slices.ContainsFunc(p.in.Routes, func(r cloudinfra.Route) bool { return r.Input.ZoneID == z.ID }) {
				routed = append(routed, z)
			}
		}
		if len(routed) > 0 {
			zones = routed
		}
	}
	switch len(zones) {
	case 0:
		p.problem(Finding{
			Code: "db-gateway-domain-missing", Subject: ServiceSubject, Name: dbGatewayName, Path: path,
			Message: "The database gateway names its hosts in a DNS zone, and no verified zone is granted to this project. Add one under Domains and grant it to this project.",
		})
		return "", false
	case 1:
		return strings.ToLower(strings.TrimSuffix(zones[0].Name, ".")), true
	}
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, strings.ToLower(strings.TrimSuffix(z.Name, ".")))
	}
	sort.Strings(names)
	p.problem(Finding{
		Code: "db-gateway-domain-ambiguous", Subject: ServiceSubject, Name: dbGatewayName, Path: path,
		Message: fmt.Sprintf("Several DNS zones could name the database gateway's hosts: %s. Grant this project only the one to use, or route this environment's services through only that one.", strings.Join(names, ", ")),
	})
	return "", false
}

// dependOnDatabaseGateway marks each link a service has to a resource
// behind the gateway, which redeploys the service when that changes, and
// orders every service that needs one after the gateway, wherever it
// runs, moving what runs after those services along with them. A held
// service keeps the links it was deployed with.
func (p *planner) dependOnDatabaseGateway(gw *Action, resources []string) {
	behind := func(e EnvLink) bool { return e.Subject == ResourceSubject && slices.Contains(resources, e.From) }
	for _, name := range p.in.Manifest.ServiceNames() {
		a := p.services[name]
		if _, held := p.held[name]; held || a == nil || a.Service == nil {
			continue
		}
		marked := false
		for i, e := range a.Service.Env {
			if behind(e) && !e.Gateway {
				a.Service.Env[i].Gateway, marked = true, true
			}
		}
		// The links are part of the configuration, so the comparison with
		// what's deployed has to happen again.
		if marked {
			p.diff(a, a.Service, a.Provider, a.Region)
		}
	}
	for _, a := range p.actions {
		if a == gw || a.Service == nil || a.Op == Delete || slices.Contains(a.DependsOn, gw.ID) || !slices.ContainsFunc(a.Service.Env, behind) {
			continue
		}
		a.DependsOn = append(a.DependsOn, gw.ID)
		sort.Strings(a.DependsOn)
	}
	byID := map[string]*Action{}
	for _, a := range p.actions {
		byID[a.ID] = a
	}
	// Each round settles at least one more action, so the rounds are
	// bounded even by a cycle held services were deployed with.
	for range p.actions {
		moved := false
		for _, a := range p.actions {
			for _, id := range a.DependsOn {
				if dep := byID[id]; dep != nil && a.Step <= dep.Step {
					a.Step, moved = dep.Step+1, true
				}
			}
		}
		if !moved {
			return
		}
	}
}

// gatewayReaches reports whether a database gateway on gatewayProvider,
// deploying with gatewayConnection in gatewayRegion, can reach the backend
// b, and if it can't, why. b's connection is its managed profile's, or
// else its Target.
func gatewayReaches(gatewayProvider, gatewayConnection, gatewayRegion string, b *ResourceConfig) (bool, string) {
	switch b.Provider {
	case "digitalocean":
		// Managed PostgreSQL on DigitalOcean has a public address with
		// TLS, so a gateway on either target reaches it.
		return true, ""
	case "rds":
		if gatewayProvider == ecsFargate && gatewayConnection != "" && resourceConnection(b) == gatewayConnection && b.Region == gatewayRegion {
			return true, ""
		}
		return false, "RDS is private to its VPC, so only a gateway on ECS with the same AWS connection and region reaches it. Run database_gateway on ecs-fargate there."
	}
	return false, fmt.Sprintf("Postgres on %s is not supported by the database gateway yet.", b.Provider)
}

func resourceConnection(b *ResourceConfig) string {
	if b.Managed != nil {
		return b.Managed.ConnectionID
	}
	return b.Target
}

// dbGatewayLabel says why a resource's name can't be the first label of
// its host behind the gateway: a route label's database, with no branch,
// that DNS accepts.
func dbGatewayLabel(name string) error {
	l, err := plane.ParseLabel(name)
	switch {
	case err != nil:
		return err
	case l.Branch != "":
		return errors.New(`"--" separates a branch in a route label`)
	case strings.HasSuffix(name, "-"):
		return errors.New("a DNS label can't end with a hyphen")
	}
	return nil
}

// dbGatewayResources are the resources behind the database gateway in env:
// those that opt in and are on there, sorted.
func dbGatewayResources(m *manifest.Manifest, env string) []string {
	var out []string
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		if r.GatewayIn(env) && manifest.On(r.For(env, m.Defaults).Enabled) {
			out = append(out, name)
		}
	}
	return out
}

// dbGatewayTaken is where the manifest already uses the database
// gateway's name, if it does.
func dbGatewayTaken(m *manifest.Manifest) string {
	if _, ok := m.Services[dbGatewayName]; ok {
		return "services." + dbGatewayName
	}
	if _, ok := m.Gateways[dbGatewayName]; ok {
		return "gateways." + dbGatewayName
	}
	return ""
}

// dbGatewayOptPath is where a resource opts into the gateway in env.
func dbGatewayOptPath(m *manifest.Manifest, name, env string) string {
	if o, ok := m.Resources[name].Environments[env]; ok && o.Gateway != nil {
		return "resources." + name + ".environments." + env + ".gateway"
	}
	return "resources." + name + ".gateway"
}

// dbGatewaySettings are where the database gateway runs in env, as a
// service's settings: database_gateway's provider or Cloud tags, with
// the environment's override, else the manifest's default tags or
// provider. Neither leaves it to the Cloud's default placement.
func dbGatewaySettings(m *manifest.Manifest, env string) manifest.Settings {
	g := m.DatabaseGateway
	if g == nil {
		return manifest.Settings{}
	}
	out := manifest.Settings{Provider: g.Provider, Region: g.Region, CloudTags: slices.Clone(g.CloudTags)}
	if o, ok := g.Environments[env]; ok {
		// A region belongs to a provider, so a new provider leaves the
		// old region, and the tags, behind.
		if o.Provider != "" && o.Provider != out.Provider {
			out.Provider, out.Region, out.CloudTags = o.Provider, "", nil
		}
		if o.Region != "" {
			out.Region = o.Region
		}
		out.Enabled = o.Enabled
	}
	if out.Provider == "" && len(out.CloudTags) == 0 {
		switch d := m.Defaults; {
		case len(d.CloudTags) > 0:
			out.CloudTags = slices.Clone(d.CloudTags)
		case d.Provider != "":
			out.Provider = d.Provider
			if out.Region == "" {
				out.Region = d.Region
			}
		}
	}
	return out
}

// dbGatewayToPlace returns the database gateway's settings in env when a
// plan there would plan it.
func dbGatewayToPlace(m *manifest.Manifest, env string) (manifest.Settings, bool) {
	if m.DatabaseGateway == nil || dbGatewayTaken(m) != "" || len(dbGatewayResources(m, env)) == 0 {
		return manifest.Settings{}, false
	}
	set := dbGatewaySettings(m, env)
	return set, manifest.On(set.Enabled)
}

// dbGatewayOn reports whether this plan wants the database gateway.
func (p *planner) dbGatewayOn() bool {
	_, ok := dbGatewayToPlace(p.in.Manifest, p.in.Environment)
	return ok
}

// isDatabaseGateway reports whether a binding deployed the database
// gateway.
func (p *planner) isDatabaseGateway(b *Binding) bool {
	var cfg ServiceConfig
	return b.Subject == ServiceSubject && b.Name == dbGatewayName && json.Unmarshal(b.Config, &cfg) == nil && cfg.DatabaseGateway != nil
}

// resolveDatabaseGatewayPlacement pins the database gateway's Cloud
// target the way a service's is: the target its Cloud tags select, or
// the Cloud's default when nothing else places it. The manifest it
// rewrites is resolveCloudPlacements' copy.
func resolveDatabaseGatewayPlacement(out *manifest.Manifest, env string, assignment *cloudprofiles.Assignment, options []ConnectionOption, pools map[string]*cloudinfra.Placement, placements map[string]CloudPlacement) error {
	set, ok := dbGatewayToPlace(out, env)
	if !ok {
		return nil
	}
	fallback := assignment != nil && assignment.DefaultPlacement != nil && set.Provider == "" && len(set.CloudTags) == 0
	if len(set.CloudTags) == 0 && !fallback {
		return nil
	}
	path := "database_gateway.cloud_tags"
	if fallback {
		path = "database_gateway.provider"
	}
	placement, resolved, err := resolveCloudPlacement(dbGatewayName, path, set, fallback, assignment, options, pools)
	if err != nil {
		return err
	}
	placements[dbGatewayName] = placement
	out.DatabaseGateway = &manifest.DatabaseGateway{Provider: resolved.Provider, Region: resolved.Region}
	return nil
}
