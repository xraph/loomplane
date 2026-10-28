package recipes

import (
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"sync"
	"time"
)

// Placement says where a catalog resource lives.
type Placement string

const (
	PlacementServiceProject Placement = "service-project" // standalone project, as databases are today
	PlacementAppProject     Placement = "app-project"     // added into an existing project and environment
	// PlacementDependency kinds are provisioned only as another kind's
	// dependency: one process, no dependencies of their own and no
	// per-consumer outputs. Providers don't offer them on their own.
	PlacementDependency Placement = "dependency"
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
	// ConsumerOptions are the values a consuming service chooses when it
	// links, such as a tenant, passed to the kind's ConsumerBinder.
	ConsumerOptions []ConsumerOption `json:"consumer_options,omitempty"`
	// Feature is the server feature the kind belongs to, such as
	// FeatureAI, or "" for none. Planning refuses the kind while that
	// feature is off on the server, and the catalog doesn't list it.
	Feature string `json:"feature,omitempty"` // ^[a-z]{0,16}$
	// Settings are the values a resource of the kind sets under
	// resources.<name>.settings. Planning checks them and hands every
	// one, defaults filled, to each component.
	Settings []Setting `json:"settings,omitempty"`
	// EnvironmentOptIn makes a resource of the kind run only in the
	// environments where environments.<env>.enabled is true. Everywhere
	// else planning treats it as off, as an explicit enabled: false.
	EnvironmentOptIn bool `json:"environment_opt_in,omitempty"`
}

// Setting is a value a resource of the kind sets under
// resources.<name>.settings.<Name>. A setting is copied verbatim into
// plans, bindings and deployment runs, so a kind must never take a secret
// as one.
type Setting struct {
	Name     string `json:"name"` // ^[a-z][a-z0-9_]{0,31}$
	Title    string `json:"title"`
	Pattern  string `json:"pattern"` // a Go regexp the whole value must match; required
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"` // must match Pattern
}

// Matches reports whether value matches the setting's pattern as a whole.
// A pattern that doesn't compile matches nothing.
func (s Setting) Matches(value string) bool {
	re := settingPattern(s.Pattern)
	return re != nil && re.MatchString(value)
}

// settingPatterns holds each setting pattern compiled once, anchored, by
// its source; nil for one that doesn't compile.
var settingPatterns sync.Map // string -> *regexp.Regexp

func settingPattern(pattern string) *regexp.Regexp {
	if v, ok := settingPatterns.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re
		}
		return nil
	}
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		re = nil
	}
	settingPatterns.Store(pattern, re)
	return re
}

// FeatureAI is the feature of the kinds that need the server's AI section
// (config ai.enabled).
const FeatureAI = "ai"

// ConsumerOption is a value a consuming service sets for its link, under
// services.<name>.need_options.<need>.<Name>.
type ConsumerOption struct {
	Name     string `json:"name"` // ^[a-z][a-z0-9_]{0,31}$
	Title    string `json:"title"`
	Pattern  string `json:"pattern,omitempty"` // a Go regexp the value must match
	Required bool   `json:"required,omitempty"`
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
	Name      string     `json:"name"` // DNS label, no "--"
	Role      Role       `json:"role"`
	Command   []string   `json:"command,omitempty"`
	Args      []string   `json:"args,omitempty"`
	Ports     []Port     `json:"ports,omitempty"` // processes only
	Env       []EnvVar   `json:"env,omitempty"`
	Files     []File     `json:"files,omitempty"` // rendered by CatalogHooks.Files
	Readiness *HTTPProbe `json:"readiness,omitempty"`
	Volume    *Volume    `json:"volume,omitempty"` // nil means stateless
	// WeightCache is a volume for downloaded files; only GPU components have one.
	WeightCache *WeightCache  `json:"weight_cache,omitempty"`
	Protection  Protection    `json:"protection,omitempty"`
	After       []string      `json:"after,omitempty"` // components that must be ready or have succeeded first
	Resources   Resources     `json:"resources"`
	Timeout     time.Duration `json:"timeout,omitempty"` // jobs only
	// StartTimeout is how long the runtime waits for a process to become
	// ready: 0 keeps the runtime's default, else 1 minute to 3 hours.
	// Processes only.
	StartTimeout time.Duration `json:"start_timeout,omitempty"`
	// KubernetesRules is the read-only discovery access a process gets
	// through its own ServiceAccount on Kubernetes.
	KubernetesRules []KubernetesRule `json:"kubernetes_rules,omitempty"`
	// Requires names an optional dependency the component runs only with.
	Requires string `json:"requires,omitempty"`
}

// KubernetesRule is one rule of a process's Kubernetes Role. Only
// discovery is allowed: groups "" and "discovery.k8s.io", resources
// services, endpoints and endpointslices, verbs get, list and watch.
type KubernetesRule struct {
	APIGroups []string `json:"api_groups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// Port is a container port a process listens on.
type Port struct {
	Name      string `json:"name"` // DNS label: "http", "admin"
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`           // "http", "tcp", "redis" (RESP) or "memcached" (ASCII)
	Control   bool   `json:"control,omitempty"`  // reached only by Loomplane over the private channel; never routed publicly
	Routable  bool   `json:"routable,omitempty"` // may receive a public HTTP route; requires Protocol "http"; never with Control
}

// HTTPProbe checks a process with HTTP GET, Redis PING or Memcached
// VERSION. Native protocol probes have no path.
type HTTPProbe struct {
	Port string `json:"port"` // a Port.Name with Protocol "http", "redis" or "memcached"
	Path string `json:"path"` // "/readyz"; "" on a native protocol port
}

// Volume is a component's data directory. Its data is rebuilt, never
// backed up.
type Volume struct {
	MountPath        string `json:"mount_path"`
	KubernetesSizeMB int    `json:"kubernetes_size_mb,omitempty"` // PVC request on Kubernetes; servers ignore it
	FSGroup          int    `json:"fs_group,omitempty"`           // group that owns the volume on Kubernetes; 0 leaves it alone
}

// GPU is a component's GPU request. Kind "" lets the resource choose with its gpu setting.
type GPU struct {
	Kind         string `json:"kind,omitempty"`           // a provider.AcceleratorKinds ID, NVIDIA only
	Count        int    `json:"count"`                    // default devices, 1 to 8; the resource may ask for more
	MinMemoryGiB int    `json:"min_memory_gib,omitempty"` // per-device floor the chosen kind must meet
}

// Resources is a component's CPU, memory and GPU request.
type Resources struct {
	CPUMillis int  `json:"cpu_millis"`
	MemoryMB  int  `json:"memory_mb"`
	GPU       *GPU `json:"gpu,omitempty"`
	ShmMB     int  `json:"shm_mb,omitempty"` // /dev/shm size; counts toward MemoryMB
	// PerGPU is added once for each GPU the component is placed with, on
	// top of the rest (see ForGPUs). Only with GPU.
	PerGPU *ResourceStep `json:"per_gpu,omitempty"`
}

// ResourceStep is what a component needs for each GPU it gets.
type ResourceStep struct {
	CPUMillis int `json:"cpu_millis,omitempty"`
	MemoryMB  int `json:"memory_mb,omitempty"`
	ShmMB     int `json:"shm_mb,omitempty"`
}

// MaxVolumeMB is the largest volume, in MB, a runtime asks for.
const MaxVolumeMB = 1 << 30

// WeightCache is a persistent volume for downloaded files, such as model
// weights, kept across restarts and releases so they aren't fetched again.
type WeightCache struct {
	MountPath        string `json:"mount_path"`
	KubernetesSizeMB int    `json:"kubernetes_size_mb"` // PVC request; required on Kubernetes targets
	// SizeSetting names one of the kind's Settings whose value, in MB
	// (digits), raises the PVC request above KubernetesSizeMB. It never
	// lowers it.
	SizeSetting string `json:"size_setting,omitempty"`
}

// EnvVar sets exactly one of Value, Secret, From and Template.
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret string `json:"secret,omitempty"` // a GeneratedSecret.Name
	From   *Ref   `json:"from,omitempty"`
	// Template is text with ${dep:<dependency>:<field>}, ${secret:<name>}
	// and ${component:<name>:<field>} placeholders.
	Template string `json:"template,omitempty"`
	// Requires names an optional dependency the variable is set only with.
	// A variable reading an optional dependency must require it, unless its
	// component does.
	Requires string `json:"requires,omitempty"`
}

// Ref names exactly one of a dependency or a sibling component.
type Ref struct {
	Dependency string `json:"dependency,omitempty"` // a Dependency.Name
	Component  string `json:"component,omitempty"`  // a sibling Component.Name
	// Field is, for dependencies, a Field constant, or on a dependency-only
	// kind "url", "output:<name>" or "secret:<name>"; for components
	// "url:<port name>" or "address:<port name>".
	Field string `json:"field"`
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
	FieldAdminURL = "admin-url" // the superuser URL; postgres-family dependencies only
	FieldQuery    = "query"     // raw query string of the dependency URL
)

// File is a file CatalogHooks.Files renders into a component.
type File struct {
	Path string      `json:"path"`           // absolute, no "..", not under /proc, /sys or /dev
	Mode fs.FileMode `json:"mode,omitempty"` // default 0o444
}

// Dependency is a resource a composition connects to: one Loomplane
// provisions, one already in the manifest, or an external endpoint.
type Dependency struct {
	// BackupEveryHours installs a default on new managed dependencies, by target.
	BackupEveryHours map[string]int `json:"backup_every_hours,omitempty"`
	Name             string         `json:"name"` // "postgres", "redis", "falkordb"
	Title            string         `json:"title"`
	Kinds            []string       `json:"kinds"`             // manifest kinds accepted for a connected resource; Kinds[0] is provisioned for "new"
	Version          string         `json:"version,omitempty"` // version of Kinds[0] to provision; "" is that kind's default
	Optional         bool           `json:"optional,omitempty"`
	Default          bool           `json:"default,omitempty"`  // optional only: on unless uses.<name>.enabled is false
	External         *Requirement   `json:"external,omitempty"` // nil: external endpoints are refused
}

// Requirement is what an external endpoint must offer.
type Requirement struct {
	Engine     string   `json:"engine"` // prober name: "postgres" or "redis"
	MinVersion string   `json:"min_version,omitempty"`
	Extensions []string `json:"extensions,omitempty"`  // postgres: must be in pg_available_extensions
	Preload    []string `json:"preload,omitempty"`     // postgres: must be in shared_preload_libraries
	CreateRole bool     `json:"create_role,omitempty"` // postgres: the connection's role must be able to create roles
	TLS        []string `json:"tls,omitempty"`         // allowed endpoint TLS modes: "disable", "require", "verify-full"; empty allows all
}

// GeneratedSecret is a random value Loomplane generates once and keeps
// in Vault.
type GeneratedSecret struct {
	Name     string `json:"name"`               // "admin-password"
	Bytes    int    `json:"bytes,omitempty"`    // random bytes, default 32
	Encoding string `json:"encoding,omitempty"` // "base64url" (default) or "hex"
	Prefix   string `json:"prefix,omitempty"`   // ^[a-z]{1,8}_$ or "", prepended verbatim
}

// Output is a value a consuming service gets. It sets exactly one of From,
// Template and PerConsumer.
type Output struct {
	Name        string `json:"name"`    // DNS label; Outputs[0] conventionally "url"
	EnvKey      string `json:"env_key"` // "OCTOPUS_URL"
	From        *Ref   `json:"from,omitempty"`
	PerConsumer bool   `json:"per_consumer,omitempty"` // issued per consuming service by the kind's ConsumerBinder
	Secret      bool   `json:"secret,omitempty"`
	Template    string `json:"template,omitempty"` // placeholders as in EnvVar.Template
	Requires    string `json:"requires,omitempty"` // an optional dependency the output is set only with
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
	// Extensions the image ships, postgres family only. They are recorded
	// in the recipe; nothing creates them.
	Extensions []string `json:"extensions,omitempty"`
	FSGroup    int      `json:"fs_group,omitempty"` // group that owns the data volume on Kubernetes
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
	// ConsumerOptions are what a consuming service chooses when it links.
	ConsumerOptions []ConsumerOption `json:"consumer_options,omitempty"`
	// Feature is the server feature the kind belongs to, or "" for none.
	Feature string `json:"feature,omitempty"`
	// Settings are the values a resource of the kind sets under
	// resources.<name>.settings, for a form that edits them.
	Settings []SettingInfo `json:"settings,omitempty"`
}

// SettingInfo is the public part of a Setting.
type SettingInfo struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Pattern  string `json:"pattern"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
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
		d.BackupEveryHours = maps.Clone(d.BackupEveryHours)
		if d.External != nil {
			ext := *d.External
			ext.Extensions = slices.Clone(ext.Extensions)
			ext.Preload = slices.Clone(ext.Preload)
			ext.TLS = slices.Clone(ext.TLS)
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
	k.ConsumerOptions = slices.Clone(k.ConsumerOptions)
	k.Settings = slices.Clone(k.Settings)
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
	if c.WeightCache != nil {
		cache := *c.WeightCache
		c.WeightCache = &cache
	}
	if c.Resources.GPU != nil {
		gpu := *c.Resources.GPU
		c.Resources.GPU = &gpu
	}
	if c.Resources.PerGPU != nil {
		step := *c.Resources.PerGPU
		c.Resources.PerGPU = &step
	}
	c.After = slices.Clone(c.After)
	c.KubernetesRules = slices.Clone(c.KubernetesRules)
	for i := range c.KubernetesRules {
		rule := &c.KubernetesRules[i]
		rule.APIGroups = slices.Clone(rule.APIGroups)
		rule.Resources = slices.Clone(rule.Resources)
		rule.Verbs = slices.Clone(rule.Verbs)
	}
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
		e.Releases[i].Extensions = slices.Clone(e.Releases[i].Extensions)
	}
	return e
}
