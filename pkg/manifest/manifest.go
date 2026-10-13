// Package manifest is loomplane.yaml: what an application deploys, where,
// and what it needs. It names services by where their source lives, the
// backing resources they use, and the provider each runs on. It says
// nothing about how Loomplane is built: an application deployed with
// Loomplane never needs Forge, or Go.
//
// A manifest is versioned. Version 1 is the only one so far; Parse refuses
// any other, and refuses fields it doesn't know, so a typo is an error
// instead of a setting silently ignored.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the manifest's name in a repository.
const FileName = "loomplane.yaml"

// Version is the manifest version this package reads and writes.
const Version = 1

// MaxSize is the largest manifest Parse reads, in bytes.
const MaxSize = 256 << 10

// Limits on what a manifest may ask for. They're generous; a provider may
// allow less, and the planner says so.
const (
	MaxServices  = 100
	MaxResources = 100
	MaxInstances = 100
	MaxCPU       = 8
	MinMemory    = 128
	MaxMemory    = 32768
	MaxPort      = 65535
	MaxGateways  = 10
	MaxRoutes    = 50
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid manifest")

// Manifest is one version of an application's loomplane.yaml.
type Manifest struct {
	Version   int                 `json:"version" yaml:"version"`
	Defaults  Defaults            `json:"defaults,omitzero" yaml:"defaults,omitempty"`
	Services  map[string]Service  `json:"services,omitempty" yaml:"services,omitempty"`
	Resources map[string]Resource `json:"resources,omitempty" yaml:"resources,omitempty"`
	Gateways  map[string]Gateway  `json:"gateways,omitempty" yaml:"gateways,omitempty"`
	Ignore    Ignore              `json:"ignore,omitzero" yaml:"ignore,omitempty"`
}

// Defaults apply to every service and resource that doesn't set its own.
type Defaults struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
}

// Service is something the application runs.
type Service struct {
	// Repo is the alias of the repository the service is in. A project
	// with one repository can leave it out.
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty"`
	// Source is where the service's code is, as repository analysis found
	// it: a Go main package's directory, a Rust binary's source file, a
	// Node package's, Python project's or JVM module's directory, or a
	// Dockerfile. When two ecosystems have a service at the same path,
	// prefix the ecosystem: "go:services/api".
	Source string `json:"source" yaml:"source"`
	// Kind corrects what analysis decided the service is: http-service,
	// web-app, static-site or worker. Empty keeps what analysis found.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`
	// Build changes how the service's Dockerfile is built.
	Build    *Build `json:"build,omitempty" yaml:"build,omitempty"`
	Settings `json:",inline" yaml:",inline"`
	// Needs names the resources and other services this one talks to.
	// Each one it names gets the service its address, in a variable.
	Needs []string `json:"needs,omitempty" yaml:"needs,omitempty"`
	// Environments override settings in one environment, by name.
	Environments map[string]Settings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Build is how a service's Dockerfile is built.
type Build struct {
	// Context is the directory the build runs in, from the repository's
	// root: "." for the root itself. Empty uses what analysis found, which
	// is the Dockerfile's own directory unless a Compose file says another.
	Context string `json:"context,omitempty" yaml:"context,omitempty"`
}

// Settings are what a service sets, and what an environment can override.
type Settings struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// Port is the port the service listens on. Empty uses what analysis
	// found in its Dockerfile.
	Port  int    `json:"port,omitempty" yaml:"port,omitempty"`
	Scale *Scale `json:"scale,omitempty" yaml:"scale,omitempty"`
	Size  *Size  `json:"size,omitempty" yaml:"size,omitempty"`
	// Public says whether the service gets an address on the internet.
	// Empty means yes for anything that serves HTTP, and no for a worker.
	Public *bool `json:"public,omitempty" yaml:"public,omitempty"`
	// Enabled false leaves the service out, here or in one environment.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// Scale is how many instances of a service run.
type Scale struct {
	Min int `json:"min" yaml:"min"`
	Max int `json:"max" yaml:"max"`
}

// Size is what each instance of a service gets.
type Size struct {
	// CPU is in virtual CPUs, such as 0.5 or 2.
	CPU float64 `json:"cpu" yaml:"cpu"`
	// Memory is in MiB.
	Memory int `json:"memory" yaml:"memory"`
}

// Resource is a backing service the application needs, such as Postgres.
type Resource struct {
	// Kind is what it is: postgres, mysql, redis and so on, the names
	// repository analysis uses.
	Kind     string `json:"kind" yaml:"kind"`
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// Version is the engine's version, such as "17" for Postgres. Empty
	// lets the provider choose.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Environments override the provider or region in one environment, or
	// leave the resource out of it.
	Environments map[string]ResourceSettings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Gateway is one public address in front of several services, routing
// each request by its path. Loomplane builds it and runs it on a target
// like any service.
type Gateway struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// Routes send paths to services. The longest path that matches wins,
	// so "/" catches whatever the others don't.
	Routes []Route `json:"routes" yaml:"routes"`
	// Environments override the provider or region in one environment, or
	// leave the gateway out of it.
	Environments map[string]ResourceSettings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Route sends requests under a path to a service.
type Route struct {
	// Path is a prefix, such as /api. A request for /api or anything
	// under /api/ matches it.
	Path string `json:"path" yaml:"path"`
	// To is the service, by its name in this manifest.
	To string `json:"to" yaml:"to"`
	// StripPrefix removes the path before the request reaches the
	// service, so /api/users arrives as /users.
	StripPrefix bool `json:"strip_prefix,omitempty" yaml:"strip_prefix,omitempty"`
}

// ResourceSettings are what an environment can override on a resource.
type ResourceSettings struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// Ignore lists what analysis found that the application doesn't use, so
// Loomplane stops asking about it.
type Ignore struct {
	// Resources are resource kinds, such as "mysql".
	Resources []string `json:"resources,omitempty" yaml:"resources,omitempty"`
}

// Service kinds a manifest may name, as repository analysis names them.
var serviceKinds = []string{"http-service", "web-app", "static-site", "worker"}

var (
	nameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	envRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	kindRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// versionRE allows what engines call versions: 17, 8.4, 7.2.4-alpine.
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`)
	routeRE   = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*/?)?$`)
	dirRE     = regexp.MustCompile(`^[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)
	sourceRE  = regexp.MustCompile(`^(?:[a-z]+:)?[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)
	repoRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// Problem is one thing wrong with a manifest, at a path such as
// "services.api.scale".
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Problems are everything wrong with a manifest. It's an error, and it
// wraps ErrInvalid.
type Problems []Problem //nolint:errname // manifest.Problems reads better than manifest.ProblemsError

func (ps Problems) Error() string {
	msgs := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Path == "" {
			msgs = append(msgs, p.Message)
			continue
		}
		msgs = append(msgs, p.Path+": "+p.Message)
	}
	return "invalid manifest: " + strings.Join(msgs, "; ")
}

// Unwrap lets errors.Is find ErrInvalid.
func (ps Problems) Unwrap() error { return ErrInvalid }

// Parse reads and validates a manifest written as YAML (or JSON, which is
// YAML too).
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxSize {
		return nil, Problems{{Message: fmt.Sprintf("it's larger than %d KiB", MaxSize>>10)}}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, Problems{{Message: "it's empty; it needs at least \"version: 1\""}}
	}
	// Read the version first, so a manifest from a later Loomplane fails
	// on its version, not on whichever new field comes first.
	var head struct {
		Version any `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, yamlProblems(err)
	}
	switch v := head.Version.(type) {
	case nil:
		return nil, Problems{{Path: "version", Message: fmt.Sprintf("is missing; this Loomplane reads version %d", Version)}}
	case int:
		if v != Version {
			return nil, Problems{{Path: "version", Message: fmt.Sprintf("is %d; this Loomplane reads version %d", v, Version)}}
		}
	default:
		return nil, Problems{{Path: "version", Message: fmt.Sprintf("must be the number %d", Version)}}
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, yamlProblems(err)
	}
	// One document per file: a second one would be silently ignored.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, Problems{{Message: "it holds more than one YAML document"}}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// yamlProblems turns a YAML error into one problem per line it reports,
// without the package's prefixes, which is all a person editing the file
// needs.
func yamlProblems(err error) Problems {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:")
	var ps Problems
	for _, line := range strings.Split(msg, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ps = append(ps, Problem{Message: line})
		}
	}
	if len(ps) == 0 {
		ps = Problems{{Message: msg}}
	}
	return ps
}

// Marshal writes m as YAML, in a stable order: the same manifest always
// gives the same bytes.
func Marshal(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// Validate checks what can be checked without knowing the repository or
// the providers: names, references and ranges. The planner checks the rest.
func (m *Manifest) Validate() error {
	var ps Problems
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	if m.Version != Version {
		add("version", "is %d; this Loomplane reads version %d", m.Version, Version)
	}
	if m.Defaults.Provider != "" && !tokenRE.MatchString(m.Defaults.Provider) {
		add("defaults.provider", "%q isn't a provider name", m.Defaults.Provider)
	}
	if m.Defaults.Region != "" && !tokenRE.MatchString(m.Defaults.Region) {
		add("defaults.region", "%q isn't a region name", m.Defaults.Region)
	}
	if len(m.Services) > MaxServices {
		add("services", "has %d services; the most is %d", len(m.Services), MaxServices)
	}
	if len(m.Resources) > MaxResources {
		add("resources", "has %d resources; the most is %d", len(m.Resources), MaxResources)
	}
	if len(m.Gateways) > MaxGateways {
		add("gateways", "has %d gateways; the most is %d", len(m.Gateways), MaxGateways)
	}
	m.validateGateways(add)

	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		path := "resources." + name
		if !nameRE.MatchString(name) {
			add(path, "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
		}
		if _, clash := m.Services[name]; clash {
			add(path, "a service has the same name; needs couldn't tell them apart")
		}
		switch {
		case r.Kind == "":
			add(path+".kind", "is required, such as postgres or redis")
		case !kindRE.MatchString(r.Kind):
			add(path+".kind", "%q isn't a resource kind", r.Kind)
		}
		checkToken(add, path+".provider", "provider", r.Provider)
		checkToken(add, path+".region", "region", r.Region)
		if r.Version != "" && !versionRE.MatchString(r.Version) {
			add(path+".version", "%q isn't a version", r.Version)
		}
		for _, env := range sortedKeys(r.Environments) {
			o := r.Environments[env]
			epath := path + ".environments." + env
			if !envRE.MatchString(env) {
				add(epath, "isn't an environment name")
			}
			checkToken(add, epath+".provider", "provider", o.Provider)
			checkToken(add, epath+".region", "region", o.Region)
		}
	}

	for _, name := range m.ServiceNames() {
		s := m.Services[name]
		path := "services." + name
		if !nameRE.MatchString(name) {
			add(path, "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
		}
		switch {
		case strings.TrimSpace(s.Source) == "":
			add(path+".source", "is required: where the service's code is, such as services/api")
		case !sourceRE.MatchString(s.Source) || strings.Contains(s.Source, "..") || strings.HasPrefix(s.Source, "/"):
			add(path+".source", "%q isn't a path in the repository", s.Source)
		}
		if s.Repo != "" && !repoRE.MatchString(s.Repo) {
			add(path+".repo", "%q isn't a repository alias", s.Repo)
		}
		if s.Kind != "" && !slices.Contains(serviceKinds, s.Kind) {
			add(path+".kind", "%q isn't a service kind; use one of %s", s.Kind, strings.Join(serviceKinds, ", "))
		}
		if s.Build != nil && s.Build.Context != "" && !isDir(s.Build.Context) {
			add(path+".build.context", "%q isn't a directory in the repository; use . for its root", s.Build.Context)
		}
		checkSettings(add, path, s.Settings)
		seen := map[string]bool{}
		for i, need := range s.Needs {
			npath := fmt.Sprintf("%s.needs[%d]", path, i)
			_, isResource := m.Resources[need]
			_, isService := m.Services[need]
			switch {
			case need == name:
				add(npath, "a service can't need itself")
			case !isResource && !isService:
				add(npath, "%q isn't a service or resource in this manifest", need)
			case seen[need]:
				add(npath, "%q is listed twice", need)
			}
			seen[need] = true
		}
		for _, env := range sortedKeys(s.Environments) {
			epath := path + ".environments." + env
			if !envRE.MatchString(env) {
				add(epath, "isn't an environment name")
			}
			checkSettings(add, epath, s.Environments[env])
		}
	}
	if cycle := m.serviceCycle(); cycle != nil {
		add("services", "needs go round in a circle (%s), so none of them could start first", strings.Join(cycle, " → "))
	}

	for i, kind := range m.Ignore.Resources {
		if !kindRE.MatchString(kind) {
			add(fmt.Sprintf("ignore.resources[%d]", i), "%q isn't a resource kind", kind)
		}
	}
	if len(ps) > 0 {
		return ps
	}
	return nil
}

func checkToken(add func(string, string, ...any), path, what, value string) {
	if value != "" && !tokenRE.MatchString(value) {
		add(path, "%q isn't a %s name", value, what)
	}
}

func checkSettings(add func(string, string, ...any), path string, s Settings) {
	checkToken(add, path+".provider", "provider", s.Provider)
	checkToken(add, path+".region", "region", s.Region)
	if s.Port < 0 || s.Port > MaxPort {
		add(path+".port", "must be between 1 and %d", MaxPort)
	}
	if s.Scale != nil {
		switch {
		case s.Scale.Min < 0:
			add(path+".scale.min", "can't be negative")
		case s.Scale.Max < 1:
			add(path+".scale.max", "must be at least 1")
		case s.Scale.Max > MaxInstances:
			add(path+".scale.max", "can be at most %d", MaxInstances)
		case s.Scale.Min > s.Scale.Max:
			add(path+".scale", "min (%d) is more than max (%d)", s.Scale.Min, s.Scale.Max)
		}
	}
	if s.Size != nil {
		if s.Size.CPU <= 0 || s.Size.CPU > MaxCPU {
			add(path+".size.cpu", "must be more than 0 and at most %d", MaxCPU)
		}
		if s.Size.Memory < MinMemory || s.Size.Memory > MaxMemory {
			add(path+".size.memory", "must be between %d and %d MiB", MinMemory, MaxMemory)
		}
	}
}

// serviceCycle returns a cycle among services' needs, or nil.
func (m *Manifest) serviceCycle() []string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(string) []string
	visit = func(name string) []string {
		state[name] = visiting
		stack = append(stack, name)
		for _, need := range m.Services[name].Needs {
			if _, ok := m.Services[need]; !ok || need == name {
				continue
			}
			switch state[need] {
			case visiting:
				i := slices.Index(stack, need)
				return append(slices.Clone(stack[i:]), need)
			case unvisited:
				if c := visit(need); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, name := range m.ServiceNames() {
		if state[name] == unvisited {
			if c := visit(name); c != nil {
				return c
			}
		}
	}
	return nil
}

func (m *Manifest) validateGateways(add func(string, string, ...any)) {
	for _, name := range m.GatewayNames() {
		g := m.Gateways[name]
		path := "gateways." + name
		if !nameRE.MatchString(name) {
			add(path, "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
		}
		_, isService := m.Services[name]
		_, isResource := m.Resources[name]
		if isService || isResource {
			add(path, "a service or resource has the same name; they'd share an address")
		}
		checkToken(add, path+".provider", "provider", g.Provider)
		checkToken(add, path+".region", "region", g.Region)
		switch {
		case len(g.Routes) == 0:
			add(path+".routes", "needs at least one route, such as {path: /, to: web}")
		case len(g.Routes) > MaxRoutes:
			add(path+".routes", "has %d routes; the most is %d", len(g.Routes), MaxRoutes)
		}
		seen := map[string]bool{}
		for i, r := range g.Routes {
			rpath := fmt.Sprintf("%s.routes[%d]", path, i)
			switch {
			case !routeRE.MatchString(r.Path):
				add(rpath+".path", "%q isn't a path prefix, such as /api", r.Path)
			case seen[strings.TrimSuffix(r.Path, "/")]:
				add(rpath+".path", "%q is routed twice", r.Path)
			}
			seen[strings.TrimSuffix(r.Path, "/")] = true
			if _, ok := m.Services[r.To]; !ok {
				add(rpath+".to", "%q isn't a service in this manifest", r.To)
			}
		}
		for _, env := range sortedKeys(g.Environments) {
			o := g.Environments[env]
			epath := path + ".environments." + env
			if !envRE.MatchString(env) {
				add(epath, "isn't an environment name")
			}
			checkToken(add, epath+".provider", "provider", o.Provider)
			checkToken(add, epath+".region", "region", o.Region)
		}
	}
}

// GatewayNames returns the gateways' names, sorted.
// isDir says whether p is a clean directory path inside the repository:
// no empty, "." or ".." segments, so it can't climb out.
func isDir(p string) bool {
	if p == "." {
		return true
	}
	if !dirRE.MatchString(p) {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (m *Manifest) GatewayNames() []string { return sortedKeys(m.Gateways) }

// For returns a gateway's settings in an environment, the way a
// resource's are.
func (g Gateway) For(env string, d Defaults) ResourceSettings {
	return Resource{Provider: g.Provider, Region: g.Region, Environments: g.Environments}.For(env, d)
}

// ServiceNames returns the services' names, sorted.
func (m *Manifest) ServiceNames() []string { return sortedKeys(m.Services) }

// ResourceNames returns the resources' names, sorted.
func (m *Manifest) ResourceNames() []string { return sortedKeys(m.Resources) }

// Ignored reports whether the manifest ignores a resource kind.
func (m *Manifest) Ignored(kind string) bool { return slices.Contains(m.Ignore.Resources, kind) }

// For returns a service's settings in an environment: the environment's
// overrides on the service's own, on the manifest's defaults. A region
// belongs to a provider, so the default region only comes with the default
// provider, and an environment that moves a service to another provider
// doesn't take its region along.
func (s Service) For(env string, d Defaults) Settings {
	out := s.Settings
	out.Provider, out.Region = place(out.Provider, out.Region, d)
	o, ok := s.Environments[env]
	if !ok {
		return out
	}
	out.Provider, out.Region = override(out.Provider, out.Region, o.Provider, o.Region, d)
	if o.Port != 0 {
		out.Port = o.Port
	}
	if o.Scale != nil {
		out.Scale = o.Scale
	}
	if o.Size != nil {
		out.Size = o.Size
	}
	if o.Public != nil {
		out.Public = o.Public
	}
	if o.Enabled != nil {
		out.Enabled = o.Enabled
	}
	return out
}

// For returns a resource's settings in an environment, the same way.
func (r Resource) For(env string, d Defaults) ResourceSettings {
	out := ResourceSettings{}
	out.Provider, out.Region = place(r.Provider, r.Region, d)
	o, ok := r.Environments[env]
	if !ok {
		return out
	}
	out.Provider, out.Region = override(out.Provider, out.Region, o.Provider, o.Region, d)
	out.Enabled = o.Enabled
	return out
}

func place(provider, region string, d Defaults) (p, r string) {
	if provider == "" {
		provider = d.Provider
	}
	if region == "" && provider == d.Provider {
		region = d.Region
	}
	return provider, region
}

func override(provider, region, newProvider, newRegion string, d Defaults) (p, r string) {
	if newProvider != "" && newProvider != provider {
		provider, region = place(newProvider, "", d)
	}
	if newRegion != "" {
		region = newRegion
	}
	return provider, region
}

// On reports whether settings leave the service or resource in.
func On(enabled *bool) bool { return enabled == nil || *enabled }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
