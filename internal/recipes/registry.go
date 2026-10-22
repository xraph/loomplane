package recipes

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	envKeyPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

	// Every image is pinned for both; any other architecture is an error.
	architectures = []string{"amd64", "arm64"}
	targets       = []string{"server", "kubernetes"}
	// builtinOrder is how the five built-in engines are listed.
	builtinOrder = []string{"postgres", "mysql", "mariadb", "redis", "valkey"}
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
	if k.Placement != PlacementServiceProject && k.Placement != PlacementAppProject {
		f.add("placement %q is not %s or %s", k.Placement, PlacementServiceProject, PlacementAppProject)
	}
	validateTargets(f, k.Targets)

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
		if d.External != nil && d.External.Engine != "postgres" && d.External.Engine != "redis" {
			f.add("dependency %q accepts external %q endpoints; only postgres and redis can be checked", d.Name, d.External.Engine)
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
		validateComponent(f, &k.Components[i], components, dependencies, secrets)
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
		switch {
		case o.From != nil && o.PerConsumer:
			f.add("output %q has both from and per consumer; it takes one", o.Name)
		case o.From == nil && !o.PerConsumer:
			f.add("output %q needs from or per consumer", o.Name)
		case o.From != nil:
			if o.From.Dependency != "" {
				f.add("output %q reads a dependency; outputs read components", o.Name)
			} else {
				validateRef(f, fmt.Sprintf("output %q", o.Name), o.From, components, dependencies)
			}
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
			validateImage(f, fmt.Sprintf("release %s component %q", release.Version, c.Name), image)
		}
		for name := range release.Images {
			if _, ok := components[name]; !ok {
				f.add("release %s has an image for %q, which the kind doesn't declare", release.Version, name)
			}
		}
	}
	return f.errs
}

func validateComponent(f *findings, c *Component, components map[string]*Component, dependencies map[string]Dependency, secrets map[string]bool) {
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
		if p.Protocol != "http" && p.Protocol != "tcp" {
			f.add("%s port %q protocol %q is not http or tcp", what, p.Name, p.Protocol)
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
		case port.Protocol != "http":
			f.add("%s readiness port %q is not an http port", what, probe.Port)
		}
		if !strings.HasPrefix(probe.Path, "/") {
			f.add("%s readiness path %q must start with /", what, probe.Path)
		}
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
	}
	if c.Resources.CPUMillis < 0 || c.Resources.MemoryMB < 0 {
		f.add("%s resources must not be negative", what)
	}

	env := map[string]bool{}
	for _, e := range c.Env {
		if !envNamePattern.MatchString(e.Name) {
			f.add("%s env name %q is not a valid variable name", what, e.Name)
		}
		if env[e.Name] {
			f.add("%s sets env %s more than once", what, e.Name)
		}
		env[e.Name] = true
		set := 0
		for _, ok := range []bool{e.Value != "", e.Secret != "", e.From != nil} {
			if ok {
				set++
			}
		}
		if set != 1 {
			f.add("%s env %s must set exactly one of value, secret and from", what, e.Name)
		}
		if e.Secret != "" && !secrets[e.Secret] {
			f.add("%s env %s names secret %q, which the kind doesn't declare", what, e.Name, e.Secret)
		}
		if e.From != nil {
			validateRef(f, fmt.Sprintf("%s env %s", what, e.Name), e.From, components, dependencies)
		}
	}

	files := map[string]bool{}
	for _, file := range c.Files {
		p := file.Path
		switch {
		case !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, ".."):
			f.add("%s file %q must be a clean absolute path", what, p)
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
		switch ref.Field {
		case FieldURL, FieldHost, FieldPort, FieldHostPort, FieldUsername, FieldPassword, FieldDatabase:
		default:
			f.add("%s reads field %q, which a dependency doesn't have", what, ref.Field)
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

func validateImage(f *findings, what string, image Image) {
	if image.Repository == "" {
		f.add("%s has no image repository", what)
	} else if strings.ContainsAny(image.Repository, "@ \t\n") {
		f.add("%s repository %q must not carry a digest or spaces", what, image.Repository)
	}
	for _, architecture := range architectures {
		digest, ok := image.Digests[architecture]
		switch {
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
		f.add("family %q is not one of postgres, mysql, mariadb, redis and valkey", e.Family)
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
		validateImage(f, "release "+release.Version, release.Image)
		if !path.IsAbs(release.MountPath) || path.Clean(release.MountPath) != release.MountPath || release.MountPath == "/" {
			f.add("release %s mount path %q must be a clean absolute path below /", release.Version, release.MountPath)
		}
		if ok && release.BackupFormat != format {
			f.add("release %s backup format %q is not %q, the %s family's", release.Version, release.BackupFormat, format, e.Family)
		}
		if release.KubernetesVolumeMB < 0 || kubernetes && release.KubernetesVolumeMB == 0 {
			f.add("release %s needs a positive Kubernetes volume size", release.Version)
		}
	}
	return f.errs
}
