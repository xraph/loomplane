// Package provider describes deployment targets: what each one can run,
// how it runs it, and which backing resources it can provision. The
// planner reads these descriptions; it never asks a cloud.
//
// A description says what a target can do, not whether Loomplane can drive
// it yet. Available says that, so a plan can name a target before its
// adapter exists and say plainly that it can't be carried out.
package provider

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Workload is a kind of service a target runs. The names are repository
// analysis's service kinds.
type Workload string

// Workloads.
const (
	// HTTPService serves HTTP or gRPC.
	HTTPService Workload = "http-service"
	// WebApp is a server-rendered web framework app, such as Next.js.
	WebApp Workload = "web-app"
	// StaticSite is files a CDN serves as they are.
	StaticSite Workload = "static-site"
	// Worker is a long-running process that serves nothing.
	Worker Workload = "worker"
)

// Execution is how a target runs a workload.
type Execution string

// Executions.
const (
	// Container is a long-running container instance.
	Container Execution = "container"
	// Serverless is functions started per request, with static assets on
	// a CDN.
	Serverless Execution = "serverless"
	// Static is files on a CDN.
	Static Execution = "static"
)

// Model is how a target's deployments behave, which decides who manages
// their lifecycle.
type Model string

// Models.
const (
	// Instance targets run something you start, stop, scale and replace:
	// Ctrl Plane's model, so Ctrl Plane manages them.
	Instance Model = "instance"
	// Build targets turn each deployment into an immutable build behind a
	// URL. There's no instance to start or stop, so a Loomplane adapter
	// manages them instead of forcing them into Ctrl Plane's model.
	Build Model = "build"
	// Managed targets provision a backing resource, such as a database.
	// Ctrl Plane has no model for those, so Loomplane does.
	Managed Model = "managed"
)

// Engine is the system that carries out a target's actions.
type Engine string

// Engines.
const (
	CtrlPlane Engine = "ctrlplane"
	Loomplane Engine = "loomplane"
)

// Runs is one workload a target can run.
type Runs struct {
	Workload  Workload  `json:"workload"`
	Execution Execution `json:"execution"`
	// Creates names what a deployment creates, such as "Cloud Run service".
	Creates string `json:"creates"`
}

// Provisions is one resource kind a target can provision. Loomplane
// provisions it, whatever the target's model.
type Provisions struct {
	// Kind is a resource kind as repository analysis names it: postgres,
	// redis and so on.
	Kind string `json:"kind"`
	// Creates names what it creates, such as "Cloud SQL for PostgreSQL
	// instance".
	Creates string `json:"creates"`
	// Versions are the engine versions it offers, newest first. Empty
	// means the target doesn't let you choose.
	Versions []string `json:"versions,omitempty"`
	// ManagedProfiles are reviewed requests for standalone managed databases.
	ManagedProfiles []ManagedDatabaseProfile `json:"managed_profiles,omitempty"`
}

// Provider describes one deployment target.
type Provider struct {
	// Name is how a manifest names it, such as "cloud-run".
	Name  string `json:"name"`
	Title string `json:"title"`
	// Cloud is the account a connection to it authenticates: gcp, aws,
	// vercel, render. Targets on one cloud share a connection.
	Cloud       string       `json:"cloud"`
	Description string       `json:"description"`
	Model       Model        `json:"model"`
	Runs        []Runs       `json:"runs,omitempty"`
	Provisions  []Provisions `json:"provisions,omitempty"`
	// Infrastructure lists separately implemented account capabilities. It does
	// not change this target's deployment model or runtime.
	Infrastructure []InfrastructureCapability `json:"infrastructure,omitempty"`
	// Regions are the regions Loomplane offers, the default first. Empty
	// means the target places things itself.
	Regions []string `json:"regions,omitempty"`
	// Scaling says whether you choose how many instances run and how big
	// each is. Targets that scale on their own ignore both.
	Scaling bool `json:"scaling"`
	// BuildLogs is streamed, polled or unavailable for one build operation.
	BuildLogs string `json:"build_logs,omitempty"`
	LogNote   string `json:"log_note,omitempty"`
	// Available says whether Loomplane can carry out this target's actions
	// yet. A plan can still name it; it just can't be applied.
	Available bool `json:"available"`
	// Note says what's missing, when it isn't available.
	Note string `json:"note,omitempty"`
}

// Engine returns who manages a deployment of r: Ctrl Plane for a container
// on an instance target, Loomplane for everything else. Resources are
// always Loomplane's (ResourceEngine).
func (p *Provider) Engine(r Runs) Engine {
	if p.Model == Instance && r.Execution == Container {
		return CtrlPlane
	}
	return Loomplane
}

// ResourceEngine is who provisions resources: Loomplane, since Ctrl Plane
// models instances and nothing else.
const ResourceEngine = Loomplane

// DefaultRegion is the region used when a manifest names none.
func (p *Provider) DefaultRegion() string {
	if len(p.Regions) == 0 {
		return ""
	}
	return p.Regions[0]
}

// Placed reports whether the target lets you choose a region.
func (p *Provider) Placed() bool { return len(p.Regions) > 0 }

// HasRegion reports whether region is one the target offers.
func (p *Provider) HasRegion(region string) bool { return slices.Contains(p.Regions, region) }

// Running returns how the target runs w, if it does.
func (p *Provider) Running(w Workload) (Runs, bool) {
	for _, r := range p.Runs {
		if r.Workload == w {
			return r, true
		}
	}
	return Runs{}, false
}

// Provisioning returns how the target provisions kind, if it does.
func (p *Provider) Provisioning(kind string) (Provisions, bool) {
	for _, r := range p.Provisions {
		if r.Kind == kind {
			return r, true
		}
	}
	return Provisions{}, false
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Validate checks a description is complete.
func (p *Provider) Validate() error {
	switch {
	case !nameRE.MatchString(p.Name):
		return fmt.Errorf("provider %q: the name must be lower case letters, digits and hyphens", p.Name)
	case p.Title == "" || p.Cloud == "":
		return fmt.Errorf("provider %s: title and cloud are required", p.Name)
	case len(p.Runs) == 0 && len(p.Provisions) == 0:
		return fmt.Errorf("provider %s runs nothing and provisions nothing", p.Name)
	case p.Model == Managed && len(p.Runs) > 0:
		return fmt.Errorf("provider %s: a managed target provisions resources; it doesn't run services", p.Name)
	case p.Model != Managed && p.Model != Instance && p.Model != Build:
		return fmt.Errorf("provider %s: unknown model %q", p.Name, p.Model)
	case !p.Available && p.Note == "":
		return fmt.Errorf("provider %s isn't available, so it needs a note saying why", p.Name)
	}
	for _, r := range p.Runs {
		if r.Creates == "" || r.Execution == "" {
			return fmt.Errorf("provider %s: running %s needs creates and execution", p.Name, r.Workload)
		}
	}
	for _, r := range p.Provisions {
		if r.Kind == "" || r.Creates == "" {
			return fmt.Errorf("provider %s: each resource needs a kind and creates", p.Name)
		}
	}
	return nil
}

// ErrUnknown is returned for a provider no registry entry has.
var ErrUnknown = errors.New("unknown provider")

// Registry holds the targets Loomplane knows, in the order they were
// registered.
type Registry struct {
	list   []*Provider
	byName map[string]*Provider
}

// NewRegistry returns a registry holding providers, or an error if one is
// invalid or two share a name.
func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{byName: map[string]*Provider{}}
	for i := range providers {
		if err := r.Register(providers[i]); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a provider.
func (r *Registry) Register(p Provider) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, dup := r.byName[p.Name]; dup {
		return fmt.Errorf("provider %s is registered twice", p.Name)
	}
	p.Runs = slices.Clone(p.Runs)
	p.Provisions = slices.Clone(p.Provisions)
	p.Regions = slices.Clone(p.Regions)
	p.Infrastructure = slices.Clone(p.Infrastructure)
	r.list = append(r.list, &p)
	r.byName[p.Name] = &p
	return nil
}

// Get returns the provider called name.
func (r *Registry) Get(name string) (*Provider, error) {
	p, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	return p, nil
}

// List returns every provider, in registration order. The slice is a
// copy; the providers aren't, so don't change them.
func (r *Registry) List() []*Provider { return slices.Clone(r.list) }

// Names returns every provider's name, in registration order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.list))
	for _, p := range r.list {
		out = append(out, p.Name)
	}
	return out
}

// Running returns the providers that run w, in registration order.
func (r *Registry) Running(w Workload) []*Provider {
	var out []*Provider
	for _, p := range r.list {
		if _, ok := p.Running(w); ok {
			out = append(out, p)
		}
	}
	return out
}

// Provisioning returns the providers that provision kind.
func (r *Registry) Provisioning(kind string) []*Provider {
	var out []*Provider
	for _, p := range r.list {
		if _, ok := p.Provisioning(kind); ok {
			out = append(out, p)
		}
	}
	return out
}
