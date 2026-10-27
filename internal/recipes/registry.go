package recipes

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	envKeyPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	prefixPattern  = regexp.MustCompile(`^[a-z]{1,8}_$`)
	optionPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// Postgres extension names, such as "vector" or "uuid-ossp".
	extensionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

	// Every image is pinned for both; any other architecture is an error.
	architectures = []string{"amd64", "arm64"}
	targets       = []string{"server", "kubernetes"}
	// builtinOrder is how the five built-in engines are listed.
	builtinOrder = []string{"postgres", "mysql", "mariadb", "redis", "valkey"}

	protocols = []string{"http", "tcp", "redis", "memcached"}
	// dependencyFields are what a Ref reads from a dependency other than a
	// dependency-only kind.
	dependencyFields = []string{FieldURL, FieldHost, FieldPort, FieldHostPort, FieldUsername, FieldPassword, FieldDatabase, FieldAdminURL, FieldQuery}

	// A process's Kubernetes rules only discover Services.
	ruleGroups    = []string{"", "discovery.k8s.io"}
	ruleResources = []string{"services", "endpoints", "endpointslices"}
	ruleVerbs     = []string{"get", "list", "watch"}
)

const (
	defaultJobTimeout  = 10 * time.Minute
	maxJobTimeout      = time.Hour
	defaultCPUMillis   = 500
	defaultMemoryMB    = 256
	defaultSecretBytes = 32
	defaultFileMode    = fs.FileMode(0o444)
)

// Registry holds the catalog's engines and composed kinds. Every registry
// starts with the five built-in engines.
type Registry struct {
	mu      sync.RWMutex
	kinds   map[string]Kind
	engines map[string]Engine
}

// NewRegistry returns a registry with only the built-in engines.
func NewRegistry() *Registry {
	r := &Registry{kinds: map[string]Kind{}, engines: map[string]Engine{}}
	for _, e := range builtinEngines() {
		r.engines[e.Kind] = e
	}
	return r
}

var defaultRegistry = NewRegistry()

// Default is the package registry: the built-in engines plus every kind
// registered from init in this package.
func Default() *Registry { return defaultRegistry }

// MustRegister adds a kind to Default, panicking with every problem the
// kind has. It is meant for init.
func MustRegister(k Kind) {
	if err := Default().Register(k); err != nil {
		panic(fmt.Sprintf("recipes: register kind %q: %v", k.Name, err))
	}
}

// MustRegisterEngine adds an engine to Default, panicking with every
// problem the engine has. It is meant for init.
func MustRegisterEngine(e Engine) {
	if err := Default().RegisterEngine(e); err != nil {
		panic(fmt.Sprintf("recipes: register engine %q: %v", e.Kind, err))
	}
}

// Register validates a composed kind and adds a copy of it, with defaults
// filled in. The error joins one finding per broken rule.
func (r *Registry) Register(k Kind) error {
	k = k.clone()
	normalizeKind(&k)
	errs := validateKind(k)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.taken(k.Name) {
		errs = append(errs, fmt.Errorf("%s: a kind or engine named %q is already registered", k.Name, k.Name))
	} else {
		errs = append(errs, crossFindings(k, r.kinds, r.engines)...)
		kinds := maps.Clone(r.kinds)
		kinds[k.Name] = k
		errs = append(errs, r.dependentFindings(k.Name, kinds, r.engines)...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	r.kinds[k.Name] = k
	return nil
}

// RegisterEngine validates an engine and adds a copy of it.
func (r *Registry) RegisterEngine(e Engine) error {
	e = e.clone()
	e.builtin = false
	if e.Placement == "" {
		e.Placement = PlacementServiceProject
	}
	errs := validateEngine(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.taken(e.Kind) {
		errs = append(errs, fmt.Errorf("%s: a kind or engine named %q is already registered", e.Kind, e.Kind))
	} else {
		engines := maps.Clone(r.engines)
		engines[e.Kind] = e
		errs = append(errs, r.dependentFindings(e.Kind, r.kinds, engines)...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	r.engines[e.Kind] = e
	return nil
}

func (r *Registry) taken(name string) bool {
	_, kind := r.kinds[name]
	_, engine := r.engines[name]
	return kind || engine
}

// dependentFindings checks again every registered kind with a dependency
// that accepts name, now that name is in kinds or engines. Kinds register
// from init in file order, so a kind can name one that isn't registered
// yet; whichever comes second is the one refused.
func (r *Registry) dependentFindings(name string, kinds map[string]Kind, engines map[string]Engine) []error {
	var errs []error
	for _, other := range slices.Sorted(maps.Keys(r.kinds)) {
		k := r.kinds[other]
		if slices.ContainsFunc(k.Dependencies, func(d Dependency) bool { return slices.Contains(d.Kinds, name) }) {
			errs = append(errs, crossFindings(k, kinds, engines)...)
		}
	}
	return errs
}

// Kind returns a copy of a composed kind.
func (r *Registry) Kind(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	if !ok {
		return Kind{}, false
	}
	return k.clone(), true
}

// Engine returns a copy of an engine, built-in or registered.
func (r *Registry) Engine(name string) (Engine, bool) {
	e, ok := r.engine(name)
	if !ok {
		return Engine{}, false
	}
	return e.clone(), true
}

func (r *Registry) engine(name string) (Engine, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.engines[name]
	return e, ok
}

// Kinds describes every engine and kind: the built-in engines first, then
// the registered ones by name.
func (r *Registry) Kinds() []KindInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]KindInfo, 0, len(r.engines)+len(r.kinds))
	for _, name := range builtinOrder {
		infos = append(infos, r.engines[name].info())
	}
	var rest []KindInfo
	for _, e := range r.engines {
		if !e.builtin {
			rest = append(rest, e.info())
		}
	}
	for _, k := range r.kinds {
		rest = append(rest, k.info())
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append(infos, rest...)
}

// EnvKey is the variable a consuming service gets for a kind: the engine's
// key, or a composed kind's primary output. It is "" for unknown kinds.
func (r *Registry) EnvKey(kind string) string {
	if e, ok := r.engine(kind); ok {
		return e.EnvKey
	}
	if k, ok := r.Kind(kind); ok && len(k.Outputs) > 0 {
		return k.Outputs[0].EnvKey
	}
	return ""
}

// FamilyOf is the native adapter family an engine kind reuses. Built-in
// engines are their own family; anything else is "".
func (r *Registry) FamilyOf(kind string) string {
	e, _ := r.engine(kind)
	return e.Family
}

// Composed reports whether kind is a composed kind.
func (r *Registry) Composed(kind string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.kinds[kind]
	return ok
}

// defaultVersion is the newest release of a registered engine or kind.
func (r *Registry) defaultVersion(kind string) string {
	if e, ok := r.engine(kind); ok && len(e.Releases) > 0 {
		return e.Releases[0].Version
	}
	if k, ok := r.Kind(kind); ok && len(k.Releases) > 0 {
		return k.Releases[0].Version
	}
	return ""
}

// EnvKey reads Default; see Registry.EnvKey.
func EnvKey(kind string) string { return Default().EnvKey(kind) }

// FamilyOf reads Default; see Registry.FamilyOf.
func FamilyOf(kind string) string { return Default().FamilyOf(kind) }

// Composed reports whether Default has a composed kind by that name.
func Composed(kind string) bool { return Default().Composed(kind) }

func (e Engine) info() KindInfo {
	info := KindInfo{Name: e.Kind, Title: e.Title, Placement: e.Placement, Targets: slices.Clone(e.Targets), Engine: true}
	for _, release := range e.Releases {
		info.Versions = append(info.Versions, release.Version)
	}
	return info
}

func (k Kind) info() KindInfo {
	info := KindInfo{Name: k.Name, Title: k.Title, Description: k.Description, Placement: k.Placement, Targets: slices.Clone(k.Targets)}
	for _, release := range k.Releases {
		info.Versions = append(info.Versions, release.Version)
	}
	for _, d := range k.Dependencies {
		info.Dependencies = append(info.Dependencies, DependencyInfo{Name: d.Name, Title: d.Title, Kinds: slices.Clone(d.Kinds), Optional: d.Optional, Default: d.Default, External: d.External != nil})
	}
	for _, c := range k.Components {
		info.Components = append(info.Components, ComponentInfo{Name: c.Name, Role: c.Role, Stateful: c.Volume != nil, Protection: c.Protection})
	}
	for _, o := range k.Outputs {
		info.Outputs = append(info.Outputs, OutputInfo{Name: o.Name, EnvKey: o.EnvKey, PerConsumer: o.PerConsumer})
	}
	info.ConsumerOptions = slices.Clone(k.ConsumerOptions)
	return info
}

// normalizeKind fills in the documented defaults before validation.
func normalizeKind(k *Kind) {
	for i := range k.Secrets {
		s := &k.Secrets[i]
		if s.Bytes == 0 {
			s.Bytes = defaultSecretBytes
		}
		if s.Encoding == "" {
			s.Encoding = "base64url"
		}
	}
	for i := range k.Components {
		c := &k.Components[i]
		if c.Role == RoleJob && c.Timeout == 0 {
			c.Timeout = defaultJobTimeout
		}
		if c.Resources.CPUMillis == 0 {
			c.Resources.CPUMillis = defaultCPUMillis
		}
		if c.Resources.MemoryMB == 0 {
			c.Resources.MemoryMB = defaultMemoryMB
		}
		if c.Protection == "" {
			c.Protection = ProtectionNone
			if c.Volume != nil {
				c.Protection = ProtectionRebuildable
			}
		}
		for j := range c.Files {
			if c.Files[j].Mode == 0 {
				c.Files[j].Mode = defaultFileMode
			}
		}
	}
}

// findings collects one error per broken rule, each naming where it is.
type findings struct {
	where string
	errs  []error
}

func (f *findings) add(format string, args ...any) {
	f.errs = append(f.errs, fmt.Errorf("%s: %s", f.where, fmt.Sprintf(format, args...)))
}

func validateKind(k Kind) []error {
	f := &findings{where: k.Name}
	if k.Name == "" {
		f.where = "kind"
	}
	if !namePattern.MatchString(k.Name) {
		f.add("kind name %q must be a lowercase DNS label of at most 40 characters", k.Name)
	}
	if k.Title == "" {
		f.add("kind has no title")
	}
	if k.Placement != PlacementServiceProject && k.Placement != PlacementAppProject && k.Placement != PlacementDependency {
		f.add("placement %q is not %s, %s or %s", k.Placement, PlacementServiceProject, PlacementAppProject, PlacementDependency)
	}
	validateTargets(f, k.Targets)
	if k.Placement == PlacementDependency {
		validateDependencyOnly(f, k)
	}

	dependencies := map[string]Dependency{}
	for _, d := range k.Dependencies {
		validateName(f, "dependency", d.Name)
		if _, dup := dependencies[d.Name]; dup {
			f.add("dependency %q is declared more than once", d.Name)
		}
		dependencies[d.Name] = d
		if d.Title == "" {
			f.add("dependency %q has no title", d.Name)
		}
		if len(d.Kinds) == 0 {
			f.add("dependency %q accepts no kinds", d.Name)
		}
		for _, kind := range d.Kinds {
			if !namePattern.MatchString(kind) {
				f.add("dependency %q accepts kind %q, which is not a valid kind name", d.Name, kind)
			}
		}
		if d.Default && !d.Optional {
			f.add("dependency %q is on by default but not optional", d.Name)
		}
		for target, hours := range d.BackupEveryHours {
			if !slices.Contains(k.Targets, target) || hours < 1 || hours > 168 {
				f.add("dependency %q backup default for %q must use a supported target and an interval of 1 to 168 hours", d.Name, target)
			}
		}
		if d.External != nil {
			validateRequirement(f, d.Name, *d.External)
		}
	}

	secrets := map[string]bool{}
	for _, s := range k.Secrets {
		validateName(f, "secret", s.Name)
		if secrets[s.Name] {
			f.add("secret %q is declared more than once", s.Name)
		}
		secrets[s.Name] = true
		if s.Bytes < 16 || s.Bytes > 1024 {
			f.add("secret %q has %d random bytes; use 16 to 1024", s.Name, s.Bytes)
		}
		if s.Encoding != "base64url" && s.Encoding != "hex" {
			f.add("secret %q encoding %q is not base64url or hex", s.Name, s.Encoding)
		}
		if s.Prefix != "" && !prefixPattern.MatchString(s.Prefix) {
			f.add("secret %q prefix %q must be 1 to 8 lowercase letters and an underscore", s.Name, s.Prefix)
		}
	}

	components := map[string]*Component{}
	if len(k.Components) == 0 {
		f.add("kind has no components")
	}
	for i := range k.Components {
		c := &k.Components[i]
		if _, dup := components[c.Name]; dup {
			f.add("component %q is declared more than once", c.Name)
			continue
		}
		components[c.Name] = c
	}
	for i := range k.Components {
		validateComponent(f, k, &k.Components[i], components, dependencies, secrets)
		if slices.Contains(k.Targets, "kubernetes") && slices.ContainsFunc(k.Components[i].Ports, func(p Port) bool { return p.Protocol == "memcached" }) {
			f.add("component %q uses Memcached readiness, which is qualified only on server targets", k.Components[i].Name)
		}
		// A volume's PVC request is its Kubernetes size; zero would plan
		// and then fail to apply.
		if v := k.Components[i].Volume; v != nil && v.KubernetesSizeMB == 0 && slices.Contains(k.Targets, "kubernetes") {
			f.add("component %q has a volume with no Kubernetes size, and the kind runs on Kubernetes; set KubernetesSizeMB", k.Components[i].Name)
		}
	}
	if cycle := afterCycle(k.Components, components); cycle != nil {
		f.add("components %s form a cycle through after", strings.Join(cycle, " -> "))
	}

	outputs := map[string]bool{}
	envKeys := map[string]bool{}
	for _, o := range k.Outputs {
		validateName(f, "output", o.Name)
		if outputs[o.Name] {
			f.add("output %q is declared more than once", o.Name)
		}
		outputs[o.Name] = true
		if !envKeyPattern.MatchString(o.EnvKey) {
			f.add("output %q env key %q must be upper case letters, digits and underscores", o.Name, o.EnvKey)
		} else if envKeys[o.EnvKey] {
			f.add("env key %q belongs to more than one output", o.EnvKey)
		}
		envKeys[o.EnvKey] = true
		what := fmt.Sprintf("output %q", o.Name)
		if count(o.From != nil, o.Template != "", o.PerConsumer) != 1 {
			f.add("%s must set exactly one of from, template and per consumer", what)
		}
		refs := validateReads(f, what, o.From, o.Template, components, dependencies, secrets)
		validateRequires(f, what, o.Requires, "", refs, components, dependencies)
	}

	options := map[string]bool{}
	for _, o := range k.ConsumerOptions {
		if !optionPattern.MatchString(o.Name) {
			f.add("consumer option name %q must be a lowercase letter then up to 31 lowercase letters, digits and underscores", o.Name)
		}
		if options[o.Name] {
			f.add("consumer option %q is declared more than once", o.Name)
		}
		options[o.Name] = true
		if o.Title == "" {
			f.add("consumer option %q has no title", o.Name)
		}
		if _, err := regexp.Compile(o.Pattern); err != nil {
			f.add("consumer option %q pattern %q doesn't compile: %v", o.Name, o.Pattern, err)
		}
	}

	if len(k.Releases) == 0 {
		f.add("kind has no releases")
	}
	versions := map[string]bool{}
	for _, release := range k.Releases {
		validateRelease(f, release.Version, release.Product, release.Revision, versions)
		for _, c := range k.Components {
			image, ok := release.Images[c.Name]
			if !ok {
				f.add("release %s has no image for component %q", release.Version, c.Name)
				continue
			}
			validateImage(f, fmt.Sprintf("release %s component %q", release.Version, c.Name), image, c.Resources.GPU != nil)
		}
		for name := range release.Images {
			if _, ok := components[name]; !ok {
				f.add("release %s has an image for %q, which the kind doesn't declare", release.Version, name)
			}
		}
	}
	return f.errs
}

func validateComponent(f *findings, k Kind, c *Component, components map[string]*Component, dependencies map[string]Dependency, secrets map[string]bool) {
	validateName(f, "component", c.Name)
	if strings.Contains(c.Name, "--") {
		f.add("component name %q has \"--\", which action names use as a separator", c.Name)
	}
	what := fmt.Sprintf("component %q", c.Name)
	switch c.Role {
	case RoleProcess:
		what = fmt.Sprintf("process %q", c.Name)
		if c.Timeout != 0 {
			f.add("%s has a timeout; only jobs do", what)
		}
		if c.Readiness == nil {
			f.add("%s has no readiness probe", what)
		}
	case RoleJob:
		what = fmt.Sprintf("job %q", c.Name)
		if len(c.Ports) > 0 {
			f.add("%s has ports; jobs serve nothing", what)
		}
		if c.Readiness != nil {
			f.add("%s has a readiness probe; jobs run to completion", what)
		}
		if c.Volume != nil {
			f.add("%s has a volume; jobs are stateless", what)
		}
		if c.Timeout < 0 || c.Timeout > maxJobTimeout {
			f.add("%s timeout %s must be positive and at most an hour", what, c.Timeout)
		}
	default:
		f.add("%s role %q is not process or job", what, c.Role)
	}

	ports := map[string]Port{}
	containerPorts := map[int]bool{}
	for _, p := range c.Ports {
		validateName(f, what+" port", p.Name)
		if _, dup := ports[p.Name]; dup {
			f.add("%s declares port %q more than once", what, p.Name)
		}
		ports[p.Name] = p
		if p.Container < 1 || p.Container > 65535 {
			f.add("%s port %q container port %d is out of range", what, p.Name, p.Container)
		} else if containerPorts[p.Container] {
			f.add("%s listens on %d more than once", what, p.Container)
		}
		containerPorts[p.Container] = true
		if !slices.Contains(protocols, p.Protocol) {
			f.add("%s port %q protocol %q is not http, tcp, redis or memcached", what, p.Name, p.Protocol)
		}
		if p.Routable && p.Control {
			f.add("%s port %q is both routable and control; a control port is never routed", what, p.Name)
		}
		if p.Routable && p.Protocol != "http" {
			f.add("%s port %q is routable but its protocol is %q; only http ports are routed", what, p.Name, p.Protocol)
		}
	}
	if probe := c.Readiness; probe != nil && c.Role == RoleProcess {
		port, ok := ports[probe.Port]
		switch {
		case !ok:
			f.add("%s readiness names port %q, which it doesn't declare", what, probe.Port)
		case port.Protocol == "redis" || port.Protocol == "memcached":
			if probe.Path != "" {
				f.add("%s readiness port %q speaks %s, so its path %q must be empty", what, probe.Port, port.Protocol, probe.Path)
			}
		case port.Protocol != "http":
			f.add("%s readiness port %q is not an http, redis or memcached port", what, probe.Port)
		case !strings.HasPrefix(probe.Path, "/"):
			f.add("%s readiness path %q must start with /", what, probe.Path)
		}
	}
	if len(c.KubernetesRules) > 0 && c.Role != RoleProcess {
		f.add("%s has Kubernetes rules; only processes get them", what)
	}
	for i, rule := range c.KubernetesRules {
		validateRule(f, fmt.Sprintf("%s Kubernetes rule %d", what, i+1), rule)
	}
	if c.Requires != "" {
		validateRequires(f, what, c.Requires, "", nil, components, dependencies)
	}

	switch {
	case c.Protection == ProtectionNative:
		f.add("%s has native protection, which is reserved for engines", what)
	case c.Protection == ProtectionExternal:
		f.add("%s has external protection, which is for dependencies", what)
	case c.Volume != nil && c.Protection != ProtectionRebuildable:
		f.add("%s has a volume, so its protection must be %s", what, ProtectionRebuildable)
	case c.Volume == nil && c.Protection != ProtectionNone:
		f.add("%s has no volume, so its protection must be %s", what, ProtectionNone)
	}
	if v := c.Volume; v != nil {
		if !path.IsAbs(v.MountPath) || path.Clean(v.MountPath) != v.MountPath || v.MountPath == "/" {
			f.add("%s volume mount path %q must be a clean absolute path below /", what, v.MountPath)
		}
		if v.KubernetesSizeMB < 0 {
			f.add("%s volume size %d MB is negative", what, v.KubernetesSizeMB)
		}
		if v.FSGroup < 0 {
			f.add("%s volume fs group %d is negative", what, v.FSGroup)
		}
	}
	if c.Resources.CPUMillis < 0 || c.Resources.MemoryMB < 0 {
		f.add("%s resources must not be negative", what)
	}
	validateGPU(f, what, c, k)

	env := map[string]bool{}
	for _, e := range c.Env {
		if !envNamePattern.MatchString(e.Name) {
			f.add("%s env name %q is not a valid variable name", what, e.Name)
		}
		if env[e.Name] {
			f.add("%s sets env %s more than once", what, e.Name)
		}
		env[e.Name] = true
		variable := fmt.Sprintf("%s env %s", what, e.Name)
		if count(e.Value != "", e.Secret != "", e.From != nil, e.Template != "") != 1 {
			f.add("%s must set exactly one of value, secret, from and template", variable)
		}
		if e.Secret != "" && !secrets[e.Secret] {
			f.add("%s names secret %q, which the kind doesn't declare", variable, e.Secret)
		}
		refs := validateReads(f, variable, e.From, e.Template, components, dependencies, secrets)
		validateRequires(f, variable, e.Requires, c.Requires, refs, components, dependencies)
	}

	files := map[string]bool{}
	for _, file := range c.Files {
		p := file.Path
		switch {
		case !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, ".."):
			f.add("%s file %q must be a clean absolute path", what, p)
		case p == "/":
			f.add("%s file %q names no file", what, p)
		case underSystem(p):
			f.add("%s file %q is under /proc, /sys or /dev", what, p)
		}
		if files[p] {
			f.add("%s declares file %q more than once", what, p)
		}
		files[p] = true
		if file.Mode&^fs.ModePerm != 0 {
			f.add("%s file %q mode %s has more than permission bits", what, p, file.Mode)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		for q := range files {
			if strings.HasPrefix(p, q+"/") {
				f.add("%s file %q sits under file %q", what, p, q)
			}
		}
	}

	after := map[string]bool{}
	for _, name := range c.After {
		switch _, ok := components[name]; {
		case name == c.Name:
			f.add("%s starts after itself", what)
		case !ok:
			f.add("%s after names %q, which the kind doesn't declare", what, name)
		case after[name]:
			f.add("%s names %q in after more than once", what, name)
		}
		after[name] = true
	}
}

func validateGPU(f *findings, what string, c *Component, k Kind) {
	r := c.Resources
	if g := r.GPU; g != nil {
		if c.Role != RoleProcess {
			f.add("%s requests a GPU but isn't a process", what)
		}
		if g.Count < 1 || g.Count > 8 {
			f.add("%s GPU count %d is outside 1 to 8", what, g.Count)
		}
		memory := 0
		if g.Kind != "" {
			kind, ok := provider.LookupAcceleratorKind(g.Kind)
			if !ok || kind.Vendor != provider.VendorNVIDIA {
				f.add("%s GPU kind %q isn't a known NVIDIA kind", what, g.Kind)
			} else {
				memory = kind.MemoryGiB
			}
		}
		switch {
		case g.MinMemoryGiB < 0:
			f.add("%s GPU memory floor %d GiB is negative", what, g.MinMemoryGiB)
		case memory > 0 && g.MinMemoryGiB > memory:
			f.add("%s needs %d GiB per GPU but %s has %d", what, g.MinMemoryGiB, g.Kind, memory)
		}
	}
	if r.ShmMB < 0 || r.ShmMB > 65536 || (r.ShmMB > 0 && r.ShmMB >= r.MemoryMB) {
		f.add("%s shared memory must be smaller than its memory, and at most 65536 MB", what)
	}
	w := c.WeightCache
	if w == nil {
		return
	}
	if r.GPU == nil {
		f.add("%s weight cache needs a GPU component", what)
	}
	p := w.MountPath
	switch {
	case !path.IsAbs(p) || path.Clean(p) != p || p == "/":
		f.add("%s weight cache mount path %q must be a clean absolute path below /", what, p)
	case underSystem(p):
		f.add("%s weight cache mount path %q is under /proc, /sys or /dev", what, p)
	}
	if v := c.Volume; v != nil && (p == v.MountPath || strings.HasPrefix(p, v.MountPath+"/") || strings.HasPrefix(v.MountPath, p+"/")) {
		f.add("%s weight cache mount path %q overlaps volume mount path %q", what, p, v.MountPath)
	}
	if slices.Contains(k.Targets, "kubernetes") && w.KubernetesSizeMB < 1024 {
		f.add("%s weight cache needs at least 1024 MB on Kubernetes", what)
	}
}

func count(set ...bool) int {
	n := 0
	for _, ok := range set {
		if ok {
			n++
		}
	}
	return n
}

// validateDependencyOnly checks what a dependency-only kind can't have.
func validateDependencyOnly(f *findings, k Kind) {
	// The planner runs that one component as the dependency, so a job
	// beside it would never run, and a job alone would run in its place.
	processes := len(slices.DeleteFunc(slices.Clone(k.Components), func(c Component) bool { return c.Role != RoleProcess }))
	if processes != 1 || len(k.Components) != 1 {
		f.add("a dependency-only kind runs exactly one process and no other components, not %d processes and %d other components", processes, len(k.Components)-processes)
	}
	if len(k.Dependencies) > 0 {
		f.add("a dependency-only kind has no dependencies of its own")
	}
	for _, o := range k.Outputs {
		if o.PerConsumer {
			f.add("output %q is per consumer; a dependency-only kind has no consumers of its own", o.Name)
		}
	}
	if len(k.ConsumerOptions) > 0 {
		f.add("a dependency-only kind has no consumers of its own, so it takes no consumer options")
	}
}

func validateRule(f *findings, what string, rule KubernetesRule) {
	if len(rule.APIGroups) == 0 || len(rule.Resources) == 0 || len(rule.Verbs) == 0 {
		f.add("%s needs api groups, resources and verbs", what)
	}
	for _, group := range rule.APIGroups {
		if !slices.Contains(ruleGroups, group) {
			f.add("%s api group %q is not the core group or discovery.k8s.io", what, group)
		}
	}
	for _, resource := range rule.Resources {
		if !slices.Contains(ruleResources, resource) {
			f.add("%s resource %q is not services, endpoints or endpointslices", what, resource)
		}
	}
	for _, verb := range rule.Verbs {
		if !slices.Contains(ruleVerbs, verb) {
			f.add("%s verb %q is not get, list or watch; rules only read", what, verb)
		}
	}
}

// templateReads parses a template's ${...} placeholders into the refs and
// secrets they read. problems describes each placeholder that isn't one of
// the three forms.
func templateReads(template string) (refs []*Ref, secrets, problems []string) {
	parts, problems := ParseTemplate(template)
	for _, part := range parts {
		switch {
		case part.Ref != nil:
			refs = append(refs, part.Ref)
		case part.Placeholder:
			secrets = append(secrets, part.Secret)
		}
	}
	return refs, secrets, problems
}

// validateReads checks what an env var or output reads, through from and
// its template, and returns those refs.
func validateReads(f *findings, what string, from *Ref, template string, components map[string]*Component, dependencies map[string]Dependency, secrets map[string]bool) []*Ref {
	refs, names, problems := templateReads(template)
	for _, problem := range problems {
		f.add("%s %s", what, problem)
	}
	for _, name := range names {
		if !secrets[name] {
			f.add("%s reads secret %q, which the kind doesn't declare", what, name)
		}
	}
	if from != nil {
		refs = append([]*Ref{from}, refs...)
	}
	for _, ref := range refs {
		validateRef(f, what, ref, components, dependencies)
	}
	return refs
}

// validateRequires checks that requires names an optional dependency, and
// that whatever refs read through an optional dependency, or through a
// component that requires one, is required here or by inherited (the
// enclosing component's Requires).
func validateRequires(f *findings, what, requires, inherited string, refs []*Ref, components map[string]*Component, dependencies map[string]Dependency) {
	if requires != "" {
		switch d, ok := dependencies[requires]; {
		case !ok:
			f.add("%s requires %q, which the kind doesn't declare", what, requires)
		case !d.Optional:
			f.add("%s requires dependency %q, which isn't optional", what, requires)
		}
	}
	reported := map[string]bool{}
	for _, ref := range refs {
		var needs string
		if d, ok := dependencies[ref.Dependency]; ok && ref.Dependency != "" && d.Optional {
			needs = ref.Dependency
		} else if c, ok := components[ref.Component]; ok && ref.Component != "" {
			needs = c.Requires
		}
		if needs == "" || needs == requires || needs == inherited || reported[needs] {
			continue
		}
		reported[needs] = true
		if ref.Dependency != "" {
			f.add("%s reads optional dependency %q, so it must require it", what, needs)
		} else {
			f.add("%s reads component %q, which requires %q, so it must require it too", what, ref.Component, needs)
		}
	}
}

func underSystem(p string) bool {
	for _, dir := range []string{"/proc", "/sys", "/dev"} {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func validateRef(f *findings, what string, ref *Ref, components map[string]*Component, dependencies map[string]Dependency) {
	switch {
	case (ref.Dependency == "") == (ref.Component == ""):
		f.add("%s must name exactly one of a dependency and a component", what)
	case ref.Dependency != "":
		if _, ok := dependencies[ref.Dependency]; !ok {
			f.add("%s names dependency %q, which the kind doesn't declare", what, ref.Dependency)
		}
		if !slices.Contains(dependencyFields, ref.Field) {
			// output:<name> and secret:<name> read a dependency-only kind;
			// crossFindings checks the names once that kind is registered.
			_, name, ok := cutAny(ref.Field, "output:", "secret:")
			if !ok || !namePattern.MatchString(name) {
				f.add("%s reads field %q, which a dependency doesn't have", what, ref.Field)
			}
		}
	default:
		c, ok := components[ref.Component]
		if !ok {
			f.add("%s names component %q, which the kind doesn't declare", what, ref.Component)
			return
		}
		field, port, _ := strings.Cut(ref.Field, ":")
		if field != "url" && field != "address" {
			f.add("%s reads field %q; components have url:<port> and address:<port>", what, ref.Field)
			return
		}
		if !slices.ContainsFunc(c.Ports, func(p Port) bool { return p.Name == port }) {
			f.add("%s reads port %q, which component %q doesn't declare", what, port, ref.Component)
		}
	}
}

// cutAny cuts the first of prefixes s starts with.
func cutAny(s string, prefixes ...string) (prefix, rest string, ok bool) {
	for _, prefix := range prefixes {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			return prefix, rest, true
		}
	}
	return "", s, false
}

// crossFindings checks what k's dependencies read against the kinds and
// engines they accept, as far as those are registered. A kind registered
// later is checked against k when it registers (see dependentFindings).
func crossFindings(k Kind, kinds map[string]Kind, engines map[string]Engine) []error {
	f := &findings{where: k.Name}
	reads := dependencyReads(k)
	for _, d := range k.Dependencies {
		var only *Kind
		for _, name := range d.Kinds {
			if dk, ok := kinds[name]; ok && dk.Placement == PlacementDependency {
				only = &dk
				break
			}
		}
		if only != nil {
			if slices.ContainsFunc(d.Kinds, func(name string) bool { return name != only.Name }) {
				f.add("dependency %q accepts %s, which is dependency-only, with other kinds; a dependency-only kind is accepted alone", d.Name, only.Name)
			}
			if d.External != nil {
				f.add("dependency %q accepts %s, which is dependency-only, so it takes no external endpoints", d.Name, only.Name)
			}
		}
		for _, use := range reads[d.Name] {
			if only != nil {
				validateDependencyOnlyField(f, use.what, use.field, d.Name, *only)
				continue
			}
			if _, _, ok := cutAny(use.field, "output:", "secret:"); ok && known(d.Kinds, kinds, engines) {
				f.add("%s reads %q from dependency %q, which only a dependency-only kind has", use.what, use.field, d.Name)
			}
			if use.field == FieldAdminURL && !postgresFamily(d, kinds, engines) {
				f.add("%s reads admin-url from dependency %q, which only postgres-family dependencies have", use.what, d.Name)
			}
		}
	}
	return f.errs
}

type fieldUse struct{ what, field string }

// dependencyReads is every field k's env vars and outputs read from each
// dependency, through from or a template.
func dependencyReads(k Kind) map[string][]fieldUse {
	out := map[string][]fieldUse{}
	add := func(what string, from *Ref, template string) {
		refs, _, _ := templateReads(template)
		if from != nil {
			refs = append(refs, from)
		}
		for _, ref := range refs {
			if ref.Dependency != "" {
				out[ref.Dependency] = append(out[ref.Dependency], fieldUse{what, ref.Field})
			}
		}
	}
	for _, c := range k.Components {
		for _, e := range c.Env {
			add(fmt.Sprintf("component %q env %s", c.Name, e.Name), e.From, e.Template)
		}
	}
	for _, o := range k.Outputs {
		add(fmt.Sprintf("output %q", o.Name), o.From, o.Template)
	}
	return out
}

// validateDependencyOnlyField checks a field read from a dependency-only
// kind: url (its primary output), output:<name> or secret:<name>.
func validateDependencyOnlyField(f *findings, what, field, dependency string, dk Kind) {
	prefix, name, _ := cutAny(field, "output:", "secret:")
	switch {
	case field == FieldURL:
		if len(dk.Outputs) == 0 {
			f.add("%s reads url from dependency %q, but %s has no outputs", what, dependency, dk.Name)
		}
	case prefix == "output:":
		if !slices.ContainsFunc(dk.Outputs, func(o Output) bool { return o.Name == name }) {
			f.add("%s reads %q from dependency %q, which %s doesn't have", what, field, dependency, dk.Name)
		}
	case prefix == "secret:":
		if !slices.ContainsFunc(dk.Secrets, func(s GeneratedSecret) bool { return s.Name == name }) {
			f.add("%s reads %q from dependency %q, which %s doesn't have", what, field, dependency, dk.Name)
		}
	default:
		f.add("%s reads %q from dependency %q; %s is dependency-only, so it has url, output:<name> and secret:<name>", what, field, dependency, dk.Name)
	}
}

// known reports whether every kind is registered.
func known(list []string, kinds map[string]Kind, engines map[string]Engine) bool {
	for _, name := range list {
		_, kind := kinds[name]
		_, engine := engines[name]
		if !kind && !engine {
			return false
		}
	}
	return true
}

// postgresFamily reports whether d can only be a postgres-family database,
// as far as its kinds are registered.
func postgresFamily(d Dependency, kinds map[string]Kind, engines map[string]Engine) bool {
	if d.External != nil && d.External.Engine != "postgres" {
		return false
	}
	for _, name := range d.Kinds {
		if e, ok := engines[name]; ok && e.Family != "postgres" {
			return false
		}
		if _, ok := kinds[name]; ok {
			return false
		}
	}
	return true
}

// afterCycle returns the components of one cycle through After, or nil.
// Names After doesn't resolve are reported elsewhere and skipped here.
func afterCycle(list []Component, components map[string]*Component) []string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(name string) []string
	visit = func(name string) []string {
		switch state[name] {
		case visiting:
			i := slices.Index(stack, name)
			return append(slices.Clone(stack[i:]), name)
		case done:
			return nil
		}
		state[name] = visiting
		stack = append(stack, name)
		for _, next := range components[name].After {
			if _, ok := components[next]; !ok || next == name {
				continue
			}
			if cycle := visit(next); cycle != nil {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, c := range list {
		if cycle := visit(c.Name); cycle != nil {
			return cycle
		}
	}
	return nil
}

// validateRequirement checks what a dependency asks of an external
// endpoint: an engine Loomplane can check, and rules that engine has.
func validateRequirement(f *findings, dependency string, req Requirement) {
	if req.Engine != "postgres" && req.Engine != "redis" {
		f.add("dependency %q accepts external %q endpoints; only postgres and redis can be checked", dependency, req.Engine)
	}
	if req.Engine != "postgres" && (len(req.Preload) > 0 || req.CreateRole) {
		f.add("dependency %q checks preload libraries or role rights, which only postgres endpoints have", dependency)
	}
	for _, mode := range req.TLS {
		if mode != "disable" && mode != "require" && mode != "verify-full" {
			f.add("dependency %q allows TLS mode %q; the modes are disable, require and verify-full", dependency, mode)
		}
	}
}

func validateName(f *findings, what, name string) {
	if !namePattern.MatchString(name) {
		f.add("%s name %q must be a lowercase DNS label of at most 40 characters", what, name)
	}
}

func validateTargets(f *findings, list []string) {
	if len(list) == 0 {
		f.add("lists no targets")
	}
	seen := map[string]bool{}
	for _, target := range list {
		if !slices.Contains(targets, target) {
			f.add("target %q is not server or kubernetes", target)
		}
		if seen[target] {
			f.add("lists target %q more than once", target)
		}
		seen[target] = true
	}
}

func validateRelease(f *findings, version, product, revision string, seen map[string]bool) {
	if version == "" {
		f.add("a release has no version")
	} else if seen[version] {
		f.add("release %s is declared more than once", version)
	}
	seen[version] = true
	if product == "" {
		f.add("release %s has no product version", version)
	}
	if revision == "" {
		f.add("release %s has no revision", version)
	}
}

// validateImage pins both architectures, except that a GPU component's image
// may leave out arm64, since NVIDIA GPUs run on amd64 targets.
func validateImage(f *findings, what string, image Image, gpu bool) {
	if image.Repository == "" {
		f.add("%s has no image repository", what)
	} else if strings.ContainsAny(image.Repository, "@ \t\n") {
		f.add("%s repository %q must not carry a digest or spaces", what, image.Repository)
	}
	for _, architecture := range architectures {
		digest, ok := image.Digests[architecture]
		switch {
		case !ok && gpu && architecture == "arm64":
		case !ok:
			f.add("%s has no %s digest", what, architecture)
		case !digestPattern.MatchString(digest):
			f.add("%s %s digest %q is not \"sha256:\" and 64 hex digits", what, architecture, digest)
		}
	}
	for architecture := range image.Digests {
		if !slices.Contains(architectures, architecture) {
			f.add("%s has a digest for unknown architecture %q", what, architecture)
		}
	}
}

func validateEngine(e Engine) []error {
	f := &findings{where: e.Kind}
	if e.Kind == "" {
		f.where = "engine"
	}
	if !namePattern.MatchString(e.Kind) {
		f.add("engine name %q must be a lowercase DNS label of at most 40 characters", e.Kind)
	}
	format, ok := families[e.Family]
	if !ok {
		f.add("family %q has no native recovery adapter", e.Family)
	}
	if e.Title == "" {
		f.add("engine has no title")
	}
	if !envKeyPattern.MatchString(e.EnvKey) {
		f.add("env key %q must be upper case letters, digits and underscores", e.EnvKey)
	}
	if e.Placement != PlacementServiceProject && e.Placement != PlacementAppProject {
		f.add("placement %q is not %s or %s", e.Placement, PlacementServiceProject, PlacementAppProject)
	}
	validateTargets(f, e.Targets)
	kubernetes := slices.Contains(e.Targets, "kubernetes")
	if kubernetes && e.Family != "postgres" {
		f.add("targets kubernetes, which only the postgres family supports")
	}
	if len(e.Releases) == 0 {
		f.add("engine has no releases")
	}
	versions := map[string]bool{}
	for _, release := range e.Releases {
		validateRelease(f, release.Version, release.Product, release.Revision, versions)
		validateImage(f, "release "+release.Version, release.Image, false)
		if !path.IsAbs(release.MountPath) || path.Clean(release.MountPath) != release.MountPath || release.MountPath == "/" {
			f.add("release %s mount path %q must be a clean absolute path below /", release.Version, release.MountPath)
		}
		if ok && release.BackupFormat != format {
			f.add("release %s backup format %q is not %q, the %s family's", release.Version, release.BackupFormat, format, e.Family)
		}
		if release.KubernetesVolumeMB < 0 || kubernetes && release.KubernetesVolumeMB == 0 {
			f.add("release %s needs a positive Kubernetes volume size", release.Version)
		}
		if release.FSGroup < 0 {
			f.add("release %s fs group %d is negative", release.Version, release.FSGroup)
		}
		if len(release.Extensions) > 0 && e.Family != "postgres" {
			f.add("release %s lists extensions, which only the postgres family has", release.Version)
		}
		extensions := map[string]bool{}
		for _, name := range release.Extensions {
			if !extensionPattern.MatchString(name) {
				f.add("release %s extension %q is not a valid extension name", release.Version, name)
			}
			if extensions[name] {
				f.add("release %s lists extension %q more than once", release.Version, name)
			}
			extensions[name] = true
		}
	}
	return f.errs
}
