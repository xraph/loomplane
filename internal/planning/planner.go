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
	// CommitSHA is the commit the plan deploys. A service built from
	// another commit is updated.
	CommitSHA string
	// Connections are the organization's cloud connections, and Chosen
	// the environment's choice of one per cloud. A plan needs a ready
	// connection for every cloud it deploys to.
	Connections []ConnectionOption
	Chosen      map[string]string
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
	}
	for _, b := range in.Bindings {
		p.bindings[actionID(b.Subject, b.Name)] = b
	}
	p.planResources()
	p.planServices()
	p.checkFindings()
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
			p.warn(Finding{
				Code: "plain-http", Subject: ServiceSubject, Name: name, Path: "services." + name,
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
	for _, name := range m.ServiceNames() {
		s := m.Services[name]
		base := "services." + name
		gs := p.match(name, base, s.Source)
		if gs == nil {
			continue
		}
		p.matched[gs.ID] = true
		p.graphServices[name] = gs
		set := s.For(p.in.Environment, m.Defaults)
		if !manifest.On(set.Enabled) {
			continue
		}
		kind := string(gs.Kind)
		if s.Kind != "" {
			kind = s.Kind
		}
		if !graph.ServiceKind(kind).Deployable() {
			p.problem(Finding{
				Code: "not-deployable", Subject: ServiceSubject, Name: name, Path: base + ".source",
				Message: fmt.Sprintf("%s at %s is a %s, which Loomplane doesn't deploy. Set %s.kind if analysis got it wrong.", name, s.Source, kind, base),
			})
			continue
		}
		workload := provider.Workload(kind)
		able := p.in.Providers.Running(workload)
		prov := p.lookup(ServiceSubject, name, base+".provider", set.Provider, able)
		if prov == nil {
			continue
		}
		runs, ok := prov.Running(workload)
		if !ok {
			p.problem(Finding{
				Code: "provider-cant-run", Subject: ServiceSubject, Name: name, Path: base + ".provider",
				Message: fmt.Sprintf("%s doesn't run a %s like %s. %s", prov.Title, kind, name, couldUse(able)),
			})
			continue
		}
		cfg := p.serviceConfig(name, base, gs, prov, runs, set)
		a := &Action{
			ID: actionID(ServiceSubject, name), Subject: ServiceSubject, Name: name,
			Provider: prov.Name, Region: cfg.Region, Engine: prov.Engine(runs), Creates: runs.Creates,
			Service: cfg,
		}
		p.services[name] = a
		p.actions = append(p.actions, a)
	}
	// Needs are settled once every service has its action, since a service
	// can need one planned after it.
	for _, name := range m.ServiceNames() {
		if a := p.services[name]; a != nil {
			p.link(name, a)
		}
	}
	p.order()
}

// match finds the graph service a manifest source names.
func (p *planner) match(name, base, source string) *graph.Service {
	var found []*graph.Service
	for i := range p.in.Graph.Services {
		gs := &p.in.Graph.Services[i]
		if strings.TrimPrefix(gs.ID, "svc:") == source || gs.Entrypoint == source {
			found = append(found, gs)
		}
	}
	if len(found) == 0 {
		// A unit's directory names its one service too.
		for i := range p.in.Graph.Services {
			if gs := &p.in.Graph.Services[i]; gs.Path == source {
				found = append(found, gs)
			}
		}
	}
	switch len(found) {
	case 0:
		p.problem(Finding{
			Code: "service-not-found", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Analysis found no service at %s in this commit.", source),
		})
		return nil
	case 1:
		return found[0]
	}
	ids := make([]string, 0, len(found))
	for _, gs := range found {
		ids = append(ids, strings.TrimPrefix(gs.ID, "svc:"))
	}
	p.problem(Finding{
		Code: "ambiguous-source", Subject: ServiceSubject, Name: name, Path: base + ".source",
		Message: fmt.Sprintf("More than one service is at %s. Name one: %s.", source, strings.Join(ids, ", ")),
	})
	return nil
}

func (p *planner) serviceConfig(name, base string, gs *graph.Service, prov *provider.Provider, runs provider.Runs, set manifest.Settings) *ServiceConfig {
	cfg := &ServiceConfig{
		Provider: prov.Name, Region: p.place(ServiceSubject, name, base+".region", prov, set.Region),
		Workload: runs.Workload, Execution: runs.Execution,
		Service: gs.ID, Source: gs.Entrypoint, Ecosystem: gs.Ecosystem, Framework: gs.Framework, Image: gs.Image,
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
		for _, need := range p.in.Manifest.Services[name].Needs {
			if p.services[need] != nil {
				step = max(step, depth(need, seen)+1)
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
	// A service runs code, so a new commit is a change too.
	newCode := a.Subject == ServiceSubject && b.CommitSHA != "" && p.in.CommitSHA != "" && b.CommitSHA != p.in.CommitSHA
	if newCode {
		a.Changes = append(a.Changes, Change{Field: "commit", From: short(b.CommitSHA), To: short(p.in.CommitSHA)})
	}
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
		if p.services[name] == nil {
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
		if !gs.Deployable || p.matched[gs.ID] {
			continue
		}
		p.warn(Finding{
			Code: "service-left-out", Subject: ServiceSubject, Name: gs.Name, Path: "services",
			Message:  fmt.Sprintf("Analysis found %s (%s at %s), but the manifest doesn't deploy it.", gs.Name, gs.Kind, gs.Entrypoint),
			Evidence: gs.Evidence,
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
		if b.Subject == ServiceSubject && p.services[b.Name] != nil || b.Subject == ResourceSubject && p.resources[b.Name] != nil {
			continue
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

// wanted reports whether the manifest still has a binding's subject, and
// whether it's on in this environment.
func (p *planner) wanted(b *Binding) (wanted, on bool) {
	m := p.in.Manifest
	if b.Subject == ServiceSubject {
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
