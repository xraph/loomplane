package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// EndpointCheck is what checking an external endpoint from a target
// found. Found is false when the organization has no such endpoint; Err
// says why the check couldn't run. Name is the endpoint's name, which
// findings use in place of its ID. Neither Err nor Report holds the DSN.
type EndpointCheck struct {
	ID     string
	Name   string
	Engine string
	Found  bool
	Report *catalogconnect.Report
	Err    string
}

// endpointKey is where Input.Endpoints holds the check of an endpoint from
// one target against one requirement, when the endpoint is checked more
// than once.
func endpointKey(id, connection string, req recipes.Requirement) string {
	extensions := slices.Clone(req.Extensions)
	slices.Sort(extensions)
	return id + "@" + connection + "/" + req.Engine + "/" + req.MinVersion + "/" + strings.Join(extensions, ",")
}

// kinds is the catalog the plan reads its engines and composed kinds from.
func (p *planner) kinds() *recipes.Registry {
	if p.in.Kinds != nil {
		return p.in.Kinds
	}
	return recipes.Default()
}

// planCompositions plans the manifest's composed catalog resources, after
// every other resource, so a dependency connected from the manifest has
// its action already. A composed resource connected as another's
// dependency is planned before it.
func (p *planner) planCompositions(names []string) {
	m := p.in.Manifest
	const visiting, done = 1, 2
	state := map[string]int{}
	var visit func(name string)
	visit = func(name string) {
		state[name] = visiting
		r := m.Resources[name]
		for _, dep := range slices.Sorted(maps.Keys(r.Uses)) {
			used := r.Uses[dep].Resource
			if !slices.Contains(names, used) {
				continue
			}
			switch state[used] {
			case visiting:
				p.problem(Finding{
					Code: "catalog-dependency-cycle", Subject: ResourceSubject, Name: name, Path: "resources." + name + ".uses." + dep + ".resource",
					Message: fmt.Sprintf("%s and %s depend on each other. One of them has to get its %s somewhere else.", name, used, dep),
				})
			case 0:
				visit(used)
			}
		}
		p.planComposed(name)
		state[name] = done
	}
	for _, name := range names {
		if state[name] == 0 {
			visit(name)
		}
	}
}

// planComposed settles a composed resource's provider and plans it.
func (p *planner) planComposed(name string) {
	m := p.in.Manifest
	r := m.Resources[name]
	base := "resources." + name
	kind, ok := p.kinds().Kind(r.Kind)
	if !ok {
		return
	}
	set := r.For(p.in.Environment, m.Defaults)
	var able []*provider.Provider
	for _, prov := range p.in.Providers.List() {
		if OwnedCloud(prov.Cloud) && slices.Contains(kind.Targets, prov.Cloud) {
			able = append(able, prov)
		}
	}
	prov := p.lookup(ResourceSubject, name, base+".provider", set.Provider, able)
	if prov == nil {
		return
	}
	p.planComposition(name, base, r, kind, prov, set)
}

// planComposition expands a composed catalog resource into ordinary
// resource actions: one for each dependency it provisions (step 1), one for
// each component (step 2 plus its start rank), and one for the resource
// itself, after its last component, which publishes its outputs.
func (p *planner) planComposition(name, base string, r manifest.Resource, kind recipes.Kind, prov *provider.Provider, set manifest.ResourceSettings) {
	if !OwnedCloud(prov.Cloud) || !slices.Contains(kind.Targets, prov.Cloud) {
		p.problem(Finding{Code: "catalog-target-unsupported", Subject: ResourceSubject, Name: name, Path: base + ".provider", Message: fmt.Sprintf("%s can't run %s. Choose an existing server.", prov.Title, kind.Title)})
		return
	}
	version := r.Version
	if version == "" && len(kind.Releases) > 0 {
		version = kind.Releases[0].Version
	}
	if !slices.ContainsFunc(kind.Releases, func(release recipes.Release) bool { return release.Version == version }) {
		versions := make([]string, 0, len(kind.Releases))
		for _, release := range kind.Releases {
			versions = append(versions, release.Version)
		}
		p.problem(Finding{
			Code: "unknown-version", Subject: ResourceSubject, Name: name, Path: base + ".version",
			Message: fmt.Sprintf("%s offers %s %s, not %s.", prov.Title, r.Kind, strings.Join(versions, ", "), version),
		})
		return
	}
	connection := p.targetConnection(name, base+".target", "target", prov.Cloud, r.Target)
	if connection == nil {
		return
	}
	if prov.Cloud == "kubernetes" {
		if err := ValidateKubernetesWorkloadSettings(connection.Details); err != nil {
			p.problem(Finding{Code: "catalog-unavailable", Subject: ResourceSubject, Name: name, Path: base + ".target", Message: err.Error()})
			return
		}
	}
	composition, err := p.kinds().Resolve(kind.Name, version, connection.Details["architecture"], prov.Cloud)
	if err != nil {
		p.problem(Finding{Code: "catalog-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: err.Error()})
		return
	}
	if prov.Cloud == "kubernetes" && !p.clusterRuns(name, base, kind, composition, connection) {
		return
	}
	region := p.place(ResourceSubject, name, base+".region", prov, set.Region)
	settings := ReviewedConnectionSettings(connection.Cloud, connection.Details)
	resource := func() *ResourceConfig {
		return &ResourceConfig{Provider: prov.Name, Region: region, Target: connection.ID, TargetSettings: settings, Kind: kind.Name, Version: version}
	}

	choices, uses := p.dependencies(name, base, r, kind, prov, set)

	var parts []string
	last := 1
	for _, c := range composition.Components {
		id := actionID(ResourceSubject, ComponentActionName(name, c.Name))
		if p.componentNameTaken(name, c.Name) {
			continue
		}
		parts = append(parts, id)
		cfg := resource()
		cfg.Component = &ComponentConfig{Kind: kind.Name, Resource: name, Revision: composition.Revision, Component: c, Dependencies: choices, Protection: ComponentProtection(c.Component)}
		creates := componentCreates(kind.Title, c.Component)
		if c.Role != recipes.RoleJob {
			for _, port := range c.Ports {
				if port.Routable && !port.Control {
					cfg.RoutablePorts = append(cfg.RoutablePorts, port.Container)
				}
			}
		}
		dependsOn := slices.Clone(uses)
		if len(c.After) > 0 {
			dependsOn = nil
			for _, after := range c.After {
				dependsOn = append(dependsOn, actionID(ResourceSubject, ComponentActionName(name, after)))
			}
		}
		sort.Strings(dependsOn)
		a := &Action{
			ID: id, Subject: ResourceSubject, Name: ComponentActionName(name, c.Name),
			Provider: prov.Name, ConnectionID: connection.ID, Region: region, Engine: provider.ResourceEngine, Creates: creates,
			Resource: cfg, DependsOn: dependsOn, Step: 2 + c.Step,
		}
		last = max(last, a.Step)
		p.diff(a, cfg, cfg.Provider, cfg.Region)
		p.namedChanges(a)
		p.resources[a.Name] = a
		p.actions = append(p.actions, a)
	}

	cfg := resource()
	cfg.Composition = &CompositionConfig{Kind: kind.Name, Version: version, Revision: composition.Revision, Dependencies: choices, Outputs: kind.Outputs, Secrets: kind.Secrets}
	sort.Strings(parts)
	a := &Action{
		ID: actionID(ResourceSubject, name), Subject: ResourceSubject, Name: name,
		Provider: prov.Name, ConnectionID: connection.ID, Region: region, Engine: provider.ResourceEngine, Creates: kind.Title + " catalog service",
		Resource: cfg, DependsOn: parts, Step: last + 1,
	}
	p.diff(a, cfg, cfg.Provider, cfg.Region)
	p.namedChanges(a)
	p.resources[name] = a
	p.actions = append(p.actions, a)
}

// clusterRuns reports, as problems, what a cluster connection lacks to run
// a composition: retained storage when a component keeps a volume, and Job
// permissions when one is a job. A composition never needs the database
// recovery permissions; a dependency it provisions checks its own.
func (p *planner) clusterRuns(name, base string, kind recipes.Kind, composition *recipes.Composition, connection *ConnectionOption) bool {
	ok := true
	if i := slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Volume != nil }); i >= 0 && ValidateKubernetesDatabaseSettings(connection.Details) != nil {
		p.problem(Finding{
			Code: "catalog-storage-unavailable", Subject: ResourceSubject, Name: name, Path: base + ".target",
			Message: fmt.Sprintf("%s's %s keeps data on a volume. Select and check a retained filesystem storage class for this cluster connection.", kind.Title, composition.Components[i].Name),
		})
		ok = false
	}
	if i := slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Role == recipes.RoleJob }); i >= 0 && connection.Details["jobs"] != "ready" {
		p.problem(Finding{
			Code: "catalog-jobs-unavailable", Subject: ResourceSubject, Name: name, Path: base + ".target",
			Message: fmt.Sprintf("This connection needs namespace Job and pod log permissions to run %s's %s job. Check the connection after granting them.", kind.Title, composition.Components[i].Name),
		})
		ok = false
	}
	return ok
}

// dependencies settles where a composed resource gets each dependency,
// plans the ones it provisions, and returns the choices with the actions
// its first components run after. A bound resource keeps the choices it
// was deployed with: switching one needs a restore Loomplane doesn't do.
func (p *planner) dependencies(name, base string, r manifest.Resource, kind recipes.Kind, prov *provider.Provider, set manifest.ResourceSettings) (choices map[string]DependencyChoice, uses []string) {
	m := p.in.Manifest
	for _, dep := range slices.Sorted(maps.Keys(r.Uses)) {
		if !slices.ContainsFunc(kind.Dependencies, func(d recipes.Dependency) bool { return d.Name == dep }) {
			p.problem(Finding{
				Code: "catalog-dependency-unknown", Subject: ResourceSubject, Name: name, Path: base + ".uses." + dep,
				Message: fmt.Sprintf("%s has no dependency called %s.", kind.Title, dep),
			})
		}
	}
	bound := p.boundChoices(name)
	choices = map[string]DependencyChoice{}
	for _, dep := range kind.Dependencies {
		path := base + ".uses." + dep.Name
		u, chosen := r.Uses[dep.Name]
		on := !dep.Optional || dep.Default
		if chosen && u.Enabled != nil {
			on = *u.Enabled
		}
		if u.Resource != "" || u.External != "" {
			on = true
		}
		if !dep.Optional && !on {
			p.problem(Finding{
				Code: "catalog-dependency-required", Subject: ResourceSubject, Name: name, Path: path + ".enabled",
				Message: fmt.Sprintf("%s always needs %s. Remove enabled: false, or connect one with resource or external.", kind.Title, dep.Title),
			})
			continue
		}
		choice := DependencyChoice{Mode: DependencyOff}
		switch {
		case !on:
		case u.Resource != "":
			choice = DependencyChoice{Mode: DependencyResource, Resource: u.Resource, Kind: m.Resources[u.Resource].Kind}
		case u.External != "":
			// Endpoint checks decide whether it's usable.
			choice = DependencyChoice{Mode: DependencyExternal, External: u.External}
		default:
			choice = DependencyChoice{Mode: DependencyNew, Resource: ComponentActionName(name, dep.Name), Kind: firstKind(dep)}
		}
		if was, ok := bound[dep.Name]; ok && switched(was, choice) {
			p.problem(Finding{
				Code: "catalog-dependency-migration-required", Subject: ResourceSubject, Name: name, Path: path,
				Message: fmt.Sprintf("Switching %s from %s to %s needs a tested restore into the new dependency. The current one stays as it is.", dep.Name, choiceName(was), choiceName(choice)),
			})
			choice = was
		} else {
			p.checkConnected(name, path, dep, choice)
			p.checkExternal(name, path, kind, dep, choice, r.Target, prov.Cloud)
		}
		choices[dep.Name] = choice
		switch choice.Mode {
		case DependencyResource:
			if a := p.resources[choice.Resource]; a != nil {
				uses = append(uses, a.ID)
			}
		case DependencyNew:
			if a := p.provision(name, base, r, dep, choice.Resource, prov, set); a != nil {
				uses = append(uses, a.ID)
			}
		}
	}
	return choices, uses
}

// ComponentProtection is how a component's data is protected: as its
// recipe says, otherwise not at all without a volume, and rebuilt with one.
func ComponentProtection(c recipes.Component) recipes.Protection {
	if c.Protection != "" {
		return c.Protection
	}
	if c.Volume != nil {
		return recipes.ProtectionRebuildable
	}
	return recipes.ProtectionNone
}

func firstKind(dep recipes.Dependency) string {
	if len(dep.Kinds) == 0 {
		return ""
	}
	return dep.Kinds[0]
}

// checkConnected reports a connected manifest resource the dependency
// can't use: one of another kind, one that's off, or one on another target.
func (p *planner) checkConnected(name, path string, dep recipes.Dependency, choice DependencyChoice) {
	if choice.Mode != DependencyResource {
		return
	}
	m := p.in.Manifest
	path += ".resource"
	if !slices.Contains(dep.Kinds, choice.Kind) {
		p.problem(Finding{
			Code: "catalog-connect-kind-mismatch", Subject: ResourceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s needs %s for %s, and %s is %s.", name, strings.Join(dep.Kinds, " or "), dep.Name, choice.Resource, choice.Kind),
		})
		return
	}
	connected := p.resources[choice.Resource]
	if connected == nil {
		if !manifest.On(m.Resources[choice.Resource].For(p.in.Environment, m.Defaults).Enabled) {
			p.problem(Finding{
				Code: "need-off", Subject: ResourceSubject, Name: name, Path: path,
				Message: fmt.Sprintf("%s uses %s, which is off in %s.", name, choice.Resource, p.in.Environment),
			})
		}
		return // otherwise a problem says why it couldn't be planned
	}
	if connected.Resource == nil || !p.sameTarget(name, connected) {
		p.problem(Finding{
			Code: "catalog-connect-across-targets", Subject: ResourceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s and %s must run on the same checked target network.", name, choice.Resource),
		})
	}
}

// checkExternal reports what the check of a connected external endpoint
// found, which was made before planning from the composed resource's
// target. A dependency that takes no external endpoint refuses one.
func (p *planner) checkExternal(name, path string, kind recipes.Kind, dep recipes.Dependency, choice DependencyChoice, connection, cloud string) {
	if choice.Mode != DependencyExternal {
		return
	}
	path += ".external"
	finding := func(code, message string) Finding {
		return Finding{Code: code, Subject: ResourceSubject, Name: name, Path: path, Message: message}
	}
	if dep.External == nil {
		p.problem(finding("catalog-connect-external-unaccepted", fmt.Sprintf("%s can't connect %s to an external endpoint. Provision one, or connect a resource.", kind.Title, dep.Title)))
		return
	}
	p.warn(finding("catalog-endpoint-unmanaged", "Not managed by Loomplane: no backups or restores"))
	if cloud == "kubernetes" {
		p.warn(finding("catalog-endpoint-checked-from-loomplane", "Checked from Loomplane, not from inside the cluster."))
	}
	id := choice.External
	check, ok := p.in.Endpoints[endpointKey(id, connection, *dep.External)]
	if !ok {
		check, ok = p.in.Endpoints[id]
	}
	label := "endpoint " + id
	if check.Name != "" {
		label = "endpoint " + check.Name
	}
	const unavailable, incompatible = "catalog-endpoint-unavailable", "catalog-endpoint-incompatible"
	switch {
	case !ok || (check.Found && check.Err == "" && check.Report == nil):
		p.problem(finding(unavailable, fmt.Sprintf("Loomplane couldn't check %s from %s's target, so %s can't use it. Plan again.", label, name, name)))
	case check.Err != "":
		p.problem(finding(unavailable, fmt.Sprintf("Loomplane couldn't check %s: %s", label, check.Err)))
	case !check.Found:
		p.problem(finding(unavailable, fmt.Sprintf("%s isn't in this organization. Connect another endpoint, or provision one.", label)))
	case check.Engine != "" && check.Engine != dep.External.Engine:
		p.problem(finding(incompatible, fmt.Sprintf("%s runs %s, and %s's %s dependency needs %s.", label, check.Engine, name, dep.Name, dep.External.Engine)))
	default:
		// A check that got no version never reached a usable endpoint.
		code := incompatible
		if check.Report.Version == "" {
			code = unavailable
		}
		for _, problem := range check.Report.Problems {
			p.problem(finding(code, fmt.Sprintf("%s can't be used: %s", label, problem)))
		}
		for _, extension := range check.Report.Missing {
			p.problem(finding(incompatible, fmt.Sprintf("%s lacks the %s extension", label, extension)))
		}
	}
}

// sameTarget reports whether a connected resource runs on the composed
// resource's target network.
func (p *planner) sameTarget(name string, connected *Action) bool {
	r := p.in.Manifest.Resources[name]
	set := r.For(p.in.Environment, p.in.Manifest.Defaults)
	prov, err := p.in.Providers.Get(set.Provider)
	if err != nil {
		return false
	}
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == r.Target })
	if i < 0 {
		return false
	}
	ours := &ServiceConfig{Target: r.Target, TargetSettings: ReviewedConnectionSettings(p.in.Connections[i].Cloud, p.in.Connections[i].Details)}
	theirs := &ServiceConfig{Target: connected.Resource.Target, TargetSettings: connected.Resource.TargetSettings}
	return sameTargetNetwork(ours, theirs, prov.Cloud, p.cloud(connected.Provider))
}

// provision plans a dependency a composed resource gets one of its own of:
// an ordinary database of the dependency's first kind, named for it.
func (p *planner) provision(name, base string, r manifest.Resource, dep recipes.Dependency, action string, prov *provider.Provider, set manifest.ResourceSettings) *Action {
	kind := firstKind(dep)
	if _, ok := p.kinds().Engine(kind); !ok {
		why := "which this Loomplane doesn't know"
		if p.kinds().Composed(kind) {
			why = "which can't be provisioned as a dependency yet"
		}
		p.problem(Finding{
			Code: "catalog-dependency-unavailable", Subject: ResourceSubject, Name: name, Path: base + ".uses." + dep.Name,
			Message: fmt.Sprintf("%s's %s dependency is %s, %s. Connect one with resource or external.", name, dep.Name, kind, why),
		})
		return nil
	}
	if p.componentNameTaken(name, dep.Name) {
		return nil
	}
	offer, ok := prov.Provisioning(kind)
	if !ok {
		p.problem(Finding{
			Code: "provider-cant-provision", Subject: ResourceSubject, Name: action, Path: base + ".provider",
			Message: fmt.Sprintf("%s doesn't provision %s, which %s needs for %s. Connect one with resource or external.", prov.Title, kind, name, dep.Name),
		})
		return nil
	}
	version := dep.Version
	switch {
	case version == "" && len(offer.Versions) > 0:
		version = offer.Versions[0]
	case version != "" && !slices.Contains(offer.Versions, version):
		p.problem(Finding{
			Code: "unknown-version", Subject: ResourceSubject, Name: action, Path: base + ".uses." + dep.Name,
			Message: fmt.Sprintf("%s offers %s %s, not %s.", prov.Title, kind, strings.Join(offer.Versions, ", "), version),
		})
		return nil
	}
	cfg := &ResourceConfig{
		Provider: prov.Name, Region: p.place(ResourceSubject, action, base+".region", prov, set.Region),
		Kind: kind, Version: version,
	}
	a := &Action{
		ID: actionID(ResourceSubject, action), Subject: ResourceSubject, Name: action,
		Provider: prov.Name, Region: cfg.Region, Engine: provider.ResourceEngine, Creates: offer.Creates,
		Resource: cfg, Step: 1,
	}
	p.configureCatalog(action, base, manifest.Resource{Kind: kind, Provider: prov.Name, Target: r.Target}, cfg, a)
	p.diff(a, cfg, cfg.Provider, cfg.Region)
	p.resources[action] = a
	p.actions = append(p.actions, a)
	return a
}

// componentNameTaken reports, as a problem, a name the manifest already
// uses for something else, which a component's or provisioned dependency's
// action would otherwise share a binding with.
func (p *planner) componentNameTaken(parent, part string) bool {
	m := p.in.Manifest
	name := ComponentActionName(parent, part)
	path := ""
	subject := ResourceSubject
	if _, ok := m.Resources[name]; ok {
		path = "resources." + name
	} else if _, ok := m.Services[name]; ok {
		path, subject = "services."+name, ServiceSubject
	} else if _, ok := m.Gateways[name]; ok {
		path, subject = "gateways."+name, ServiceSubject
	} else if _, ok := m.Links[name]; ok {
		path = "links." + name
	} else if p.resources[name] == nil {
		return false
	}
	p.problem(Finding{
		Code: "catalog-component-name-taken", Subject: subject, Name: name, Path: path,
		Message: fmt.Sprintf("%s is the name Loomplane gives %s's %s. Rename one of them.", name, parent, part),
	})
	return true
}

// boundChoices are the dependency choices a composed resource was last
// deployed with.
func (p *planner) boundChoices(name string) map[string]DependencyChoice {
	b := p.bindings[actionID(ResourceSubject, name)]
	if b == nil {
		return nil
	}
	var cfg ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil || cfg.Composition == nil {
		return nil
	}
	return cfg.Composition.Dependencies
}

// switched reports whether a dependency moves from one database or
// endpoint to another. Turning an optional one on or off isn't a move.
func switched(was, now DependencyChoice) bool {
	if was.Mode == DependencyOff || now.Mode == DependencyOff {
		return false
	}
	return was.Mode != now.Mode || was.Resource != now.Resource || was.External != now.External
}

func choiceName(c DependencyChoice) string {
	if c.Resource != "" {
		return c.Resource
	}
	return c.External
}

// namedChanges says what changed in a component or composition by name:
// its recipe revision and image, in place of one change to the whole
// configuration.
func (p *planner) namedChanges(a *Action) {
	b := p.bindings[a.ID]
	i := slices.IndexFunc(a.Changes, func(c Change) bool { return c.Field == "component" || c.Field == "composition" })
	if b == nil || i < 0 {
		return
	}
	var old ResourceConfig
	if json.Unmarshal(b.Config, &old) != nil {
		return
	}
	var named []Change
	var was, now any
	switch {
	case a.Resource.Component != nil && old.Component != nil:
		w, n := *old.Component, *a.Resource.Component
		if w.Revision != n.Revision {
			named = append(named, Change{Field: "recipe revision", From: w.Revision, To: n.Revision})
		}
		if w.Component.Image != n.Component.Image {
			named = append(named, Change{Field: "image", From: w.Component.Image, To: n.Component.Image})
		}
		w.Revision, n.Revision = "", ""
		w.Component.Image, n.Component.Image = "", ""
		was, now = w, n
	case a.Resource.Composition != nil && old.Composition != nil:
		w, n := *old.Composition, *a.Resource.Composition
		if w.Revision != n.Revision {
			named = append(named, Change{Field: "recipe revision", From: w.Revision, To: n.Revision})
		}
		w.Revision, n.Revision = "", ""
		was, now = w, n
	default:
		return
	}
	if render(was) == render(now) {
		a.Changes = slices.Delete(a.Changes, i, i+1)
	}
	a.Changes = append(a.Changes, named...)
	sort.SliceStable(a.Changes, func(i, j int) bool { return a.Changes[i].Field < a.Changes[j].Field })
}

// linkComposition gives a service a variable for each of a composed
// resource's outputs: its primary output under "url", the others by name,
// and per-consumer ones issued to the service itself. A key two of the
// service's needs would share is named for the need instead.
func (p *planner) linkComposition(a *Action, need string, outputs []recipes.Output, count map[string]int) {
	for i, o := range outputs {
		key := o.EnvKey
		if key == "" || count[key] > 1 {
			key = envName(need) + "_" + envName(o.Name)
		}
		field := "output:" + o.Name
		switch {
		case o.PerConsumer:
			field = "consumer:" + o.Name
		case i == 0:
			field = "url"
		}
		a.Service.Env = append(a.Service.Env, EnvLink{Key: key, From: need, Subject: ResourceSubject, Field: field})
	}
}

func componentCreates(title string, c recipes.Component) string {
	if c.Role == recipes.RoleJob {
		return fmt.Sprintf("%s %s job", title, c.Name)
	}
	return fmt.Sprintf("%s %s container", title, c.Name)
}

// composedCreates names what a composed resource's binding made, or is
// empty for any other resource.
func (p *planner) composedCreates(cfg ResourceConfig) string {
	title := cfg.Kind
	if kind, ok := p.kinds().Kind(cfg.Kind); ok {
		title = kind.Title
	}
	switch {
	case cfg.Component != nil:
		return componentCreates(title, cfg.Component.Component.Component)
	case cfg.Composition != nil:
		return title + " catalog service"
	}
	return ""
}

// composedBinding is a binding that's part of a composed resource.
type composedBinding struct {
	// parent is the composed resource's manifest name.
	parent string
	// last says it's removed after the components: the resource itself,
	// which holds its secrets, and the dependencies it provisioned.
	last bool
	// stateless says deleting it loses no data.
	stateless bool
}

// composedBindings finds the bindings that are part of a composed
// resource, by action ID.
func (p *planner) composedBindings() map[string]composedBinding {
	out := map[string]composedBinding{}
	for _, id := range slices.Sorted(maps.Keys(p.bindings)) {
		b := p.bindings[id]
		var cfg ResourceConfig
		if b.Subject != ResourceSubject || json.Unmarshal(b.Config, &cfg) != nil {
			continue
		}
		switch {
		case cfg.Component != nil:
			out[id] = composedBinding{parent: cfg.Component.Resource, stateless: cfg.Component.Protection != recipes.ProtectionRebuildable}
		case cfg.Composition != nil:
			out[id] = composedBinding{parent: b.Name, last: true, stateless: true}
			for _, choice := range cfg.Composition.Dependencies {
				if choice.Mode == DependencyNew {
					out[actionID(ResourceSubject, choice.Resource)] = composedBinding{parent: b.Name, last: true}
				}
			}
		}
	}
	return out
}
