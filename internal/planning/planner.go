package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// DefaultPort is the port assumed for a container that serves HTTP when
// neither the manifest nor its Dockerfile says which.
const DefaultPort = 8080

// Input is everything a plan is made from.
type Input struct {
	Routes []cloudinfra.Route

	// Catalog requests a standalone service without repository sources.
	Catalog         bool
	CloudPlacements map[string]CloudPlacement
	// Federation contains scoped persisted observations; nil leaves all federation capabilities unavailable.
	Federation *FederationInput
	// Environment is the environment's name, which picks the manifest's
	// overrides.
	Environment string
	Graph       *graph.Graph
	Manifest    *manifest.Manifest
	Providers   *provider.Registry
	// Bindings are what's deployed in the environment now.
	Bindings []*Binding
	// Connections are the organization's cloud connections, and Chosen
	// the environment's choice of one per cloud. A plan needs a ready
	// connection for every cloud it deploys to.
	Connections []ConnectionOption
	Chosen      map[string]string
	// Held are the aliases of repositories this plan doesn't move: their
	// services stay as deployed.
	Held map[string]bool
	// Kinds are the catalog's engines and composed kinds. Nil means
	// recipes.Default().
	Kinds *recipes.Registry
	// Endpoints are the checks of the external endpoints composed
	// resources connect, by endpoint ID. An endpoint checked from more
	// than one target, or against more than one requirement, is under
	// endpointKey instead. A connected endpoint with no check can't be
	// used.
	Endpoints map[string]EndpointCheck
}

// ConnectionOption is a cloud connection a plan could deploy with.
type ConnectionOption struct {
	Pooled    bool `json:"pooled,omitempty"`
	Account   string
	ID        string
	Name      string
	Cloud     string
	Ready     bool
	PullReady bool
	// HTTPS says whether the connection serves public services over
	// HTTPS where that takes a certificate of its own, as ECS does.
	HTTPS bool
	// Region is the region the connection works in, for a cloud whose
	// connection names one, as AWS's does.
	Region string
	// Details are checked, non-secret placement and capability settings.
	Details map[string]string
}

// connectionRegions are the clouds whose connection decides the region.
// An AWS connection names one, and everything Loomplane runs or
// provisions with it is there, down to the network ECS tasks reach their
// databases on, so a resource or service can't be anywhere else.
var connectionRegions = map[string]bool{"aws": true}

// cloudTitles name the clouds connections are for, for people.
var cloudTitles = map[string]string{"gcp": "Google Cloud", "vercel": "Vercel", "aws": "AWS", "render": "Render", "digitalocean": "DigitalOcean", "azure": "Microsoft Azure", "server": "SSH server", "kubernetes": "Kubernetes", "registry": "image registry"}

func cloudTitle(cloud string) string {
	if t, ok := cloudTitles[cloud]; ok {
		return t
	}
	return cloud
}

// Conventional variable names for the address of each resource kind. A
// service that needs two resources of one kind gets each by name instead.
var envKeys = map[string]string{
	"postgres":       "DATABASE_URL",
	"mysql":          "DATABASE_URL",
	"redis":          "REDIS_URL",
	"memcached":      "MEMCACHED_SERVERS",
	"mongodb":        "MONGODB_URI",
	"rabbitmq":       "AMQP_URL",
	"kafka":          "KAFKA_BROKERS",
	"nats":           "NATS_URL",
	"object-storage": "STORAGE_URL",
	"search":         "SEARCH_URL",
	"clickhouse":     "CLICKHOUSE_URL",
}

// Compute decides what would make the environment match the manifest. It's
// deterministic: the same input gives the same result, so a plan's
// revision is reproducible.
func Compute(in Input) *Result {
	if in.Manifest != nil && in.Manifest.Version == manifest.FederationVersion {
		return federationResult(in)
	}
	p := &planner{
		in:        in,
		resources: map[string]*Action{},
		services:  map[string]*Action{},
		matched:   map[string]bool{},
		unready:   map[string]string{},
		clouds:    map[string]bool{},
		bindings:  map[string]*Binding{},
		heldNew:   map[string]string{},
		held:      map[string]string{},
		retained:  map[string]*Action{},
		keptFor:   map[string][]string{},
		taken:     map[string]string{},
		replaced:  map[string][]holder{},
		addressed: map[string][]string{},
		removing:  map[string]bool{},
	}
	for _, b := range in.Bindings {
		p.bindings[actionID(b.Subject, b.Name)] = b
	}
	p.planResources()
	p.planLinks()
	p.planServices()
	p.planGateways()
	p.checkTargetPlacements()
	p.readdress()
	for p.routedReplacements() {
		p.readdress()
	}
	p.readdress()
	p.heldAddresses()
	p.gatewayHeld()
	p.heldDrift()
	p.checkFindings()
	p.impact()
	p.planDeletes()
	p.stillReplaced()
	return p.finish()
}

type planner struct {
	// links are the plan's links, by their two clouds, sorted.
	links    map[[2]string]*Action
	in       Input
	actions  []*Action
	problems []Finding
	warnings []Finding
	// resources and services are the desired actions by manifest name.
	resources map[string]*Action
	services  map[string]*Action
	// matched are graph service IDs the manifest deploys, so the rest can
	// be reported.
	matched map[string]bool
	// graphServices are the graph services the manifest's services are, by
	// manifest name.
	graphServices map[string]*graph.Service
	// unready are providers without an adapter, with why.
	unready map[string]string
	// clouds are the clouds of the available providers the plan uses,
	// each of which needs a connection.
	clouds   map[string]bool
	bindings map[string]*Binding
	// heldNew are the held services that run nowhere yet, by manifest
	// name, with the alias of the repository that holds them.
	heldNew map[string]string
	// held are the held services kept as they're deployed, by manifest
	// name, with the alias of the repository that holds them.
	held map[string]string
	// retained are keep actions, by action ID, for what held services use
	// that the plan would otherwise remove.
	retained map[string]*Action
	// keptFor are the aliases of the held repositories each retained
	// action is kept for, by action ID.
	keptFor map[string][]string
	// taken are the held services the manifest dropped whose name a
	// gateway on here takes, by action ID, with the repository that holds
	// each.
	taken map[string]string
	// replaced are what held services were deployed to use whose name a
	// gateway on here takes, by action ID, with who used them.
	replaced map[string][]holder
	// addressed are the services heldAddresses reported as keeping the
	// address of what this plan replaces, by the replaced action's ID.
	addressed map[string][]string
	// removing are the clouds of what earlier moves left to remove, and
	// of what's deleted from before bindings recorded their connection,
	// which a deployment may need a connection to even when nothing else
	// in the plan uses them.
	removing map[string]bool
}

func actionID(s Subject, name string) string { return string(s) + ":" + name }

func (p *planner) problem(f Finding) { p.problems = append(p.problems, f) }
func (p *planner) warn(f Finding)    { p.warnings = append(p.warnings, f) }

// lookup finds the provider a setting names, reporting a problem if there
// isn't one. alternatives lists the providers that would do.
func (p *planner) lookup(subject Subject, name, path, chosen string, alternatives []*provider.Provider) *provider.Provider {
	if chosen == "" {
		p.problem(Finding{
			Code: "no-provider", Subject: subject, Name: name, Path: path,
			Message: fmt.Sprintf("%s has no provider. Set %s, or defaults.provider. %s", name, path, couldUse(alternatives)),
		})
		return nil
	}
	prov, err := p.in.Providers.Get(chosen)
	if err != nil {
		p.problem(Finding{
			Code: "unknown-provider", Subject: subject, Name: name, Path: path,
			Message: fmt.Sprintf("Loomplane doesn't know a provider called %q. %s", chosen, couldUse(alternatives)),
		})
		return nil
	}
	p.use(prov)
	return prov
}

// use notes that the plan acts on prov: it needs an adapter, and a
// connection to its cloud.
func (p *planner) use(prov *provider.Provider) {
	if !prov.Available {
		p.unready[prov.Name] = prov.Title + ": " + prov.Note
		return
	}
	if !OwnedCloud(prov.Cloud) {
		p.clouds[prov.Cloud] = true
	}
}

// checkHTTPS warns about public services on ECS when the AWS connection
// they deploy with has no certificate: they'll serve plain HTTP.
func (p *planner) checkHTTPS(chosen map[string]string) {
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		a := p.services[name]
		if a.Provider != ecsFargate || a.Service == nil || !a.Service.Public || a.Service.Port == 0 {
			continue
		}
		id := a.ConnectionID
		if id == "" {
			id = chosen["aws"]
		}
		i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
		if i < 0 || p.in.Connections[i].HTTPS {
			continue
		}
		path := "services." + name
		if a.Service.Gateway != nil {
			path = "gateways." + name
		}
		p.warn(Finding{Code: "plain-http", Subject: ServiceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s will serve plain HTTP: %s has no certificate. Add an ACM certificate to the connection to serve HTTPS.", name, p.in.Connections[i].Name)})
	}
}

// connect settles which connection each cloud the plan uses deploys
// with: the environment's choice, or else the organization's only ready
// connection to it.
func (p *planner) connect() map[string]string {
	out := map[string]string{}
	for _, cloud := range slices.Sorted(maps.Keys(p.clouds)) {
		if p.allCloudActionsPinned(cloud) {
			continue
		}
		title := cloudTitle(cloud)
		var ready []ConnectionOption
		for _, c := range p.in.Connections {
			if c.Cloud == cloud && c.Ready {
				ready = append(ready, c)
			}
		}
		if id := p.in.Chosen[cloud]; id != "" {
			i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
			switch {
			case i < 0:
				p.problem(Finding{
					Code: "connection-gone", Path: "connections." + cloud,
					Message: fmt.Sprintf("%s deploys to %s with a connection that's been removed. Choose another.", p.in.Environment, title),
				})
			case !p.in.Connections[i].Ready:
				p.problem(Finding{
					Code: "connection-not-ready", Name: p.in.Connections[i].Name, Path: "connections." + cloud,
					Message: fmt.Sprintf("%s deploys to %s with %s, which isn't ready. Check it under Integrations.", p.in.Environment, title, p.in.Connections[i].Name),
				})
			default:
				out[cloud] = id
			}
			continue
		}
		switch len(ready) {
		case 0:
			p.problem(Finding{
				Code: "no-connection", Path: "connections." + cloud,
				Message: fmt.Sprintf("Nothing is ready to deploy to %s. Connect an account under Integrations.", title),
			})
		case 1:
			out[cloud] = ready[0].ID
		default:
			p.problem(Finding{
				Code: "connection-ambiguous", Path: "connections." + cloud,
				Message: fmt.Sprintf("Several %s connections are ready. Choose which one %s deploys with.", title, p.in.Environment),
			})
		}
	}
	return out
}

// connection is the connection a cloud deploys with, as connect settles
// it, if it settles one. connect says why when it doesn't.
func (p *planner) connection(cloud string) (ConnectionOption, bool) {
	if id := p.in.Chosen[cloud]; id != "" {
		i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
		if i < 0 || !p.in.Connections[i].Ready {
			return ConnectionOption{}, false
		}
		return p.in.Connections[i], true
	}
	var ready []ConnectionOption
	for _, c := range p.in.Connections {
		if c.Cloud == cloud && c.Ready {
			ready = append(ready, c)
		}
	}
	if len(ready) != 1 {
		return ConnectionOption{}, false
	}
	return ready[0], true
}

// checkRegions reports what a plan would deploy, on a cloud whose
// connection decides the region, to a region its connection isn't in.
// The deployment would put it in the connection's region anyway, and a
// move between regions would keep the one it was meant to replace.
// What's kept isn't deployed, so it can stay where it is.
func (p *planner) checkRegions(chosen map[string]string) {
	for _, a := range p.actions {
		if a.Region == "" || (a.Op != Create && a.Op != Update && a.Op != Replace) {
			continue
		}
		prov, err := p.in.Providers.Get(a.Provider)
		if err != nil || !connectionRegions[prov.Cloud] {
			continue
		}
		connectionID := a.ConnectionID
		if connectionID == "" {
			connectionID = chosen[prov.Cloud]
		}
		i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == connectionID })
		if i < 0 {
			continue
		}
		c := p.in.Connections[i]
		if c.Region == "" || c.Region == a.Region {
			continue
		}
		path := "resources." + a.Name + ".region"
		if a.Subject == ServiceSubject {
			path = "services." + a.Name + ".region"
			if a.Service != nil && a.Service.Gateway != nil {
				path = "gateways." + a.Name + ".region"
			}
		}
		title := cloudTitle(prov.Cloud)
		p.problem(Finding{
			Code: "connection-region", Subject: a.Subject, Name: a.Name, Path: path,
			Message: fmt.Sprintf("%s can't be in %s: on %s, Loomplane deploys to the connection's region, and %s is in %s. Set %s to %s, or choose an %s connection in %s for %s.",
				a.Name, a.Region, title, c.Name, c.Region, path, c.Region, title, a.Region, p.in.Environment),
		})
	}
}

func couldUse(ps []*provider.Provider) string {
	if len(ps) == 0 {
		return "No provider Loomplane knows can."
	}
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return "These can: " + strings.Join(names, ", ") + "."
}

// place settles the region, warning about one the provider doesn't list.
func (p *planner) place(subject Subject, name, path string, prov *provider.Provider, region string) string {
	if !prov.Placed() {
		if region != "" {
			p.warn(Finding{
				Code: "region-ignored", Subject: subject, Name: name, Path: path,
				Message: fmt.Sprintf("%s places %s itself, so the region %s is ignored.", prov.Title, name, region),
			})
		}
		return ""
	}
	if region == "" {
		// On a cloud whose connection decides the region, that's the one.
		c, ok := p.connection(prov.Cloud)
		if placement, pinned := p.in.CloudPlacements[name]; pinned && subject == ServiceSubject {
			for _, option := range p.in.Connections {
				if option.ID == placement.ConnectionID {
					c, ok = option, true
					break
				}
			}
		}
		if ok && connectionRegions[prov.Cloud] && c.Region != "" {
			return c.Region
		}
		return prov.DefaultRegion()
	}
	if !prov.HasRegion(region) {
		p.warn(Finding{
			Code: "unknown-region", Subject: subject, Name: name, Path: path,
			Message: fmt.Sprintf("%s isn't one of the %s regions Loomplane lists. If it's a typo, the deployment will fail there.", region, prov.Title),
		})
	}
	return region
}

func (p *planner) planResources() {
	m := p.in.Manifest
	var composed []string
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		base := "resources." + name
		set := r.For(p.in.Environment, m.Defaults)
		if !manifest.On(set.Enabled) || p.noLinkKind(name, base, r) {
			continue
		}
		if p.kinds().Composed(r.Kind) {
			composed = append(composed, name)
			continue
		}
		for _, dep := range slices.Sorted(maps.Keys(r.Uses)) {
			p.problem(Finding{
				Code: "catalog-dependency-unknown", Subject: ResourceSubject, Name: name, Path: base + ".uses." + dep,
				Message: fmt.Sprintf("%s has no dependency called %s.", r.Kind, dep),
			})
		}
		able := p.in.Providers.Provisioning(r.Kind)
		prov := p.lookup(ResourceSubject, name, base+".provider", set.Provider, able)
		if prov == nil {
			continue
		}
		offer, ok := prov.Provisioning(r.Kind)
		if !ok {
			p.problem(Finding{
				Code: "provider-cant-provision", Subject: ResourceSubject, Name: name, Path: base + ".provider",
				Message: fmt.Sprintf("%s doesn't provision %s. %s", prov.Title, r.Kind, couldUse(able)),
			})
			continue
		}
		version := r.Version
		switch {
		case version != "" && len(offer.Versions) == 0:
			p.warn(Finding{
				Code: "version-ignored", Subject: ResourceSubject, Name: name, Path: base + ".version",
				Message: fmt.Sprintf("%s chooses the %s version itself, so %s is ignored.", prov.Title, r.Kind, version),
			})
			version = ""
		case version != "" && !slices.Contains(offer.Versions, version):
			p.problem(Finding{
				Code: "unknown-version", Subject: ResourceSubject, Name: name, Path: base + ".version",
				Message: fmt.Sprintf("%s offers %s %s, not %s.", prov.Title, r.Kind, strings.Join(offer.Versions, ", "), version),
			})
			continue
		case version == "" && len(offer.Versions) > 0:
			version = offer.Versions[0]
		}
		cfg := &ResourceConfig{
			Provider: prov.Name, Region: p.place(ResourceSubject, name, base+".region", prov, set.Region),
			Kind: r.Kind, Version: version,
		}
		a := &Action{
			ID: actionID(ResourceSubject, name), Subject: ResourceSubject, Name: name,
			Provider: prov.Name, Region: cfg.Region, Engine: provider.ResourceEngine, Creates: offer.Creates,
			Resource: cfg, Step: 1,
		}
		p.configureCatalog(name, base, r, cfg, a)
		p.diff(a, cfg, cfg.Provider, cfg.Region)
		p.resources[name] = a
		p.actions = append(p.actions, a)
	}
	p.planCompositions(composed)
}

func (p *planner) planServices() {
	m := p.in.Manifest
	p.graphServices = map[string]*graph.Service{}
	var held []string
	for _, name := range m.ServiceNames() {
		s := m.Services[name]
		base := "services." + name
		set := s.For(p.in.Environment, m.Defaults)
		// A held service ignores manifest edits as well as code changes,
		// so it's settled before anything else the manifest says about it
		// is read: whether it's on, where its source is, how it runs. What
		// runs came from the repository its binding names, so a service
		// deployed from a held repository stays held even when the
		// manifest's repo now names one this plan moves.
		b := p.bindings[actionID(ServiceSubject, name)]
		if alias, ok := p.heldBinding(b); ok {
			p.keep(name, alias, b)
			held = append(held, name)
			continue
		}
		repo, ok := p.repoOf(name, base, s)
		if !ok {
			continue
		}
		if p.in.Held[repo] {
			if b != nil {
				p.keep(name, repo, b)
				held = append(held, name)
			} else if manifest.On(set.Enabled) {
				p.heldNew[name] = repo
				p.warn(Finding{
					Code: "held", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s is in %s, which this plan holds back, and nothing runs it in %s yet, so it isn't created.", name, repo, p.in.Environment),
				})
			}
			continue
		}
		gs := p.match(name, base, repo, s)
		if gs == nil {
			continue
		}
		p.matched[gs.ID] = true
		p.graphServices[name] = gs
		if !manifest.On(set.Enabled) {
			continue
		}
		a := p.desired(name, base, s, gs, set)
		if a == nil {
			continue
		}
		p.services[name] = a
		p.actions = append(p.actions, a)
	}
	p.retain()
	p.markInternal()
	// Needs are settled once every service has its action, since a service
	// can need one planned after it. A held service keeps the links it was
	// deployed with.
	for _, name := range m.ServiceNames() {
		a := p.services[name]
		_, isHeld := p.held[name]
		switch {
		case a == nil:
		case isHeld:
			p.linkHeld(a)
		default:
			p.link(name, a)
		}
	}
	for _, name := range held {
		p.waits(name)
	}
	p.order()
}

// desired builds the action the manifest asks for a service, before its
// needs are linked, or reports why it can't.
func (p *planner) desired(name, base string, s manifest.Service, gs *graph.Service, set manifest.Settings) *Action {
	kind := string(gs.Kind)
	if s.Kind != "" {
		kind = s.Kind
	}
	if !graph.ServiceKind(kind).Deployable() {
		p.problem(Finding{
			Code: "not-deployable", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("%s at %s is a %s, which Loomplane doesn't deploy. Set %s.kind if analysis got it wrong.", name, s.Source, kind, base),
		})
		return nil
	}
	if set.CloudTarget != "" || len(set.CloudTags) > 0 {
		p.problem(Finding{Code: "cloud-target-unresolved", Subject: ServiceSubject, Name: name, Path: cloudPath(name, set), Message: "Resolve this service's Cloud target before planning."})
		return nil
	}
	workload := provider.Workload(kind)
	able := p.in.Providers.Running(workload)
	prov := p.lookup(ServiceSubject, name, base+".provider", set.Provider, able)
	if prov == nil {
		return nil
	}
	runs, ok := prov.Running(workload)
	if !ok {
		p.problem(Finding{
			Code: "provider-cant-run", Subject: ServiceSubject, Name: name, Path: base + ".provider",
			Message: fmt.Sprintf("%s doesn't run a %s like %s. %s", prov.Title, kind, name, couldUse(able)),
		})
		return nil
	}
	cfg := p.serviceConfig(name, base, gs, prov, runs, set)
	p.buildContext(name, base, set.Build, gs, cfg)
	p.configureTarget(name, base, prov, set, cfg)
	if placement, ok := p.in.CloudPlacements[name]; ok {
		placement.Region = cfg.Region
		cfg.CloudPlacement = &placement
	}
	p.checkRender(name, base, cfg)
	p.checkAzure(name, base, cfg)
	return &Action{
		ID: actionID(ServiceSubject, name), Subject: ServiceSubject, Name: name,
		Provider: prov.Name, Region: cfg.Region, Engine: prov.Engine(runs), Creates: runs.Creates,
		Service:      cfg,
		ConnectionID: serviceConnection(cfg),
	}
}

// keep plans a held service as it's deployed: a keep action carrying the
// deployed configuration, so whatever needs it still gets its address and
// nothing deletes it. The manifest isn't consulted, so nothing it says
// about the service can block the plan.
func (p *planner) keep(name, alias string, b *Binding) {
	reason := fmt.Sprintf("Held: this plan doesn't move %s.", alias)
	if alias == "" {
		reason = "Held: nothing records which repository it's from, and this plan holds some back."
	}
	if from := p.deployedRepo(b); from != "" && alias != "" && from != alias {
		reason = "Held: " + movedIntoHeld(name, from, alias)
	}
	a := p.kept(b, reason)
	p.held[name] = alias
	p.services[name] = a
	p.actions = append(p.actions, a)
}

// readdress deploys again what this plan moves and leaves unchanged but
// uses something that gets a new address: what it replaces, or creates
// again over a binding. A service's variables name what it uses, not the
// address, so its configuration doesn't change, and diff would keep it
// with the old address. It runs once every service and gateway has its
// action. Each one already depends on what it uses, so it runs after it.
// A held service, or one kept for it, isn't deployed again, and
// heldAddresses says what that means.
func (p *planner) readdress() {
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		a := p.services[name]
		if _, held := p.held[name]; held || a.Op != Keep || a.Service == nil {
			continue
		}
		var moved []string
		for _, e := range a.Service.Env {
			dep := p.planned(e.Subject, e.From)
			if dep == nil || slices.Contains(moved, dep.Name) {
				continue
			}
			if _, ok := p.readdressed(dep, e, a.Provider); ok {
				moved = append(moved, dep.Name)
			}
		}
		switch len(moved) {
		case 0:
		case 1:
			a.Op, a.Reason = Update, fmt.Sprintf("%s moves, so %s gets its new address.", moved[0], name)
		default:
			a.Op, a.Reason = Update, fmt.Sprintf("%s move, so %s gets their new addresses.", strings.Join(moved, " and "), name)
		}
	}
}

// readdressed reports whether this plan gives dep a new address for a
// link to it from a service on target, and says how, for messages: it
// replaces dep, or creates it again over a binding. A link with a fixed
// address dep still answers at as planned, as one service on ECS reaches
// another, gets nothing new. A move off ECS leaves that address dead, so
// it still counts.
func (p *planner) readdressed(dep *Action, e EnvLink, target string) (string, bool) {
	var how string
	switch {
	case dep.Op == Replace:
		how = "replaces " + dep.Name
	case dep.Op == Create && p.bindings[dep.ID] != nil:
		how = "creates " + dep.Name + " again"
	default:
		return "", false
	}
	if e.Address != "" && e.Address == fixedAddress(dep, target) {
		return "", false
	}
	return how, true
}

// sameAddress reports whether dep, created again over its binding, comes
// back at the address it had, as far as the plan can tell. Only Cloud Run
// says so. The executor deploys to the Ctrl Plane instance the binding
// names, through the provider for the connection and region, and Cloud
// Run names the service after that instance, so the same instance, region
// and connection, which is the Google Cloud project, give the same URL.
// Anything else, a replacement included, which moves provider or region,
// gets an address of its own.
func (p *planner) sameAddress(dep *Action) bool {
	b := p.bindings[dep.ID]
	switch {
	case dep.Op != Create, b == nil, dep.Subject != ServiceSubject:
		return false
	case dep.Provider != cloudRun, b.Provider != cloudRun, b.Name != dep.Name:
		return false
	case b.Region == "", b.Region != dep.Region, b.ExternalID == "", b.ConnectionID == "":
		return false
	}
	prov, err := p.in.Providers.Get(cloudRun)
	connectionID := dep.ConnectionID
	if connectionID == "" && err == nil {
		connectionID = p.connectionFor(prov.Cloud)
	}
	return err == nil && b.ConnectionID == connectionID
}

// connectionFor is the connection the plan deploys to cloud with, as
// connect decides, or empty where connect reports a problem instead.
func (p *planner) connectionFor(cloud string) string {
	if id := p.in.Chosen[cloud]; id != "" {
		i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
		if i < 0 || !p.in.Connections[i].Ready {
			return ""
		}
		return id
	}
	var ready []string
	for _, c := range p.in.Connections {
		if c.Cloud == cloud && c.Ready {
			ready = append(ready, c.ID)
		}
	}
	if len(ready) != 1 {
		return ""
	}
	return ready[0]
}

// fixedAddress is the address link and route give a service on target
// that uses dep, when both run on ECS, inside the environment's network.
// Anywhere else it's empty: dep is reached at an address of its own.
func fixedAddress(dep *Action, target string) string {
	if dep.Service == nil || dep.Provider != ecsFargate || target != ecsFargate {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", dep.Name, dep.Service.Port)
}

// heldAddresses reports what keeps running with the links it was deployed
// with, when one points at something this plan gives a new address, as
// readdressed decides: held services, the services retained for them, and
// held services the manifest dropped. A replacement, or what's created
// again, gets an address of its own, but none of those is deployed
// again, so each keeps the old one's address. A deployment removes what it
// replaces once the replacement is up, and what's created again is gone
// already, so that's a problem. A manifest with held.same_address: warn
// makes it a warning instead when what's created again comes back at the
// same address, as sameAddress decides. It
// runs after planGateways, since a gateway that takes the name of what a
// held service used can replace it too. What was deleted outside
// Loomplane runs nowhere, so it uses nothing. Each one it reports is noted
// in addressed, so gatewayHeld doesn't say it again.
func (p *planner) heldAddresses() {
	type user struct {
		name     string
		provider string
		aliases  []string
		// kept is true for a service retained for a held service rather
		// than held itself.
		kept bool
		env  []EnvLink
	}
	var users []user
	for name, alias := range p.held {
		a := p.services[name]
		users = append(users, user{name: name, provider: a.Provider, aliases: []string{alias}, env: a.Service.Env})
	}
	for id, a := range p.retained {
		if a.Service != nil {
			users = append(users, user{name: a.Name, provider: a.Provider, aliases: p.keptFor[id], kept: true, env: a.Service.Env})
		}
	}
	for _, b := range p.bindings {
		if alias, ok := p.droppedHeld(b); ok {
			var cfg ServiceConfig
			if json.Unmarshal(b.Config, &cfg) == nil {
				users = append(users, user{name: b.Name, provider: b.Provider, aliases: []string{alias}, env: cfg.Env})
			}
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].name < users[j].name })
	for _, u := range users {
		if b := p.bindings[actionID(ServiceSubject, u.name)]; b != nil && b.Drift == DriftMissing {
			continue
		}
		repos, scope := heldScope(u.aliases)
		unknown := len(u.aliases) == 0 || slices.Contains(u.aliases, "")
		var who string
		switch {
		case !u.kept:
			who = heldLabel(u.name, u.aliases[0]) + ", which this plan holds back"
		case unknown:
			who = u.name + ", which this plan keeps for a repository it holds back"
		default:
			who = fmt.Sprintf("%s, which this plan keeps for %s, which it holds back", u.name, repos)
		}
		var seen []string
		for _, e := range u.env {
			dep := p.planned(e.Subject, e.From)
			if dep == nil || slices.Contains(seen, dep.ID) {
				continue
			}
			how, ok := p.readdressed(dep, e, u.provider)
			if !ok {
				continue
			}
			seen = append(seen, dep.ID)
			p.addressed[dep.ID] = append(p.addressed[dep.ID], u.name)
			if p.in.Manifest != nil && p.in.Manifest.Held.WarnSameAddress() && p.sameAddress(dep) {
				p.warn(Finding{
					Code: "held-address", Subject: ServiceSubject, Name: u.name, Path: "services." + u.name,
					Message: fmt.Sprintf("%s, uses %s's address, and this plan creates %s again on Cloud Run with the name, region and connection it had, so it should get the same address, and %s should still reach it. %s if it doesn't.",
						who, e.From, dep.Name, u.name, strings.TrimSuffix(scope, ".")),
				})
				continue
			}
			// A deployment removes what it replaces once the new one is up.
			// What's created again was deleted outside Loomplane.
			fate := "which this plan stops tracking"
			if dep.Op == Replace {
				fate = "which the deployment removes once the new one is up"
			}
			p.problem(Finding{
				Code: "held-address", Subject: ServiceSubject, Name: u.name, Path: "services." + u.name,
				Message: fmt.Sprintf("%s, uses %s's address, and this plan %s, so %s keeps the old one's address, %s. %s",
					who, e.From, how, u.name, fate, scope),
			})
		}
	}
}

// gatewayHeld warns about each name a gateway on here takes from what a
// held repository deployed: once per name, naming the held service the
// manifest dropped that it replaces and every held service that used it.
// It runs after heldAddresses, and leaves out a user it reported already,
// since that problem says more.
func (p *planner) gatewayHeld() {
	var ids []string
	for id := range p.replaced {
		ids = append(ids, id)
	}
	for id := range p.taken {
		if p.replaced[id] == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		var users []string
		for _, h := range p.replaced[id] {
			if !slices.Contains(p.addressed[id], h.user()) {
				users = append(users, h.String())
			}
		}
		b, who := p.bindings[id], strings.Join(users, " and ")
		was := "was"
		if len(users) > 1 {
			was = "were"
		}
		alias, dropped := p.taken[id]
		var msg string
		switch {
		case dropped && who == "":
			msg = fmt.Sprintf("%s is a gateway in the manifest now, so it replaces %s, which this plan holds back.", b.Name, heldLabel(b.Name, alias))
		case dropped:
			msg = fmt.Sprintf("%s is a gateway in the manifest now, so it replaces %s, which this plan holds back. %s, which this plan holds back too, %s deployed to use the %s it replaces.", b.Name, heldLabel(b.Name, alias), who, was, b.Name)
		case who == "":
			continue
		default:
			msg = fmt.Sprintf("%s is a gateway in the manifest now, so it replaces the %s that %s, which this plan holds back, %s deployed to use.", b.Name, b.Name, who, was)
		}
		p.warn(Finding{Code: "held", Subject: ServiceSubject, Name: b.Name, Path: "gateways." + b.Name, Message: msg})
	}
}

// heldDrift reports drift on what the plan keeps as deployed because it
// holds the repository it comes from: held services, and what they use.
// It stays held either way, since the plan doesn't fetch that repository
// and so can't rebuild it. Something deleted outside Loomplane that the
// plan still links to or routes to is a problem, because what needs it
// would point at nothing. Anything else that drifted is a warning.
//
// A held service the manifest no longer has is held too: planDeletes
// leaves it running, so its drift is reported the same way, except that
// no plan brings it back. Nor does one bring back a held service that's
// off here, since a plan that moves its repository removes it.
func (p *planner) heldDrift() {
	type heldItem struct {
		id      string
		aliases []string
		// kept is true for what's kept for a held service rather than
		// held itself.
		kept bool
		// dropped is true for a held service the manifest no longer has,
		// which nothing plans but planDeletes leaves running.
		dropped bool
	}
	items := make([]heldItem, 0, len(p.held)+len(p.retained))
	for name, alias := range p.held {
		items = append(items, heldItem{id: actionID(ServiceSubject, name), aliases: []string{alias}})
	}
	for id := range p.retained {
		items = append(items, heldItem{id: id, aliases: p.keptFor[id], kept: true})
	}
	for id, b := range p.bindings {
		if alias, ok := p.droppedHeld(b); ok {
			items = append(items, heldItem{id: id, aliases: []string{alias}, dropped: true})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	for _, it := range items {
		b := p.bindings[it.id]
		if b == nil || (b.Drift != DriftMissing && b.Drift != DriftChanged) {
			continue
		}
		repos, scope := heldScope(it.aliases)
		from := fmt.Sprintf("%s, which it comes from, is held", repos)
		if it.kept {
			from = fmt.Sprintf("this plan keeps it for %s, which it holds back", repos)
		}
		path := "services." + b.Name
		if b.Subject == ResourceSubject {
			path = "resources." + b.Name
		}
		f := Finding{Subject: b.Subject, Name: b.Name, Path: path}
		if it.dropped {
			p.droppedDrift(f, b, it.aliases)
			continue
		}
		// A held service that's off here is kept as deployed, but a plan
		// that moves its repository removes it.
		off := !it.kept && !p.serviceOn(b.Name)
		switch {
		case b.Drift == DriftMissing && p.needed(it.id) && off:
			f.Code = "held-missing"
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so this plan can't bring it back. It's off in %s, so turn it on and %s.", b.Name, from, p.in.Environment, heldMove(it.aliases))
			p.problem(f)
			continue
		case b.Drift == DriftMissing && p.needed(it.id):
			f.Code = "held-missing"
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so this plan can't bring it back. %s", b.Name, from, scope)
			p.problem(f)
			continue
		case b.Drift == DriftMissing && off:
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so it stays gone. It's off in %s, so %s clears what's left of it.", b.Name, from, p.in.Environment, heldClearer(it.aliases))
		case off:
			f.Message = fmt.Sprintf("%s was changed outside Loomplane (%s), and %s, so it stays that way. It's off in %s, so %s removes it.", b.Name, b.DriftMessage, from, p.in.Environment, heldClearer(it.aliases))
		// What's kept for a held service is what the manifest no longer
		// plans, so a plan that moves the repository removes it rather than
		// bringing it back.
		case b.Drift == DriftMissing && it.kept:
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so it stays gone, and %s clears what's left of it.", b.Name, from, heldClearer(it.aliases))
		case b.Drift == DriftMissing:
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so it stays gone until a plan that moves %s brings it back.", b.Name, from, repos)
		case it.kept:
			f.Message = fmt.Sprintf("%s was changed outside Loomplane (%s), and %s, so it stays that way until %s removes it.", b.Name, b.DriftMessage, from, heldClearer(it.aliases))
		default:
			f.Message = fmt.Sprintf("%s was changed outside Loomplane (%s), and %s, so it stays that way until a plan that moves %s deploys it as planned again.", b.Name, b.DriftMessage, from, repos)
		}
		f.Code = "held-drifted"
		p.warn(f)
	}
}

// droppedDrift reports drift on a held service the manifest no longer
// has, which is the drift heldDrift reports for any held service, except
// that a plan that moves its repository removes it rather than bringing
// it back.
func (p *planner) droppedDrift(f Finding, b *Binding, aliases []string) {
	switch {
	case b.Drift == DriftMissing && p.needed(actionID(b.Subject, b.Name)):
		f.Code = "held-missing"
		f.Message = fmt.Sprintf("%s was deleted outside Loomplane and the manifest no longer has it, so this plan doesn't bring it back, yet something in this plan still uses it. Put it back in the manifest and %s, or stop using it.", b.Name, heldMove(aliases))
		p.problem(f)
		return
	case b.Drift == DriftMissing:
		f.Message = fmt.Sprintf("%s was deleted outside Loomplane and the manifest no longer has it, so it stays gone, and %s clears what's left of it.", b.Name, heldClearer(aliases))
	default:
		f.Message = fmt.Sprintf("%s was changed outside Loomplane (%s) and the manifest no longer has it, so it stays that way until %s removes it.", b.Name, b.DriftMessage, heldClearer(aliases))
	}
	f.Code = "held-drifted"
	p.warn(f)
}

// heldMove says what plans the held repositories again, for a message:
// adding them to the scope, or planning every repository when a binding
// doesn't say which one it's from.
func heldMove(aliases []string) string {
	if len(aliases) == 0 || slices.Contains(aliases, "") {
		return "plan every repository"
	}
	return fmt.Sprintf("add %s to the scope", strings.Join(aliases, " and "))
}

// serviceOn reports whether the manifest has a service and has it on here.
func (p *planner) serviceOn(name string) bool {
	m := p.in.Manifest
	s, ok := m.Services[name]
	return ok && manifest.On(s.For(p.in.Environment, m.Defaults).Enabled)
}

// heldClearer names the plan that removes what a held repository keeps
// running after the manifest dropped it: one that moves the repository,
// or only a plan of every repository when a binding doesn't say which one
// it's from.
func heldClearer(aliases []string) string {
	if len(aliases) == 0 || slices.Contains(aliases, "") {
		return "only a plan of every repository"
	}
	return "a plan that moves " + strings.Join(aliases, " and ")
}

// droppedHeld reports whether a binding is a deployed service of a held
// repository that the manifest no longer has, which planDeletes leaves
// running through keepHeld, and the alias of that repository. A service
// the manifest still has is always planned, since planServices keeps a
// held one as deployed whether or not it's on, so only a dropped one is
// left. A gateway the manifest has by the same name isn't this service.
func (p *planner) droppedHeld(b *Binding) (string, bool) {
	if b.Subject != ServiceSubject || p.planned(b.Subject, b.Name) != nil || p.retained[actionID(b.Subject, b.Name)] != nil {
		return "", false
	}
	return p.heldBinding(b)
}

// heldScope names held repositories for a message, and says what fixes
// them: adding them to the scope, or planning every repository when a
// binding doesn't say which one it's from.
func heldScope(aliases []string) (repos, scope string) {
	if len(aliases) == 0 || slices.Contains(aliases, "") {
		return "a repository this plan holds back", "Plan every repository."
	}
	repos = strings.Join(aliases, " and ")
	return repos, fmt.Sprintf("Add %s to the scope.", repos)
}

// needed reports whether anything else in the plan links or routes to the
// action with this ID. A held service the manifest dropped has no action,
// but it runs on with the links it was deployed with, so those count too.
func (p *planner) needed(id string) bool {
	links := func(env []EnvLink) bool {
		return slices.ContainsFunc(env, func(e EnvLink) bool { return actionID(e.Subject, e.From) == id })
	}
	// What's kept as deployed but was deleted outside Loomplane runs
	// nowhere, so it uses nothing. Anything else deleted is created again.
	gone := func(a *Action) bool {
		b := p.bindings[a.ID]
		return a.Op == Keep && b != nil && b.Drift == DriftMissing
	}
	for _, a := range p.actions {
		if a.ID != id && a.Service != nil && !gone(a) && links(a.Service.Env) {
			return true
		}
	}
	for bid, b := range p.bindings {
		if bid == id || b.Drift == DriftMissing {
			continue
		}
		if _, ok := p.droppedHeld(b); !ok {
			continue
		}
		var cfg ServiceConfig
		if json.Unmarshal(b.Config, &cfg) == nil && links(cfg.Env) {
			return true
		}
	}
	return false
}

// kept is a keep action for what a binding deployed, with the binding's
// configuration, counting its cloud the way an unchanged action's is.
func (p *planner) kept(b *Binding, reason string) *Action {
	a := &Action{
		ID: actionID(b.Subject, b.Name), Op: Keep, Subject: b.Subject, Name: b.Name,
		Provider: b.Provider, Region: b.Region, Engine: b.Engine, Creates: p.creates(b), Reason: reason,
	}
	// A binding that doesn't parse keeps its provider and region, and
	// nothing else is known.
	if b.Subject == ResourceSubject {
		cfg := ResourceConfig{Provider: b.Provider, Region: b.Region}
		_ = json.Unmarshal(b.Config, &cfg) //nolint:errcheck // see above
		a.Resource = &cfg
	} else {
		cfg := ServiceConfig{Provider: b.Provider, Region: b.Region}
		_ = json.Unmarshal(b.Config, &cfg) //nolint:errcheck // see above
		a.Service = &cfg
		if cfg.CloudPlacement != nil {
			a.ConnectionID = b.ConnectionID
		}
	}
	if prov, err := p.in.Providers.Get(b.Provider); err == nil {
		p.use(prov)
	}
	return a
}

// holder is a service that runs on through this plan, whatever the
// manifest says, so what it was deployed against has to stay too. root
// names the held service that's the reason, and via the service kept for
// it that does the using, when that isn't the root itself.
type holder struct {
	root, via string
	// name is the root's service name.
	name string
	// alias is the held repository the root comes from, empty when its
	// binding doesn't say.
	alias string
	env   []EnvLink
}

// user is the service whose links these are: the one kept for the root,
// or the root itself.
func (h holder) user() string {
	if h.via == "" {
		return h.name
	}
	return h.via
}

func (h holder) String() string {
	if h.via == "" {
		return h.root
	}
	return fmt.Sprintf("%s (through %s)", h.root, h.via)
}

// heldLabel names a held service with the repository that holds it, for
// messages. An empty alias is a binding that doesn't say.
func heldLabel(name, alias string) string {
	if alias == "" {
		return name
	}
	return fmt.Sprintf("%s in %s", name, alias)
}

// retain keeps what a held service was deployed against when the plan
// would otherwise remove it, so a held service never loses something it
// uses. Each gets a keep action, so the held service's order points at
// something in the plan, and a held warning saying why it stays.
//
// The held services are the ones the manifest still has, and the deployed
// ones planDeletes will leave running because their repository is held.
// A service kept for one of them runs on too, so what it uses is kept in
// turn, as far down as the links go.
func (p *planner) retain() {
	var queue []holder
	roots := map[string]bool{}
	for _, name := range p.in.Manifest.ServiceNames() {
		if alias, held := p.held[name]; held {
			queue = append(queue, holder{root: heldLabel(name, alias), name: name, alias: alias, env: p.services[name].Service.Env})
			roots[actionID(ServiceSubject, name)] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.bindings)) {
		b := p.bindings[id]
		if b.Subject != ServiceSubject || p.services[b.Name] != nil {
			continue
		}
		var cfg ServiceConfig
		if json.Unmarshal(b.Config, &cfg) != nil || cfg.Gateway != nil {
			// A gateway is built from Loomplane's own code, not from any
			// repository, so no plan holds it. planGateways plans it from
			// the manifest, and planDeletes removes one the manifest dropped.
			continue
		}
		alias, held := p.heldRepo(cfg)
		switch {
		case held && p.gatewayOn(b.Name):
			// A gateway the manifest has on here takes its name, and
			// planGateways plans the gateway in its place, so it doesn't
			// run on, and nothing it uses is kept for it.
			p.taken[id] = alias
		case held:
			queue = append(queue, holder{root: heldLabel(b.Name, alias), name: b.Name, alias: alias, env: cfg.Env})
			roots[id] = true
		}
	}
	// Every user of what's kept is collected before anything is planned,
	// so each warning names them all. A service is followed once, since
	// links between services kept this way can go round, and a root has
	// been followed already: it's kept, and it's nobody's to retain.
	users := map[string][]string{}
	followed := maps.Clone(roots)
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		for _, e := range h.env {
			id := actionID(e.Subject, e.From)
			b := p.bindings[id]
			if p.planned(e.Subject, e.From) != nil || b == nil || roots[id] {
				continue
			}
			// planGateways plans a gateway that's on here in place of what
			// was deployed by its name, so that isn't kept: the held
			// service's link reaches the gateway instead.
			if e.Subject == ServiceSubject && p.gatewayOn(e.From) {
				if !p.isGateway(b) && !slices.ContainsFunc(p.replaced[id], func(r holder) bool { return r.String() == h.String() }) {
					p.replaced[id] = append(p.replaced[id], h)
				}
				continue
			}
			if who := h.String(); !slices.Contains(users[id], who) {
				users[id] = append(users[id], who)
			}
			if !slices.Contains(p.keptFor[id], h.alias) {
				p.keptFor[id] = append(p.keptFor[id], h.alias)
			}
			if b.Subject != ServiceSubject || followed[id] {
				continue
			}
			followed[id] = true
			var cfg ServiceConfig
			if json.Unmarshal(b.Config, &cfg) == nil {
				queue = append(queue, holder{root: h.root, via: b.Name, name: h.name, alias: h.alias, env: cfg.Env})
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(users)) {
		b, who := p.bindings[id], strings.Join(users[id], " and ")
		// Several held services use it, so the verb agrees with them all.
		uses := "uses"
		if len(users[id]) > 1 {
			uses = "use"
		}
		a := p.kept(b, fmt.Sprintf("Kept: %s, which this plan holds back, still %s it.", who, uses))
		a.Step = 1
		if b.Subject == ServiceSubject {
			a.Step = 2
		}
		p.retained[id] = a
		p.actions = append(p.actions, a)
		path := "resources"
		if b.Subject == ServiceSubject {
			path = "services"
		}
		var msg string
		switch wanted, on := p.wantedAsDeployed(b); {
		case !wanted:
			msg = fmt.Sprintf("%s is gone from the manifest, but %s, which this plan holds back, still %s it", b.Name, who, uses)
		case !on:
			msg = fmt.Sprintf("%s is off in %s, but %s, which this plan holds back, still %s it", b.Name, p.in.Environment, who, uses)
		default:
			continue // it couldn't be planned, and a problem says why
		}
		msg += stillThere(b)
		p.warn(Finding{Code: "held", Subject: b.Subject, Name: b.Name, Path: path, Message: msg})
	}
}

// planned is the action the plan has for a subject, if any.
func (p *planner) planned(s Subject, name string) *Action {
	if s == ResourceSubject {
		return p.resources[name]
	}
	return p.services[name]
}

// linkHeld orders a held service after what it was deployed against.
func (p *planner) linkHeld(a *Action) {
	for _, e := range a.Service.Env {
		dep := p.planned(e.Subject, e.From)
		if dep == nil {
			dep = p.retained[actionID(e.Subject, e.From)]
		}
		if dep != nil && !slices.Contains(a.DependsOn, dep.ID) {
			a.DependsOn = append(a.DependsOn, dep.ID)
		}
	}
	sort.Strings(a.DependsOn)
}

// waits says when the manifest would change a held service, which the
// plan holds back. It works out what the manifest asks for with every
// finding, cloud and provider it touches thrown away, so asking can't
// change the plan. Only what the manifest names is marked matched, since
// that's the manifest's to deploy. A repo that names another repository
// than the one it was deployed from is a change that waits too.
func (p *planner) waits(name string) {
	m := p.in.Manifest
	s, alias := m.Services[name], p.held[name]
	base := "services." + name
	set := s.For(p.in.Environment, m.Defaults)
	problems, warnings, unready, clouds := p.problems, p.warnings, p.unready, p.clouds
	p.problems, p.warnings, p.unready, p.clouds = nil, nil, map[string]string{}, map[string]bool{}
	// Moving it to another repository, or turning it off, is a change too.
	repo, ok := p.repoOf(name, base, s)
	moved := ok && repo != alias
	changed := !ok || moved || !manifest.On(set.Enabled)
	var gs *graph.Service
	if ok {
		gs = p.match(name, base, repo, s)
	}
	// The graph service the manifest names is the manifest's, so it isn't
	// reported as left out.
	if gs != nil {
		p.matched[gs.ID] = true
	}
	if !changed {
		var a *Action
		if gs != nil {
			a = p.desired(name, base, s, gs, set)
		}
		if a != nil {
			p.link(name, a)
		}
		changed = a == nil || a.Op != Keep
	}
	found := p.problems
	p.problems, p.warnings, p.unready, p.clouds = problems, warnings, unready, clouds
	if !changed && len(found) == 0 {
		return
	}
	msg := fmt.Sprintf("%s is in %s, which this plan holds back, so its manifest changes wait for a plan that moves %s.", name, alias, alias)
	if from := p.deployedRepo(p.bindings[actionID(ServiceSubject, name)]); from != "" && alias != "" && from != alias {
		// It was deployed from a repository this plan moves, and the
		// manifest now puts it in one that the plan holds back.
		msg = movedIntoHeld(name, from, alias)
	}
	if alias == "" {
		msg = fmt.Sprintf("Nothing records which repository %s was deployed from, and this plan holds some back, so its manifest changes wait for a plan that moves every repository.", name)
	}
	if moved && alias != "" {
		msg += fmt.Sprintf(" That includes its move to %s.", repo)
	}
	if len(found) > 0 {
		msg += " Its manifest also has a problem to fix first: " + found[0].Message
	}
	p.warn(Finding{Code: "held", Subject: ServiceSubject, Name: name, Path: base, Message: msg})
}

// match finds the graph service a manifest service names: in repo, which
// repoOf settled, at its source.
func (p *planner) match(name, base, repo string, s manifest.Service) *graph.Service {
	source := s.Source
	in := func(gs *graph.Service) bool { return repo == "" || gs.Source == repo }
	var found []*graph.Service
	for i := range p.in.Graph.Services {
		gs := &p.in.Graph.Services[i]
		if in(gs) && (gs.Local() == source || gs.Entrypoint == source) {
			found = append(found, gs)
		}
	}
	if len(found) == 0 {
		// A unit's directory names its one service too.
		for i := range p.in.Graph.Services {
			if gs := &p.in.Graph.Services[i]; in(gs) && gs.Path == source {
				found = append(found, gs)
			}
		}
	}
	switch len(found) {
	case 0:
		p.problem(Finding{
			Code: "service-not-found", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Analysis found no service at %s in %s.", source, p.where(repo)),
		})
		return nil
	case 1:
		return found[0]
	}
	ids := make([]string, 0, len(found))
	for _, gs := range found {
		ids = append(ids, gs.Local())
	}
	p.problem(Finding{
		Code: "ambiguous-source", Subject: ServiceSubject, Name: name, Path: base + ".source",
		Message: fmt.Sprintf("More than one service is at %s. Name one: %s.", source, strings.Join(ids, ", ")),
	})
	return nil
}

// repoOf settles which repository a manifest service is in. A graph from
// before sources has none, and matches anywhere.
func (p *planner) repoOf(name, base string, s manifest.Service) (string, bool) {
	sources := p.in.Graph.Sources
	if s.Repo != "" {
		if slices.ContainsFunc(sources, func(r graph.SourceRef) bool { return r.Alias == s.Repo }) {
			return s.Repo, true
		}
		msg := fmt.Sprintf("%s.repo is %s, which isn't one of this project's repositories: %s.", base, s.Repo, aliases(sources))
		if len(sources) == 0 {
			msg = fmt.Sprintf("%s.repo is %s, but this project has no repositories.", base, s.Repo)
		}
		p.problem(Finding{
			Code: "unknown-repo", Subject: ServiceSubject, Name: name, Path: base + ".repo",
			Message: msg,
		})
		return "", false
	}
	switch len(sources) {
	case 0:
		return "", true
	case 1:
		return sources[0].Alias, true
	}
	p.problem(Finding{
		Code: "ambiguous-repo", Subject: ServiceSubject, Name: name, Path: base + ".repo",
		Message: fmt.Sprintf("This project builds from %d repositories (%s), so say which one %s is in with %s.repo.", len(sources), aliases(sources), name, base),
	})
	return "", false
}

func aliases(sources []graph.SourceRef) string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Alias)
	}
	return strings.Join(names, ", ")
}

// where names a repository at the commit it was read at, for messages.
func (p *planner) where(repo string) string {
	if c := p.commit(repo); repo != "" && c != "" {
		return repo + "@" + c[:min(7, len(c))]
	}
	return "this commit"
}

func (p *planner) commit(alias string) string {
	for _, s := range p.in.Graph.Sources {
		if s.Alias == alias {
			return s.CommitSHA
		}
	}
	return ""
}

func (p *planner) serviceConfig(name, base string, gs *graph.Service, prov *provider.Provider, runs provider.Runs, set manifest.Settings) *ServiceConfig {
	cfg := &ServiceConfig{
		Provider: prov.Name, Region: p.place(ServiceSubject, name, base+".region", prov, set.Region),
		Workload: runs.Workload, Execution: runs.Execution,
		Service: gs.ID, Source: gs.Entrypoint, Repo: gs.Source, CommitSHA: p.commit(gs.Source),
		Ecosystem: gs.Ecosystem, Framework: gs.Framework, Image: gs.Image,
		Build: gs.Commands.Build, Start: gs.Commands.Start,
		Public:     runs.Workload != provider.Worker,
		HealthPath: set.HealthPath,
	}
	if cfg.HealthPath == "" && cfg.Framework == "forge" {
		cfg.HealthPath = "/_/health"
	}
	if set.Public != nil {
		cfg.Public = *set.Public
	}
	if cfg.Public && runs.Workload == provider.Worker {
		p.warn(Finding{
			Code: "worker-not-public", Subject: ServiceSubject, Name: name, Path: base + ".public",
			Message: fmt.Sprintf("%s is a worker, which serves nothing, so it gets no public address.", name),
		})
		cfg.Public = false
	}
	if runs.Execution == provider.Container && runs.Workload != provider.Worker {
		cfg.Port = set.Port
		if cfg.Port == 0 && len(gs.Ports) > 0 {
			cfg.Port = gs.Ports[0]
		}
		if cfg.Port == 0 {
			cfg.Port = DefaultPort
			p.warn(Finding{
				Code: "port-assumed", Subject: ServiceSubject, Name: name, Path: base + ".port",
				Message: fmt.Sprintf("Nothing says which port %s listens on, so the plan assumes %d. Set %s.port if it's another.", name, DefaultPort, base),
			})
		}
	}
	if gs.Ecosystem == discovery.Python {
		// A Python service builds from its project, whatever process of it
		// runs: a Procfile's worker is the project's code with another
		// start command.
		cfg.Source = gs.Path
	}
	if gs.Ecosystem == discovery.Python && runs.Execution == provider.Container && cfg.Image == "" && cfg.Start == "" {
		p.warn(Finding{
			Code: "start-assumed", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Nothing says how to start %s, so its buildpack runs gunicorn with main:app. Add a Procfile with a web: line, or a Dockerfile, if that's wrong.", name),
		})
	}
	if prov.Scaling {
		cfg.Scale, cfg.Size = set.Scale, set.Size
	} else if set.Scale != nil || set.Size != nil {
		p.warn(Finding{
			Code: "scaling-ignored", Subject: ServiceSubject, Name: name, Path: base,
			Message: fmt.Sprintf("%s sizes and scales %s itself, so its scale and size are ignored.", prov.Title, name),
		})
	}
	return cfg
}

// buildContext sets the directory a service's Dockerfile builds from:
// the manifest's, else what analysis found. It's kept only when it isn't
// the Dockerfile's own directory, which is what the builders assume, so
// services deployed before contexts existed don't change.
func (p *planner) buildContext(name, base string, b *manifest.Build, gs *graph.Service, cfg *ServiceConfig) {
	dir := gs.Context
	if b != nil && b.Context != "" {
		if cfg.Image == "" {
			p.warn(Finding{
				Code: "build-context-ignored", Subject: ServiceSubject, Name: name, Path: base + ".build.context",
				Message: fmt.Sprintf("%s has no Dockerfile, so its build context is ignored. It's built from its source.", name),
			})
			return
		}
		dir = b.Context
	}
	if cfg.Image != "" && dir != "" {
		cfg.Context = dir
		if cfg.Context == cfg.ownDir() {
			cfg.Context = ""
		}
	}
}

// resourceEnvKey is the conventional variable name for a resource kind, or
// "" when the kind has none. The recipe registry names the kinds Loomplane
// runs itself, and envKeys the rest.
func resourceEnvKey(kind string) string {
	if key := recipes.EnvKey(kind); key != "" {
		return key
	}
	return envKeys[kind]
}

// linkResource gives a service the variable for a resource it needs. A
// service that was deployed with the variable this resource used to get
// (MariaDB's was DB_URL, Valkey's CACHE_URL) keeps it beside the new one, so
// the next release doesn't break it, and the plan says to move off it.
func (p *planner) linkResource(name string, a *Action, need, key, path string) {
	a.Service.Env = append(a.Service.Env, EnvLink{Key: key, From: need, Subject: ResourceSubject})
	legacy := envName(need) + "_URL"
	if legacy == key || !p.deployedWith(a.ID, need, legacy) {
		return
	}
	a.Service.Env = append(a.Service.Env, EnvLink{Key: legacy, From: need, Subject: ResourceSubject})
	p.warn(Finding{
		Code: "env-key-renamed", Subject: ServiceSubject, Name: name, Path: path,
		Message: fmt.Sprintf("%s still gets %s for %s, as it was deployed, and now also gets %s. Move it to %s, then remove the old variable.", name, legacy, need, key, key),
	})
}

// deployedWith reports whether the service's last applied config gave it
// the variable key for the thing it needs.
func (p *planner) deployedWith(id, need, key string) bool {
	b := p.bindings[id]
	if b == nil || len(b.Config) == 0 {
		return false
	}
	var cfg ServiceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return false
	}
	return slices.ContainsFunc(cfg.Env, func(e EnvLink) bool { return e.From == need && e.Key == key })
}

// link gives a service the variables for what it needs, and orders it
// after them.
func (p *planner) link(name string, a *Action) {
	m := p.in.Manifest
	needs := m.Services[name].Needs
	base := "services." + name + ".needs"
	keys := make([]string, len(needs))
	// A composed catalog resource gives a variable for each output.
	outputs := make([][]recipes.Output, len(needs))
	composed := make([]bool, len(needs))
	count := map[string]int{}
	for i, need := range needs {
		if r, ok := m.Resources[need]; ok {
			if kind, ok := p.kinds().Kind(r.Kind); ok {
				outputs[i], composed[i] = kind.Outputs, true
				for _, o := range kind.Outputs {
					count[o.EnvKey]++
				}
				continue
			}
			keys[i] = resourceEnvKey(r.Kind)
		}
		if keys[i] == "" {
			keys[i] = envName(need) + "_URL"
		}
		count[keys[i]]++
	}
	for i, need := range needs {
		key := keys[i]
		if count[key] > 1 {
			key = envName(need) + "_URL"
		}
		if r, ok := m.Resources[need]; ok {
			dep := p.resources[need]
			if dep == nil {
				if !manifest.On(r.For(p.in.Environment, m.Defaults).Enabled) {
					p.problem(Finding{
						Code: "need-off", Subject: ServiceSubject, Name: name, Path: base,
						Message: fmt.Sprintf("%s needs %s, which is off in %s.", name, need, p.in.Environment),
					})
				}
				continue
			}
			if dep.Resource != nil && (dep.Resource.Catalog != nil || dep.Resource.Composition != nil) {
				resourceTarget := &ServiceConfig{Target: dep.Resource.Target, TargetSettings: dep.Resource.TargetSettings}
				if !sameTargetNetwork(a.Service, resourceTarget, p.cloud(a.Provider), p.cloud(dep.Provider)) {
					message := "The private database and its dependent service must run on the same checked target network."
					if dep.Resource.Composition != nil {
						message = fmt.Sprintf("%s and %s must run on the same checked target network.", name, need)
					}
					p.problem(Finding{Code: "database-across-targets", Subject: ServiceSubject, Name: name, Path: base, Message: message})
					continue
				}
			}
			if composed[i] {
				p.linkComposition(a, need, outputs[i], count)
				a.DependsOn = append(a.DependsOn, dep.ID)
				continue
			}
			if rc, sc := p.cloud(dep.Provider), p.cloud(a.Provider); rc != sc {
				if l := p.reachOver(sc, rc); l != nil {
					// Over the link, the resource's private address works.
					p.throughLink(a, l)
					p.linkResource(name, a, need, key, base)
					a.DependsOn = append(a.DependsOn, dep.ID)
					continue
				}
				p.problem(Finding{
					Code: "unreachable-resource", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s runs on %s, but %s is on %s, and a managed resource is only reachable from its own cloud. Put them on the same one.", name, cloudTitle(sc), need, cloudTitle(rc)),
				})
				continue
			}
			if !OwnedCloud(p.cloud(a.Provider)) && a.Service.CloudPlacement != nil && !p.sameDeploymentAccount(a, dep) {
				p.problem(Finding{Code: "resource-across-accounts", Subject: ServiceSubject, Name: name, Path: base, Message: fmt.Sprintf("%s and managed resource %s use different deployment accounts. Choose a target in the resource's account.", name, need)})
				continue
			}

			p.linkResource(name, a, need, key, base)
			a.DependsOn = append(a.DependsOn, dep.ID)
			continue
		}
		dep := p.services[need]
		if dep == nil {
			if alias, held := p.heldNew[need]; held {
				p.problem(Finding{
					Code: "need-held", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s needs %s, which is in %s. This plan holds %s back and nothing runs %s in %s yet, so add %s to the scope.", name, need, alias, alias, need, p.in.Environment, alias),
				})
				continue
			}
			if s, ok := m.Services[need]; ok && !manifest.On(s.For(p.in.Environment, m.Defaults).Enabled) {
				p.problem(Finding{
					Code: "need-off", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s needs %s, which is off in %s.", name, need, p.in.Environment),
				})
			}
			continue
		}
		if dep.Service.Workload == provider.Worker {
			p.problem(Finding{
				Code: "need-worker", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s needs %s, but %s is a worker: it has no address to give.", name, need, need),
			})
			continue
		}
		link := EnvLink{Key: key, From: need, Subject: ServiceSubject}
		switch {
		case !dep.Service.Public && OwnedCloud(p.cloud(dep.Provider)):
			if !sameTargetNetwork(a.Service, dep.Service, p.cloud(a.Provider), p.cloud(dep.Provider)) {
				p.problem(Finding{Code: "private-across-targets", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s needs private service %s on another server or cluster namespace. Choose the same target or configure an externally reachable address through variables.", name, need)})
				continue
			}
		case !dep.Service.Public && !OwnedCloud(p.cloud(dep.Provider)) && p.cloud(dep.Provider) == p.cloud(a.Provider) && (a.Service.CloudPlacement != nil || dep.Service.CloudPlacement != nil) && (!p.sameDeploymentAccount(a, dep) || a.Region != dep.Region && dep.Provider == ecsFargate):
			p.problem(Finding{Code: "private-across-accounts", Subject: ServiceSubject, Name: name, Path: base, Message: fmt.Sprintf("%s needs private service %s in another deployment account or network. Choose the same account and region, or expose a reachable endpoint.", name, need)})
			continue
		case dep.Provider == ecsFargate && a.Provider == ecsFargate && ((a.Service.CloudPlacement == nil && dep.Service.CloudPlacement == nil) || p.sameDeploymentAccount(a, dep) && a.Region == dep.Region):
			// Both run in the environment's network, so traffic between
			// them stays in it, public or not.
			link.Address = fmt.Sprintf("http://%s:%d", need, dep.Service.Port)
		case !dep.Service.Public && dep.Service.Internal && p.reachOver(p.cloud(a.Provider), p.cloud(dep.Provider)) != nil:
			// Over the link, at its address on the environment's internal
			// load balancer, which the runner takes from its binding.
			p.throughLink(a, p.reachOver(p.cloud(a.Provider), p.cloud(dep.Provider)))
		case !dep.Service.Public && p.cloud(dep.Provider) != p.cloud(a.Provider):
			p.problem(Finding{
				Code: "private-across-clouds", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s runs on %s, but %s is private on %s, and nothing on another cloud can reach a private service. Make %s public, or run them on the same cloud.", name, cloudTitle(p.cloud(a.Provider)), need, cloudTitle(p.cloud(dep.Provider)), need),
			})
			continue
		case !dep.Service.Public && dep.Provider == renderTarget && a.Provider == renderTarget:
			// Render's private network: the dependency's address is its
			// name there, which Render gives it when it's made, so the
			// runner takes it from the dependency's binding.
		case !dep.Service.Public && dep.Provider == cloudRun:
			p.warn(Finding{
				Code: "authenticated-call", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s is private, so Cloud Run only lets in callers with a Google ID token. Loomplane lets the project's runtime account call it; %s has to send a token from the metadata server, with %s's URL as the audience.", need, name, need),
			})
		case !dep.Service.Public && dep.Provider != ecsFargate:
			p.problem(Finding{
				Code: "private-unreachable", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s is private on %s, which gives other services no way to reach it. Make it public.", need, cloudTitle(p.cloud(dep.Provider))),
			})
			continue
		}
		a.Service.Env = append(a.Service.Env, link)
		a.DependsOn = append(a.DependsOn, dep.ID)
	}
	sort.Slice(a.Service.Env, func(i, j int) bool { return a.Service.Env[i].Key < a.Service.Env[j].Key })
	sort.Strings(a.DependsOn)
	// The variables are part of the configuration, so the comparison with
	// what's deployed has to happen after them.
	p.diff(a, a.Service, a.Provider, a.Region)
}

// Targets the planner knows how services on them reach each other.
const (
	ecsFargate   = "ecs-fargate"
	cloudRun     = "cloud-run"
	renderTarget = "render"
	azureApps    = "azure-container-apps"
)

// checkAzure reports what Container Apps can't build: Loomplane builds
// for it with ACR Tasks, from a Dockerfile.
func (p *planner) checkAzure(name, base string, cfg *ServiceConfig) {
	if cfg.Provider != azureApps || cfg.Image != "" {
		return
	}
	p.problem(Finding{
		Code: "azure-needs-dockerfile", Subject: ServiceSubject, Name: name, Path: base + ".provider",
		Message: fmt.Sprintf("Loomplane builds for Azure Container Apps from a Dockerfile, and %s has none. Add one, or run %s somewhere else.", name, name),
	})
}

// renderNative are the ecosystems Render builds without a Dockerfile.
var renderNative = map[discovery.Ecosystem]bool{discovery.Go: true, discovery.Node: true, discovery.Python: true}

// checkRender reports what Render can't build: it has native runtimes
// for some languages, and starts a Node service with npm start.
func (p *planner) checkRender(name, base string, cfg *ServiceConfig) {
	if cfg.Provider != renderTarget || cfg.Image != "" || cfg.Execution == provider.Static {
		return
	}
	switch {
	case !renderNative[cfg.Ecosystem]:
		p.problem(Finding{
			Code: "render-needs-dockerfile", Subject: ServiceSubject, Name: name, Path: base + ".provider",
			Message: fmt.Sprintf("Render has no native runtime for %s like %s, so it builds from a Dockerfile. Add one, or run %s somewhere else.", ecosystemTitle(cfg.Ecosystem), name, name),
		})
	case cfg.Ecosystem == discovery.Node && cfg.Start == "":
		p.problem(Finding{
			Code: "render-needs-start", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Render starts a Node service with npm start, and %s's package.json has no start script. Add one, or a Dockerfile.", name),
		})
	}
}

func ecosystemTitle(e discovery.Ecosystem) string {
	switch e {
	case discovery.Rust:
		return "Rust services"
	case discovery.JVM:
		return "JVM services"
	}
	return string(e) + " services"
}

func (p *planner) cloud(name string) string {
	if prov, err := p.in.Providers.Get(name); err == nil {
		return prov.Cloud
	}
	return ""
}

func envName(name string) string { return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) }

// order puts each service one step after the deepest thing it needs.
// Validation rules out cycles, so the recursion ends.
func (p *planner) order() {
	var depth func(name string, seen map[string]bool) int
	depth = func(name string, seen map[string]bool) int {
		a := p.services[name]
		if a.Step > 0 {
			return a.Step
		}
		if seen[name] {
			return 2 // a cycle the manifest's validation should have caught
		}
		seen[name] = true
		step := 2
		for _, need := range p.needs(name, a) {
			if p.services[need] != nil {
				step = max(step, depth(need, seen)+1)
			}
		}
		// A held service also runs after what's kept only for it, and
		// any service after the resources it needs, which is later than
		// step 1 for a composed catalog resource.
		for _, id := range a.DependsOn {
			if r := p.retained[id]; r != nil {
				step = max(step, r.Step+1)
			}
			if name, ok := strings.CutPrefix(id, string(ResourceSubject)+":"); ok && p.resources[name] != nil {
				step = max(step, p.resources[name].Step+1)
			}
		}
		a.Step = step
		return step
	}
	for _, name := range p.in.Manifest.ServiceNames() {
		if p.services[name] != nil {
			depth(name, map[string]bool{})
		}
	}
}

// needs are the services a service runs after: what the manifest says it
// needs, or for a held service, what it was deployed against. Those can
// form a cycle validation never saw, which order cuts at step 2.
func (p *planner) needs(name string, a *Action) []string {
	if _, held := p.held[name]; !held {
		return p.in.Manifest.Services[name].Needs
	}
	var out []string
	for _, id := range a.DependsOn {
		if dep, ok := strings.CutPrefix(id, string(ServiceSubject)+":"); ok {
			out = append(out, dep)
		}
	}
	return out
}

// diff sets an action's op from what's deployed: create when nothing is,
// replace when it's on another provider or region, update when its
// configuration changed, and keep otherwise.
func (p *planner) diff(a *Action, cfg any, providerName, region string) {
	b := p.bindings[a.ID]
	if b == nil {
		a.Op, a.Changes = Create, nil
		a.Reason = fmt.Sprintf("Nothing runs %s in %s yet.", a.Name, p.in.Environment)
		return
	}
	// Something deleted outside Loomplane is planned as if it had never
	// been deployed. Its binding stays, so a deployment replaces it, or
	// removes it if the manifest no longer wants it. That holds for a
	// resource another connection provisioned too: it has no data left
	// to leave behind, so it's created again through this one, and the
	// runner allows that, with no connection-moved problem.
	if b.Drift == DriftMissing {
		a.Op, a.Changes = Create, nil
		a.Reason = fmt.Sprintf("%s was deleted outside Loomplane, so it's created again.", a.Name)
		return
	}
	a.Op, a.Changes = "", changes(b.Config, cfg)
	switch {
	case b.Provider != providerName:
		a.Op = Replace
		a.Reason = fmt.Sprintf("%s moves from %s to %s. The new one starts first, and the old one is removed once it's up.", a.Name, b.Provider, providerName)
	case b.Region != region:
		a.Op = Replace
		a.Reason = fmt.Sprintf("%s moves from %s to %s. A region can't change in place, so the new one starts there first, and the old one is removed once it's up.", a.Name, b.Region, region)
	default:
		p.connectionMoves(a, b, providerName)
	}
	if a.Op == Replace {
		if a.Subject == ResourceSubject {
			p.warn(Finding{
				Code: "data-loss", Subject: ResourceSubject, Name: a.Name, Path: "resources." + a.Name,
				Message: fmt.Sprintf("Replacing %s provisions a new, empty one, then deletes the old one with its data once the new one is up. Loomplane doesn't move data between them.", a.Name),
			})
		}
		return
	}
	// A service runs code, so a new commit is a change too. A gateway's
	// code is Loomplane's, so its routes are what change it.
	newCode := p.newCode(a, b)
	switch {
	case len(a.Changes) > 0 && !newCode:
		a.Op = Update
		a.Reason = fmt.Sprintf("%s's configuration changed.", a.Name)
	case newCode && len(a.Changes) > 1:
		a.Op = Update
		a.Reason = fmt.Sprintf("%s has new code and a changed configuration.", a.Name)
	case newCode:
		a.Op = Update
		a.Reason = fmt.Sprintf("%s has new code.", a.Name)
	case b.Drift == DriftChanged:
		a.Op = Update
		a.Reason = fmt.Sprintf("%s was changed outside Loomplane (%s), so it's deployed as planned again.", a.Name, b.DriftMessage)
	default:
		a.Op = Keep
		a.Reason = "Unchanged."
		if a.Service != nil && a.Service.CloudPlacement != nil {
			var previous ServiceConfig
			if json.Unmarshal(b.Config, &previous) == nil {
				a.AdoptCloudPlacement = previous.CloudPlacement == nil || *previous.CloudPlacement != *a.Service.CloudPlacement
				if a.AdoptCloudPlacement {
					a.CloudAdoption = ReviewCloudAdoption(b)
				}
			}
		}
		// A composed resource and its components adopt their placement the
		// same way. An engine never has one.
		if a.Resource != nil && a.Resource.CloudPlacement != nil {
			var previous ResourceConfig
			if json.Unmarshal(b.Config, &previous) == nil {
				a.AdoptCloudPlacement = previous.CloudPlacement == nil || *previous.CloudPlacement != *a.Resource.CloudPlacement
				if a.AdoptCloudPlacement {
					a.CloudAdoption = ReviewCloudAdoption(b)
				}
			}
		}
	}
}

// connectionMoves settles an action whose binding was deployed through
// another connection than the one the plan deploys to its cloud with, on
// the same provider and in the same region. What one connection deployed
// can't be updated through another, which may reach another account, so
// a service is replaced: the new one starts through this connection, and
// the old one is removed through its own once it's up, so whatever uses
// it is deployed again with the new address. A resource isn't, since
// that would leave its data behind: the plan has a problem instead.
//
// A binding from before bindings recorded their connection names none,
// and could have been deployed through either, so it isn't counted as
// moved. For Cloud Run and ECS the runner finds out as it deploys, since
// a Ctrl Plane instance's provider names its connection: it records the
// connection on the binding and fails the step without deploying, so the
// next plan replaces it here, with whatever uses it. Where connect reports
// a problem, there's nothing to compare with.
func (p *planner) connectionMoves(a *Action, b *Binding, providerName string) {
	prov, err := p.in.Providers.Get(providerName)
	if err != nil || b.ConnectionID == "" {
		return
	}
	now := a.ConnectionID
	if now == "" {
		now = p.connectionFor(prov.Cloud)
	}
	if now == "" || now == b.ConnectionID {
		return
	}
	from, to, cloud := p.connectionName(b.ConnectionID), p.connectionName(now), cloudTitle(prov.Cloud)
	if a.Subject == ResourceSubject {
		msg := fmt.Sprintf("%s was provisioned through %s, and %s now deploys to %s with %s. Loomplane doesn't move data between connections, so provisioning %s through %s would start an empty one and leave the one holding its data behind. Choose %s for %s again to keep %s where it is.",
			a.Name, from, p.in.Environment, cloud, to, a.Name, to, from, cloud, a.Name)
		if from == "" {
			msg = fmt.Sprintf("%s was provisioned through a connection that's been removed, and %s now deploys to %s with %s. Loomplane doesn't move data between connections, and can't tell whether %s reaches %s, so it won't provision %s through it. To start over with an empty one, copy out what you need, take %s out of the manifest, deploy, and put it back. If it's in another account, delete it there yourself.",
				a.Name, p.in.Environment, cloud, to, to, a.Name, a.Name, a.Name)
		}
		p.problem(Finding{Code: "connection-moved", Subject: ResourceSubject, Name: a.Name, Path: "resources." + a.Name, Message: msg})
		return
	}
	a.Op = Replace
	a.Reason = fmt.Sprintf("%s moves from %s to %s, which %s now deploys to %s with. The new one starts first, and the old one is removed through %s once it's up.",
		a.Name, from, to, p.in.Environment, cloud, from)
	if from == "" {
		a.Reason = fmt.Sprintf("%s moves to %s, which %s now deploys to %s with, from a connection that's been removed. The new one starts first. Loomplane can't reach the old one to remove it, so the deployment says how to remove it yourself.",
			a.Name, to, p.in.Environment, cloud)
	}
}

// connectionName names a connection for people, or is empty for one
// that's been removed.
func (p *planner) connectionName(id string) string {
	for _, c := range p.in.Connections {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// newCode reports whether a service is built from another commit than
// the one deployed, and if so says so as a "commit" change in place of the
// commit_sha its configuration carries. The commit is the one the plan
// pins for the service's own repository. A binding that doesn't record
// its commit is compared by its configuration alone.
func (p *planner) newCode(a *Action, b *Binding) bool {
	if a.Subject != ServiceSubject || a.Service == nil || a.Service.Gateway != nil {
		return false
	}
	from, to := b.CommitSHA, a.Service.CommitSHA
	if from == "" || to == "" {
		return false
	}
	a.Changes = slices.DeleteFunc(a.Changes, func(c Change) bool { return c.Field == "commit_sha" })
	if from == to {
		return false
	}
	a.Changes = append(a.Changes, Change{Field: "commit", From: short(from), To: short(to)})
	return true
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// changes compares two configurations field by field, as JSON.
func changes(before json.RawMessage, after any) []Change {
	var from map[string]any
	if len(before) > 0 {
		_ = json.Unmarshal(before, &from) //nolint:errcheck // a binding written by an older Loomplane may not parse; then everything changed
	}
	data, err := json.Marshal(after)
	if err != nil {
		return nil
	}
	var to map[string]any
	if err := json.Unmarshal(data, &to); err != nil {
		return nil
	}
	stripProviderMetadata(from)
	stripProviderMetadata(to)
	fields := map[string]bool{}
	for k := range from {
		fields[k] = true
	}
	for k := range to {
		fields[k] = true
	}
	var out []Change
	for _, f := range slices.Sorted(maps.Keys(fields)) {
		a, b := render(from[f]), render(to[f])
		if a != b {
			out = append(out, Change{Field: f, From: a, To: b})
		}
	}
	return out
}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// checkFindings compares what analysis found with what the manifest says:
// resources the services seem to use that nothing provisions, and
// deployable services the manifest leaves out.
func (p *planner) checkFindings() {
	g, m := p.in.Graph, p.in.Manifest
	byKind := map[string][]string{}
	for _, name := range m.ResourceNames() {
		byKind[m.Resources[name].Kind] = append(byKind[m.Resources[name].Kind], name)
	}
	users := map[string][]string{}
	for _, name := range m.ServiceNames() {
		// A held service's manifest entry waits, so what it says about
		// its needs isn't worth a warning in this plan.
		if _, held := p.held[name]; p.services[name] == nil || held {
			continue
		}
		gs := p.graphServices[name]
		for _, e := range g.Edges {
			if e.From == gs.ID && e.Kind == graph.Needs {
				users[e.To] = append(users[e.To], name)
			}
		}
	}

	for _, r := range g.Resources {
		served := byKind[r.Kind]
		if len(served) == 0 {
			if m.Ignored(r.Kind) {
				continue
			}
			p.warn(unprovided(r, users[r.ID]))
			continue
		}
		for _, svc := range users[r.ID] {
			if !slices.ContainsFunc(m.Services[svc].Needs, func(n string) bool { return slices.Contains(served, n) }) {
				p.warn(Finding{
					Code: "unlinked-resource", Subject: ServiceSubject, Name: svc, Path: "services." + svc + ".needs",
					Message:  fmt.Sprintf("%s seems to use %s, but doesn't need %s, so it won't get its address.", svc, r.Kind, strings.Join(served, " or ")),
					Evidence: r.Evidence,
				})
			}
		}
	}

	for _, gs := range g.Services {
		// A held repository's services aren't matched, and adding one to
		// the manifest would wait anyway.
		if !gs.Deployable || p.matched[gs.ID] || p.in.Held[gs.Source] {
			continue
		}
		p.warn(Finding{
			Code: "service-left-out", Subject: ServiceSubject, Name: gs.Name, Path: "services",
			Message:  fmt.Sprintf("Analysis found %s (%s at %s), but the manifest doesn't deploy it.", gs.Name, gs.Kind, gs.Entrypoint),
			Evidence: gs.Evidence,
		})
	}
}

// impact warns when a package this plan moves is built into services it
// holds back. Those keep the version they declare, so nothing breaks now,
// but the next plan that moves them picks the change up.
func (p *planner) impact() {
	if len(p.in.Held) == 0 {
		return
	}
	g := p.in.Graph
	source, label := map[string]string{}, map[string]string{}
	service := map[string]bool{}
	for _, s := range g.Services {
		source[s.ID], label[s.ID] = s.Source, s.Name
		service[s.ID] = true
	}
	for _, pk := range g.Packages {
		source[pk.ID], label[pk.ID] = pk.Source, pk.Name
	}
	users := map[string][]string{}
	for _, e := range g.Edges {
		// Only a held service counts as a user: it's what runs with the
		// version it declares.
		from, to := source[e.From], source[e.To]
		if e.Kind != graph.Uses || !service[e.From] || from == "" || to == "" || from == to || !p.in.Held[from] || p.in.Held[to] {
			continue
		}
		users[e.To] = append(users[e.To], fmt.Sprintf("%s in %s", label[e.From], from))
	}
	for _, id := range slices.Sorted(maps.Keys(users)) {
		list := slices.Compact(slices.Sorted(slices.Values(users[id])))
		p.warn(Finding{
			Code: "cross-repo-impact", Name: label[id],
			Message: fmt.Sprintf("%s, from %s, moves in this plan and is built into %s, which it holds back. Those keep the version they declare until a plan moves them.", label[id], source[id], strings.Join(list, ", ")),
		})
	}
}

func unprovided(r graph.Resource, users []string) Finding {
	who := "The repository"
	if len(users) > 0 {
		who = strings.Join(users, ", ")
	}
	if r.NeedsConfirmation {
		return Finding{
			Code: "unconfirmed-resource", Subject: ResourceSubject, Name: r.Kind, Path: "resources",
			Message:  fmt.Sprintf("%s might use %s, but the evidence is weak. Add a %s resource if it does, or ignore it.", who, r.Kind, r.Kind),
			Evidence: r.Evidence,
		}
	}
	return Finding{
		Code: "unprovided-resource", Subject: ResourceSubject, Name: r.Kind, Path: "resources",
		Message:  fmt.Sprintf("%s uses %s, but the manifest provisions none. Add one, set its address as a variable yourself, or ignore it.", who, r.Kind),
		Evidence: r.Evidence,
	}
}

// planDeletes removes what's deployed but no longer wanted here: services
// first, then the resources they used.
func (p *planner) planDeletes() {
	last := 1
	for _, a := range p.actions {
		last = max(last, a.Step)
	}
	ids := make([]string, 0, len(p.bindings))
	for id := range p.bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	composed := p.composedBindings()
	for _, id := range ids {
		b := p.bindings[id]
		if p.planned(b.Subject, b.Name) != nil || p.retained[id] != nil {
			continue
		}
		if b.Subject == ServiceSubject {
			if repo, held := p.heldBinding(b); held {
				p.keepHeld(b, repo)
				continue
			}
		}
		// A part of a composed resource goes with it, or when the
		// resource no longer has it.
		of, part := composed[id]
		owner := b
		if part {
			owner = &Binding{Subject: ResourceSubject, Name: of.parent}
		}
		reason := fmt.Sprintf("The manifest no longer has %s.", owner.Name)
		if wanted, on := p.wanted(owner); part && p.resources[of.parent] != nil {
			reason = fmt.Sprintf("%s no longer has %s.", of.parent, b.Name)
		} else if wanted {
			if on {
				continue // it couldn't be planned, and a problem says why
			}
			reason = fmt.Sprintf("%s is off in %s.", owner.Name, p.in.Environment)
		}
		step := last + 1
		if b.Subject == ResourceSubject {
			step = last + 2
			if of.last {
				step = last + 3
			}
			if !of.stateless {
				p.warn(Finding{
					Code: "data-loss", Subject: ResourceSubject, Name: b.Name, Path: "resources",
					Message: fmt.Sprintf("Deleting %s deletes its data.", b.Name),
				})
			}
		}
		connectionID := p.deleteThrough(b)
		p.actions = append(p.actions, &Action{
			ID: id, Op: Delete, Subject: b.Subject, Name: b.Name, Provider: b.Provider, Region: b.Region, ConnectionID: connectionID,
			Engine: b.Engine, Creates: p.creates(b), Reason: reason, Step: step,
		})
	}
}

// deleteThrough settles the connection a delete goes through: the one
// that deployed what it deletes, which needn't be the one the environment
// deploys with now, so the plan needs none of its own for it. Only a
// binding from before bindings recorded their connection goes through the
// plan's, which is noted in removing, as stillReplaced does, so the plan
// pins one when there's one. A provider Loomplane can't deploy to now is
// a problem either way: nothing could remove it.
func (p *planner) deleteThrough(b *Binding) string {
	prov, err := p.in.Providers.Get(b.Provider)
	switch {
	case err != nil:
		return ""
	case !prov.Available:
		p.use(prov)
		return ""
	case b.ConnectionID == "":
		p.removing[prov.Cloud] = true
		return ""
	}
	if !slices.ContainsFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == b.ConnectionID }) {
		p.warn(Finding{
			Code: "delete-unreachable", Subject: b.Subject, Name: b.Name, Path: p.bindingPath(b),
			Message: fmt.Sprintf("%s was deployed through a connection that's been removed, so the deployment can't reach it to delete it. Loomplane stops tracking it, and if it still runs, you delete it yourself.", b.Name),
		})
		return ""
	}
	return b.ConnectionID
}

// bindingPath is where the manifest has, or had, what a binding deployed.
func (p *planner) bindingPath(b *Binding) string {
	switch {
	case b.Subject == ResourceSubject:
		return "resources." + b.Name
	case p.isGateway(b):
		return "gateways." + b.Name
	}
	return "services." + b.Name
}

// stillReplaced warns about what an action ran as before it moved and
// still needs removing, such as an old one whose removal failed. A
// deployment of the action removes it, before anything else when the
// action is deleted, and after it's deployed otherwise. Its cloud is
// noted in removing, so the plan pins a connection to it.
func (p *planner) stillReplaced() {
	for _, id := range slices.Sorted(maps.Keys(p.bindings)) {
		b := p.bindings[id]
		if len(b.Replaced) == 0 {
			continue
		}
		path := p.bindingPath(b)
		then := fmt.Sprintf("This plan keeps %s as it is, so it won't. Retry the deployment that moved %s, or deploy a change to it, and that removes it.", b.Name, b.Name)
		if i := slices.IndexFunc(p.actions, func(a *Action) bool { return a.ID == id }); i >= 0 {
			switch p.actions[i].Op {
			case Keep:
			case Delete:
				// The binding tracks it, so it goes first. Deleting the
				// binding stops tracking it either way.
				then = fmt.Sprintf("This plan's deployment removes it before it deletes %s. If it can't, it warns, and Loomplane stops tracking it, so you delete it yourself.", b.Name)
			default:
				then = "This plan's deployment removes it, and warns if it can't."
			}
		}
		for _, old := range b.Replaced {
			if prov, err := p.in.Providers.Get(old.Provider); err == nil && prov.Available {
				p.removing[prov.Cloud] = true
			}
			on := old.Provider
			if old.Region != "" {
				on += " in " + old.Region
			}
			p.warn(Finding{
				Code: "still-replaced", Subject: b.Subject, Name: b.Name, Path: path,
				Message: fmt.Sprintf("%s moved, and the old one on %s (%s) hasn't been removed yet. %s", b.Name, on, old.ExternalID, then),
			})
		}
	}
}

// heldRepo reports whether a deployed service's repository is held. A
// binding from before repositories has none, which in a project with one
// repository can only be that one. In a project with more, it could be any
// of them, so it counts as held whenever this plan holds any repository:
// a scoped plan never deletes it, and the next full plan rewrites it with
// its repository. The alias is empty when it can't be told.
func (p *planner) heldRepo(cfg ServiceConfig) (string, bool) {
	repo := cfg.Repo
	if repo != "" {
		return repo, p.in.Held[repo]
	}
	if sources := p.in.Graph.Sources; len(sources) == 1 {
		return sources[0].Alias, p.in.Held[sources[0].Alias]
	}
	for _, held := range p.in.Held {
		if held {
			return "", true
		}
	}
	return "", false
}

// deployedRepo is the repository a binding's service was deployed from,
// empty when it doesn't say.
func (p *planner) deployedRepo(b *Binding) string {
	if b == nil {
		return ""
	}
	var cfg ServiceConfig
	if json.Unmarshal(b.Config, &cfg) != nil {
		return ""
	}
	return cfg.Repo
}

// movedIntoHeld says why a service deployed from one repository stays
// as it is when the manifest moves it to a repository this plan holds.
func movedIntoHeld(name, from, to string) string {
	return fmt.Sprintf("%s runs from %s, but the manifest moves it to %s, which this plan holds back, so it stays as deployed and doesn't pick up %s's new commit until a plan moves %s.", name, from, to, from, to)
}

// heldBinding reports whether what a binding deployed is held, by the
// repository it was deployed from, as heldRepo decides. A binding that's
// missing or doesn't parse can't say, and leaves it to the manifest. A
// gateway is never held: it comes from no repository.
func (p *planner) heldBinding(b *Binding) (string, bool) {
	if b == nil {
		return "", false
	}
	var cfg ServiceConfig
	if json.Unmarshal(b.Config, &cfg) != nil || cfg.Gateway != nil {
		return "", false
	}
	return p.heldRepo(cfg)
}

// keepHeld leaves a deployed service of a held repository running when
// the manifest no longer has it, saying why it isn't deleted. As
// droppedHeld says, a service the manifest still has is always planned,
// and a gateway it has by the same name isn't this service. An empty repo
// is a binding that doesn't say which repository it's from.
func (p *planner) keepHeld(b *Binding, repo string) {
	in := fmt.Sprintf("it's in %s, which this plan holds back", repo)
	if repo == "" {
		in = "it was deployed before Loomplane recorded which repository a service is from, and this plan holds some back"
	}
	msg := fmt.Sprintf("The manifest no longer has %s, but %s", b.Name, in) + stillThere(b)
	if repo == "" {
		msg += " Plan every repository to remove it."
	}
	p.warn(Finding{Code: "held", Subject: ServiceSubject, Name: b.Name, Path: "services", Message: msg})
}

// stillThere ends a held warning on what a scoped plan leaves alone: it
// keeps running, unless it was deleted outside Loomplane, in which case it
// stays gone, and heldDrift says what that means for the plan.
func stillThere(b *Binding) string {
	if b.Drift == DriftMissing {
		return ", so this plan doesn't remove it. It was deleted outside Loomplane, so it stays gone."
	}
	return ", so it keeps running."
}

// gatewayOn reports whether the manifest has a gateway by this name that's
// on here, which planGateways plans in place of anything deployed by the
// name.
func (p *planner) gatewayOn(name string) bool {
	m := p.in.Manifest
	g, ok := m.Gateways[name]
	return ok && manifest.On(g.For(p.in.Environment, m.Defaults).Enabled)
}

// isGateway reports whether a binding deployed a gateway.
func (p *planner) isGateway(b *Binding) bool {
	var cfg ServiceConfig
	return b.Subject == ServiceSubject && json.Unmarshal(b.Config, &cfg) == nil && cfg.Gateway != nil
}

// wantedAsDeployed is wanted for what a binding deployed. A gateway the
// manifest has by the name of a deployed service that isn't a gateway
// isn't that service, so to the service the manifest no longer has it.
func (p *planner) wantedAsDeployed(b *Binding) (wanted, on bool) {
	if _, ok := p.in.Manifest.Gateways[b.Name]; ok && b.Subject == ServiceSubject {
		var cfg ServiceConfig
		if json.Unmarshal(b.Config, &cfg) == nil && cfg.Gateway == nil {
			return false, false
		}
	}
	return p.wanted(b)
}

// wanted reports whether the manifest still has a binding's subject, and
// whether it's on in this environment.
func (p *planner) wanted(b *Binding) (wanted, on bool) {
	m := p.in.Manifest
	if b.Subject == ServiceSubject {
		if g, ok := m.Gateways[b.Name]; ok {
			return true, manifest.On(g.For(p.in.Environment, m.Defaults).Enabled)
		}
		s, ok := m.Services[b.Name]
		return ok, ok && manifest.On(s.For(p.in.Environment, m.Defaults).Enabled)
	}
	if l, ok := m.Links[b.Name]; ok {
		return true, manifest.On(l.For(p.in.Environment).Enabled)
	}
	r, ok := m.Resources[b.Name]
	return ok, ok && manifest.On(r.For(p.in.Environment, m.Defaults).Enabled)
}

// creates names what a binding's provider made, for a delete's
// description.
func (p *planner) creates(b *Binding) string {
	prov, err := p.in.Providers.Get(b.Provider)
	if err != nil {
		return b.Provider
	}
	if b.Subject == ResourceSubject {
		var cfg ResourceConfig
		if json.Unmarshal(b.Config, &cfg) == nil {
			if creates := p.composedCreates(cfg); creates != "" {
				return creates
			}
			if offer, ok := prov.Provisioning(cfg.Kind); ok {
				return offer.Creates
			}
		}
		return prov.Title + " resource"
	}
	var cfg ServiceConfig
	if json.Unmarshal(b.Config, &cfg) == nil {
		if runs, ok := prov.Running(cfg.Workload); ok {
			return runs.Creates
		}
	}
	return prov.Title + " service"
}

func (p *planner) finish() *Result {
	sort.SliceStable(p.actions, func(i, j int) bool {
		a, b := p.actions[i], p.actions[j]
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		return a.ID < b.ID
	})
	connections := p.connect()
	// A cloud only what's left to remove needs gets the connection it
	// would deploy with, when there's one, so the runner can reach an old
	// one, or one it deletes, whose binding names no connection. When
	// there isn't one, that's no problem for this plan, which deploys
	// nothing there: the runner warns about an old one, and a delete fails
	// saying to choose one.
	for cloud := range p.removing {
		if _, ok := connections[cloud]; !ok {
			if id := p.connectionFor(cloud); id != "" {
				connections[cloud] = id
			}
		}
	}
	p.checkHTTPS(connections)
	p.checkRegions(connections)
	r := &Result{Actions: make([]Action, 0, len(p.actions)), Problems: p.problems, Warnings: p.warnings, Connections: connections}
	for _, a := range p.actions {
		r.Actions = append(r.Actions, *a)
		switch a.Op {
		case Create:
			r.Summary.Create++
		case Update:
			r.Summary.Update++
		case Replace:
			r.Summary.Replace++
		case Delete:
			r.Summary.Delete++
		case Keep:
			r.Summary.Keep++
		}
	}
	names := make([]string, 0, len(p.unready))
	for name := range p.unready {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r.Warnings = append(r.Warnings, Finding{
			Code: "provider-unavailable", Name: name,
			Message: "Loomplane can't carry out actions on " + p.unready[name] + " This plan is for review.",
		})
	}
	if r.Problems == nil {
		r.Problems = []Finding{}
	}
	if r.Warnings == nil {
		r.Warnings = []Finding{}
	}
	r.Executable = len(r.Problems) == 0 && len(p.unready) == 0
	return r
}
