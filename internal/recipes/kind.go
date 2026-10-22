package recipes

import (
	"io/fs"
	"maps"
	"slices"
	"time"
)

// Placement says where a catalog resource lives.
type Placement string

const (
	PlacementServiceProject Placement = "service-project" // standalone project, as databases are today
	PlacementAppProject     Placement = "app-project"     // added into an existing project and environment
)

// Role says how a component runs.
type Role string

const (
	RoleProcess Role = "process" // long-running; needs Readiness
	RoleJob     Role = "job"     // runs to completion once per changed revision
)

// Protection says what recovery can do for a component's data.
type Protection string

const (
	ProtectionNative      Protection = "native"      // engine with a DatabaseBackend
	ProtectionNone        Protection = "none"        // stateless: not backed up, nothing to restore
	ProtectionRebuildable Protection = "rebuildable" // has a volume; rebuilt, never backed up
	ProtectionExternal    Protection = "external"    // not managed by Loomplane
)

// Kind is a catalog kind composed of components, the dependencies they
// connect to and the secrets Loomplane generates for them. A kind lives in
// its own file and registers itself with MustRegister from init.
type Kind struct {
	Name         string            `json:"name"` // manifest kind
	Title        string            `json:"title"`
	Description  string            `json:"description,omitempty"`
	Placement    Placement         `json:"placement"`
	Targets      []string          `json:"targets"`  // subset of "server", "kubernetes"
	Releases     []Release         `json:"releases"` // newest first; Releases[0].Version is the default
	Dependencies []Dependency      `json:"dependencies,omitempty"`
	Secrets      []GeneratedSecret `json:"secrets,omitempty"`
	Components   []Component       `json:"components"`
	Outputs      []Output          `json:"outputs,omitempty"` // Outputs[0] is the primary, kept under the resource's "url"
}

// Release pins every component's image for one manifest version.
type Release struct {
	Version  string           `json:"version"`  // manifest version, such as "0.9"
	Product  string           `json:"product"`  // exact upstream version, such as "0.9.3"
	Revision string           `json:"revision"` // bump on any change to this release
	Images   map[string]Image `json:"images"`   // by Component.Name
}

// Image is a repository pinned by digest for each architecture.
type Image struct {
	Repository string            `json:"repository"`
	Digests    map[string]string `json:"digests"` // "amd64" and "arm64" to "sha256:<64 hex>"; both required
}

// Component is one container of a composition.
type Component struct {
	Name       string        `json:"name"` // DNS label, no "--"
	Role       Role          `json:"role"`
	Command    []string      `json:"command,omitempty"`
	Args       []string      `json:"args,omitempty"`
	Ports      []Port        `json:"ports,omitempty"` // processes only
	Env        []EnvVar      `json:"env,omitempty"`
	Files      []File        `json:"files,omitempty"` // rendered by CatalogHooks.Files
	Readiness  *HTTPProbe    `json:"readiness,omitempty"`
	Volume     *Volume       `json:"volume,omitempty"` // nil means stateless
	Protection Protection    `json:"protection,omitempty"`
	After      []string      `json:"after,omitempty"` // components that must be ready or have succeeded first
	Resources  Resources     `json:"resources"`
	Timeout    time.Duration `json:"timeout,omitempty"` // jobs only
}

// Port is a container port a process listens on.
type Port struct {
	Name      string `json:"name"` // DNS label: "http", "admin"
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`           // "http" or "tcp"
	Control   bool   `json:"control,omitempty"`  // reached only by Loomplane over the private channel; never routed publicly
	Routable  bool   `json:"routable,omitempty"` // may receive a public HTTP route; requires Protocol "http"; never with Control
}

// HTTPProbe is the readiness check of a process.
type HTTPProbe struct {
	Port string `json:"port"` // a Port.Name with Protocol "http"
	Path string `json:"path"` // "/readyz"
}

// Volume is a component's data directory. Its data is rebuilt, never
// backed up.
type Volume struct {
	MountPath        string `json:"mount_path"`
	KubernetesSizeMB int    `json:"kubernetes_size_mb,omitempty"` // PVC request on Kubernetes; servers ignore it
}

// Resources is a component's CPU and memory request.
type Resources struct {
	CPUMillis int `json:"cpu_millis"`
	MemoryMB  int `json:"memory_mb"`
}

// EnvVar sets exactly one of Value, Secret and From.
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret string `json:"secret,omitempty"` // a GeneratedSecret.Name
	From   *Ref   `json:"from,omitempty"`
}

// Ref names exactly one of a dependency or a sibling component.
type Ref struct {
	Dependency string `json:"dependency,omitempty"` // a Dependency.Name
	Component  string `json:"component,omitempty"`  // a sibling Component.Name
	Field      string `json:"field"`                // for dependencies a Field constant; for components "url:<port name>" or "address:<port name>"
}

// Fields a Ref can read from a dependency.
const (
	FieldURL      = "url" // full connection URL with credentials
	FieldHost     = "host"
	FieldPort     = "port"
	FieldHostPort = "hostport" // "host:port"
	FieldUsername = "username"
	FieldPassword = "password"
	FieldDatabase = "database"
)

// File is a file CatalogHooks.Files renders into a component.
type File struct {
	Path string      `json:"path"`           // absolute, no "..", not under /proc, /sys or /dev
	Mode fs.FileMode `json:"mode,omitempty"` // default 0o444
}

// Dependency is a resource a composition connects to: one Loomplane
// provisions, one already in the manifest, or an external endpoint.
type Dependency struct {
	Name     string       `json:"name"` // "postgres", "redis", "falkordb"
	Title    string       `json:"title"`
	Kinds    []string     `json:"kinds"`             // manifest kinds accepted for a connected resource; Kinds[0] is provisioned for "new"
	Version  string       `json:"version,omitempty"` // version of Kinds[0] to provision; "" is that kind's default
	Optional bool         `json:"optional,omitempty"`
	Default  bool         `json:"default,omitempty"`  // optional only: on unless uses.<name>.enabled is false
	External *Requirement `json:"external,omitempty"` // nil: external endpoints are refused
}

// Requirement is what an external endpoint must offer.
type Requirement struct {
	Engine     string   `json:"engine"` // prober name: "postgres" or "redis"
	MinVersion string   `json:"min_version,omitempty"`
	Extensions []string `json:"extensions,omitempty"` // postgres: must be in pg_available_extensions
}

// GeneratedSecret is a random value Loomplane generates once and keeps
// in Vault.
type GeneratedSecret struct {
	Name     string `json:"name"`               // "admin-password"
	Bytes    int    `json:"bytes,omitempty"`    // random bytes, default 32
	Encoding string `json:"encoding,omitempty"` // "base64url" (default) or "hex"
}

// Output is a value a consuming service gets.
type Output struct {
	Name        string `json:"name"`    // DNS label; Outputs[0] conventionally "url"
	EnvKey      string `json:"env_key"` // "OCTOPUS_URL"
	From        *Ref   `json:"from,omitempty"`
	PerConsumer bool   `json:"per_consumer,omitempty"` // issued per consuming service by the kind's ConsumerBinder
	Secret      bool   `json:"secret,omitempty"`
}

// Engine is a stateful engine that reuses a native adapter family: its
// recipes run, back up and restore exactly as the family's do.
type Engine struct {
	Kind      string          `json:"kind"`   // "timescaledb"
	Family    string          `json:"family"` // "postgres", "mysql", "mariadb", "redis" or "valkey"
	Title     string          `json:"title"`
	EnvKey    string          `json:"env_key"`
	Placement Placement       `json:"placement,omitempty"` // default PlacementServiceProject
	Targets   []string        `json:"targets"`             // "kubernetes" allowed only for Family "postgres"
	Releases  []EngineRelease `json:"releases"`

	// builtin engines resolve through Lookup and KubernetesPostgres.
	builtin bool
}

// EngineRelease pins an engine image for one manifest version.
type EngineRelease struct {
	Version            string `json:"version"`
	Product            string `json:"product"`
	Revision           string `json:"revision"`
	Image              Image  `json:"image"`
	MountPath          string `json:"mount_path"`
	BackupFormat       string `json:"backup_format"` // must equal the family's full-backup format
	KubernetesVolumeMB int    `json:"kubernetes_volume_mb,omitempty"`
}

// Composition is a kind resolved for one release, architecture and target.
type Composition struct {
	Kind         string              `json:"kind"`
	Version      string              `json:"version"`
	Product      string              `json:"product"`
	Revision     string              `json:"revision"`
	Architecture string              `json:"architecture"`
	Target       string              `json:"target"`
	Components   []ResolvedComponent `json:"components"` // start order
}

// ResolvedComponent is a component with its pinned image and start rank.
type ResolvedComponent struct {
	Component
	Image string `json:"image"` // repository@sha256 for the architecture
	Step  int    `json:"step"`  // 0-based start rank
}

// KindInfo describes a kind or engine for people choosing one.
type KindInfo struct {
	Name         string           `json:"name"`
	Title        string           `json:"title"`
	Description  string           `json:"description,omitempty"`
	Placement    Placement        `json:"placement"`
	Targets      []string         `json:"targets"`
	Versions     []string         `json:"versions"`
	Engine       bool             `json:"engine"`
	Dependencies []DependencyInfo `json:"dependencies,omitempty"`
	Components   []ComponentInfo  `json:"components,omitempty"`
	Outputs      []OutputInfo     `json:"outputs,omitempty"`
}

// DependencyInfo is the public part of a Dependency.
type DependencyInfo struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Kinds    []string `json:"kinds"`
	Optional bool     `json:"optional"`
	Default  bool     `json:"default"`
	External bool     `json:"external"` // external endpoints are accepted
}

// ComponentInfo is the public part of a Component.
type ComponentInfo struct {
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	Stateful   bool       `json:"stateful"`
	Protection Protection `json:"protection"`
}

// OutputInfo is the public part of an Output.
type OutputInfo struct {
	Name        string `json:"name"`
	EnvKey      string `json:"env_key"`
	PerConsumer bool   `json:"per_consumer"`
}

// clone copies every slice, map and pointer so a registered kind never
// shares memory with its caller.
func (k Kind) clone() Kind {
	k.Targets = slices.Clone(k.Targets)
	k.Releases = slices.Clone(k.Releases)
	for i := range k.Releases {
		images := make(map[string]Image, len(k.Releases[i].Images))
		for name, image := range k.Releases[i].Images {
			images[name] = image.clone()
		}
		k.Releases[i].Images = images
	}
	k.Dependencies = slices.Clone(k.Dependencies)
	for i := range k.Dependencies {
		d := &k.Dependencies[i]
		d.Kinds = slices.Clone(d.Kinds)
		if d.External != nil {
			ext := *d.External
			ext.Extensions = slices.Clone(ext.Extensions)
			d.External = &ext
		}
	}
	k.Secrets = slices.Clone(k.Secrets)
	k.Components = slices.Clone(k.Components)
	for i := range k.Components {
		k.Components[i] = k.Components[i].clone()
	}
	k.Outputs = slices.Clone(k.Outputs)
	for i := range k.Outputs {
		k.Outputs[i].From = k.Outputs[i].From.clone()
	}
	return k
}

func (c Component) clone() Component {
	c.Command = slices.Clone(c.Command)
	c.Args = slices.Clone(c.Args)
	c.Ports = slices.Clone(c.Ports)
	c.Env = slices.Clone(c.Env)
	for i := range c.Env {
		c.Env[i].From = c.Env[i].From.clone()
	}
	c.Files = slices.Clone(c.Files)
	if c.Readiness != nil {
		probe := *c.Readiness
		c.Readiness = &probe
	}
	if c.Volume != nil {
		volume := *c.Volume
		c.Volume = &volume
	}
	c.After = slices.Clone(c.After)
	return c
}

func (r *Ref) clone() *Ref {
	if r == nil {
		return nil
	}
	ref := *r
	return &ref
}

func (i Image) clone() Image {
	i.Digests = maps.Clone(i.Digests)
	return i
}

func (e Engine) clone() Engine {
	e.Targets = slices.Clone(e.Targets)
	e.Releases = slices.Clone(e.Releases)
	for i := range e.Releases {
		e.Releases[i].Image = e.Releases[i].Image.clone()
	}
	return e
}
