package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// DefaultPort is the port assumed for a container that serves HTTP when
// neither the manifest nor its Dockerfile says which.
const DefaultPort = 8080

// Input is everything a plan is made from.
type Input struct {
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
}

// ConnectionOption is a cloud connection a plan could deploy with.
type ConnectionOption struct {
	ID    string
	Name  string
	Cloud string
	Ready bool
	// HTTPS says whether the connection serves public services over
	// HTTPS where that takes a certificate of its own, as ECS does.
	HTTPS bool
}

// cloudTitles name the clouds connections are for, for people.
var cloudTitles = map[string]string{"gcp": "Google Cloud", "vercel": "Vercel", "aws": "AWS", "render": "Render"}

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
	}
	for _, b := range in.Bindings {
		p.bindings[actionID(b.Subject, b.Name)] = b
	}
	p.planResources()
	p.planServices()
	p.planGateways()
	p.heldDrift()
	p.checkFindings()
	p.impact()
	p.planDeletes()
	return p.finish()
}

type planner struct {
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
	p.clouds[prov.Cloud] = true
}

// checkHTTPS warns about public services on ECS when the AWS connection
// they deploy with has no certificate: they'll serve plain HTTP.
func (p *planner) checkHTTPS(chosen map[string]string) {
	id := chosen["aws"]
	if id == "" {
		return
	}
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
	if i < 0 || p.in.Connections[i].HTTPS {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		a := p.services[name]
		if a.Provider == ecsFargate && a.Service != nil && a.Service.Public && a.Service.Port > 0 {
			path := "services." + name
			if a.Service.Gateway != nil {
				path = "gateways." + name
			}
			p.warn(Finding{
				Code: "plain-http", Subject: ServiceSubject, Name: name, Path: path,
				Message: fmt.Sprintf("%s will serve plain HTTP: %s has no certificate. Add an ACM certificate to the connection to serve HTTPS.", name, p.in.Connections[i].Name),
			})
		}
	}
}

// connect settles which connection each cloud the plan uses deploys
// with: the environment's choice, or else the organization's only ready
// connection to it.
func (p *planner) connect() map[string]string {
	out := map[string]string{}
	for _, cloud := range slices.Sorted(maps.Keys(p.clouds)) {
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
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		base := "resources." + name
		set := r.For(p.in.Environment, m.Defaults)
		if !manifest.On(set.Enabled) {
			continue
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
		p.diff(a, cfg, cfg.Provider, cfg.Region)
		p.resources[name] = a
		p.actions = append(p.actions, a)
	}
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
	p.buildContext(name, base, s.Build, gs, cfg)
	return &Action{
		ID: actionID(ServiceSubject, name), Subject: ServiceSubject, Name: name,
		Provider: prov.Name, Region: cfg.Region, Engine: prov.Engine(runs), Creates: runs.Creates,
		Service: cfg,
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

// heldDrift reports drift on what the plan keeps as deployed because it
// holds the repository it comes from: held services, and what they use.
// It stays held either way, since the plan doesn't fetch that repository
// and so can't rebuild it. Something deleted outside Loomplane that the
// plan still links to or routes to is a problem, because what needs it
// would point at nothing. Anything else that drifted is a warning.
//
// A held service the manifest dropped, or turned off here, is held too:
// planDeletes leaves it running, so its drift is reported the same way,
// except that no plan brings it back.
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
		switch {
		case b.Drift == DriftMissing && p.needed(it.id):
			f.Code = "held-missing"
			f.Message = fmt.Sprintf("%s was deleted outside Loomplane, and %s, so this plan can't bring it back. %s", b.Name, from, scope)
			p.problem(f)
			continue
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
	move := "plan every repository"
	if repos, _ := heldScope(aliases); len(aliases) > 0 && !slices.Contains(aliases, "") {
		move = fmt.Sprintf("add %s to the scope", repos)
	}
	switch {
	case b.Drift == DriftMissing && p.needed(actionID(b.Subject, b.Name)):
		f.Code = "held-missing"
		f.Message = fmt.Sprintf("%s was deleted outside Loomplane and the manifest no longer has it, so this plan doesn't bring it back, yet something in this plan still uses it. Put it back in the manifest and %s, or stop using it.", b.Name, move)
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
	for _, a := range p.actions {
		if a.ID != id && a.Service != nil && links(a.Service.Env) {
			return true
		}
	}
	for bid, b := range p.bindings {
		// One deleted outside Loomplane runs nowhere, so it uses nothing.
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
	// alias is the held repository the root comes from, empty when its
	// binding doesn't say.
	alias string
	env   []EnvLink
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
			queue = append(queue, holder{root: heldLabel(name, alias), alias: alias, env: p.services[name].Service.Env})
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
		if alias, held := p.heldRepo(cfg); held {
			queue = append(queue, holder{root: heldLabel(b.Name, alias), alias: alias, env: cfg.Env})
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
				queue = append(queue, holder{root: h.root, via: b.Name, alias: h.alias, env: cfg.Env})
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
		switch wanted, on := p.wanted(b); {
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
		Public: runs.Workload != provider.Worker,
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

// link gives a service the variables for what it needs, and orders it
// after them.
func (p *planner) link(name string, a *Action) {
	m := p.in.Manifest
	needs := m.Services[name].Needs
	base := "services." + name + ".needs"
	keys := make([]string, len(needs))
	count := map[string]int{}
	for i, need := range needs {
		if r, ok := m.Resources[need]; ok {
			keys[i] = envKeys[r.Kind]
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
			if rc, sc := p.cloud(dep.Provider), p.cloud(a.Provider); rc != sc {
				p.problem(Finding{
					Code: "unreachable-resource", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s runs on %s, but %s is on %s, and a managed resource is only reachable from its own cloud. Put them on the same one.", name, cloudTitle(sc), need, cloudTitle(rc)),
				})
				continue
			}
			a.Service.Env = append(a.Service.Env, EnvLink{Key: key, From: need, Subject: ResourceSubject})
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
		case dep.Provider == ecsFargate && a.Provider == ecsFargate:
			// Both run in the environment's network, so traffic between
			// them stays in it, public or not.
			link.Address = fmt.Sprintf("http://%s:%d", need, dep.Service.Port)
		case !dep.Service.Public && p.cloud(dep.Provider) != p.cloud(a.Provider):
			p.problem(Finding{
				Code: "private-across-clouds", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s runs on %s, but %s is private on %s, and nothing on another cloud can reach a private service. Make %s public, or run them on the same cloud.", name, cloudTitle(p.cloud(a.Provider)), need, cloudTitle(p.cloud(dep.Provider)), need),
			})
			continue
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
	ecsFargate = "ecs-fargate"
	cloudRun   = "cloud-run"
)

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
		// A held service also runs after what's kept only for it.
		for _, id := range a.DependsOn {
			if r := p.retained[id]; r != nil {
				step = max(step, r.Step+1)
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
	// removes it if the manifest no longer wants it.
	if b.Drift == DriftMissing {
		a.Op, a.Changes = Create, nil
		a.Reason = fmt.Sprintf("%s was deleted outside Loomplane, so it's created again.", a.Name)
		return
	}
	a.Op, a.Changes = "", changes(b.Config, cfg)
	switch {
	case b.Provider != providerName:
		a.Op = Replace
		a.Reason = fmt.Sprintf("%s moves from %s to %s. The new one starts before the old one goes.", a.Name, b.Provider, providerName)
	case b.Region != region:
		a.Op = Replace
		a.Reason = fmt.Sprintf("%s moves from %s to %s, and a region can't change in place.", a.Name, b.Region, region)
	}
	if a.Op == Replace {
		if a.Subject == ResourceSubject {
			p.warn(Finding{
				Code: "data-loss", Subject: ResourceSubject, Name: a.Name, Path: "resources." + a.Name,
				Message: fmt.Sprintf("Replacing %s provisions a new, empty one and then deletes the old one with its data. Loomplane doesn't move data between them.", a.Name),
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
	}
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
		reason := fmt.Sprintf("The manifest no longer has %s.", b.Name)
		if wanted, on := p.wanted(b); wanted {
			if on {
				continue // it couldn't be planned, and a problem says why
			}
			reason = fmt.Sprintf("%s is off in %s.", b.Name, p.in.Environment)
		}
		step := last + 1
		if b.Subject == ResourceSubject {
			step = last + 2
			p.warn(Finding{
				Code: "data-loss", Subject: ResourceSubject, Name: b.Name, Path: "resources",
				Message: fmt.Sprintf("Deleting %s deletes its data.", b.Name),
			})
		}
		if prov, err := p.in.Providers.Get(b.Provider); err == nil {
			p.use(prov)
		}
		p.actions = append(p.actions, &Action{
			ID: id, Op: Delete, Subject: b.Subject, Name: b.Name, Provider: b.Provider, Region: b.Region,
			Engine: b.Engine, Creates: p.creates(b), Reason: reason, Step: step,
		})
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
	p.checkHTTPS(connections)
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
