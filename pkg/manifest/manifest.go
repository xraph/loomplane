// Package manifest is loomplane.yaml: what an application deploys, where,
// and what it needs. It names services by where their source lives, the
// backing resources they use, and the provider each runs on. It says
// nothing about how Loomplane is built: an application deployed with
// Loomplane never needs Forge, or Go.
//
// A manifest is versioned. Parse reads versions 1 and 2 and refuses
// fields it does not know, so a typo is an error
// instead of a setting silently ignored.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	providercontract "github.com/xraph/loomplane/pkg/provider"

	"gopkg.in/yaml.v3"
)

// FileName is the manifest's name in a repository.
const FileName = "loomplane.yaml"

// Version is the default manifest version for existing workflows.
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
	MaxLinks     = 5
)

// LinkClouds are the clouds a link can join, as a pair.
var LinkClouds = [][2]string{{"aws", "gcp"}}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid manifest")

// Manifest is one version of an application's loomplane.yaml.
type Manifest struct {
	Federation *Federation         `json:"federation,omitempty" yaml:"federation,omitempty"`
	Version    int                 `json:"version" yaml:"version"`
	Defaults   Defaults            `json:"defaults,omitzero" yaml:"defaults,omitempty" optional:"true"`
	Services   map[string]Service  `json:"services,omitempty" yaml:"services,omitempty"`
	Resources  map[string]Resource `json:"resources,omitempty" yaml:"resources,omitempty"`
	Gateways   map[string]Gateway  `json:"gateways,omitempty" yaml:"gateways,omitempty"`
	// DatabaseGateway says where the database gateway runs, for the
	// Postgres resources that opt in with gateway: true.
	DatabaseGateway *DatabaseGateway `json:"database_gateway,omitempty" yaml:"database_gateway,omitempty"`
	// Links join two clouds' private networks, so services on one reach
	// what's private on the other.
	Links map[string]Link `json:"links,omitempty" yaml:"links,omitempty"`
	// Environments declare how each environment's releases go live.
	Environments map[string]Environment `json:"environments,omitempty" yaml:"environments,omitempty"`
	Ignore       Ignore                 `json:"ignore,omitzero" yaml:"ignore,omitempty" optional:"true"`
	Held         Held                   `json:"held,omitzero" yaml:"held,omitempty" optional:"true"`
}

// Link is a private network link between two clouds: an HA VPN between
// a Google Cloud project's default network and an AWS account's default
// VPC, for each environment.
type Link struct {
	// Between names the two clouds, such as [gcp, aws].
	Between []string `json:"between" yaml:"between"`
	// Region is where the Google Cloud side runs. Services there reach
	// the link from that region.
	Region string `json:"region,omitempty" yaml:"region,omitempty"`
	// Environments override the region, or turn the link off, by name.
	Environments map[string]LinkSettings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// LinkSettings are what an environment can override on a link.
type LinkSettings struct {
	Region  string `json:"region,omitempty" yaml:"region,omitempty"`
	Enabled *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// For is the link's settings in env. Defaults don't apply: a default
// region is one cloud's, and a link spans two.
func (l Link) For(env string) LinkSettings {
	out := LinkSettings{Region: l.Region}
	if o, ok := l.Environments[env]; ok {
		if o.Region != "" {
			out.Region = o.Region
		}
		out.Enabled = o.Enabled
	}
	return out
}

// Pair is the link's two clouds, sorted.
func (l Link) Pair() [2]string {
	c := slices.Clone(l.Between)
	slices.Sort(c)
	if len(c) != 2 {
		return [2]string{}
	}
	return [2]string{c[0], c[1]}
}

// LinkNames returns the links' names, sorted.
func (m *Manifest) LinkNames() []string { return sortedKeys(m.Links) }

// Defaults apply to every service and resource that doesn't set its own.
type Defaults struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// CloudTags select a Cloud target for every service that sets no
	// cloud_target, cloud_tags, provider or target of its own.
	CloudTags []string `json:"cloud_tags,omitempty" yaml:"cloud_tags,omitempty"`
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
	Kind     string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Settings `json:",inline" yaml:",inline"`
	// Needs names the resources and other services this one talks to.
	// Each one it names gets the service its address, in a variable.
	Needs []string `json:"needs,omitempty" yaml:"needs,omitempty"`
	// NeedOptions are the values this service passes to a need, by need
	// and then by option name: need_options.fabriq.tenant. Only a need
	// that is listed in Needs can have options, and what a need accepts
	// is the planner's question, not the manifest's.
	NeedOptions map[string]map[string]string `json:"need_options,omitempty" yaml:"need_options,omitempty"`
	// Environments override settings in one environment, by name.
	Environments map[string]Settings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Build is how a service's Dockerfile is built.
type Build struct {
	// Context is the directory the build runs in, from the repository's
	// root: "." for the root itself. Empty uses what analysis found, which
	// is the Dockerfile's own directory unless a Compose file says another.
	Context string `json:"context,omitempty" yaml:"context,omitempty"`
	// Builder and Registry name checked connections for a remote image build.
	Builder  string `json:"builder,omitempty" yaml:"builder,omitempty"`
	Registry string `json:"registry,omitempty" yaml:"registry,omitempty"`
	// Image skips the source build and must be pinned by its sha256 digest.
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
}

// Settings are what a service sets, and what an environment can override.
type Settings struct {
	// CapacityMode chooses managed workload reservation semantics.
	CapacityMode string `json:"capacity_mode,omitempty" yaml:"capacity_mode,omitempty"`
	// CloudSQLNetwork is the explicit application route for PSC attachments.
	CloudSQLNetwork *providercontract.CloudSQLApplicationNetwork `json:"cloud_sql_network,omitempty" yaml:"cloud_sql_network,omitempty"`
	// CloudTarget names a target in the environment's assigned Cloud.
	CloudTarget string `json:"cloud_target,omitempty" yaml:"cloud_target,omitempty"`
	// CloudTags select the one target in the assigned Cloud that carries
	// every tag. Planning fails rather than guessing when none or several do.
	CloudTags []string `json:"cloud_tags,omitempty" yaml:"cloud_tags,omitempty"`
	Provider  string   `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region    string   `json:"region,omitempty" yaml:"region,omitempty"`
	// Target names the server or cluster connection for this service.
	Target string `json:"target,omitempty" yaml:"target,omitempty"`
	// Endpoint configures public access on owned infrastructure.
	Endpoint *Endpoint `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// HealthPath is the HTTP readiness path, such as /_/health for Forge.
	HealthPath string `json:"health_path,omitempty" yaml:"health_path,omitempty"`
	Build      *Build `json:"build,omitempty" yaml:"build,omitempty"`
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
	// Farp opts the service in to FARP route discovery by an Octopus
	// gateway. An environment's block replaces the service's whole block.
	Farp *Farp `json:"farp,omitempty" yaml:"farp,omitempty"`
}

// Farp is what a service tells an Octopus gateway about itself. The block
// being there is the opt-in: an empty one takes Octopus's defaults, and
// enabled: false opts out, for example in one environment.
type Farp struct {
	// Gateway names the Octopus resource the service feeds. Empty means
	// the manifest's only one.
	Gateway string `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	// OpenAPI, AsyncAPI, GraphQL, Health and Metrics are paths on the
	// service. Empty leaves the choice to Octopus.
	OpenAPI        string `json:"openapi,omitempty" yaml:"openapi,omitempty"`
	AsyncAPI       string `json:"asyncapi,omitempty" yaml:"asyncapi,omitempty"`
	GraphQL        string `json:"graphql,omitempty" yaml:"graphql,omitempty"`
	Health         string `json:"health,omitempty" yaml:"health,omitempty"`
	Metrics        string `json:"metrics,omitempty" yaml:"metrics,omitempty"`
	GRPCReflection bool   `json:"grpc_reflection,omitempty" yaml:"grpc_reflection,omitempty"`
	// Capabilities are what the service speaks, such as rest or websocket.
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// Port is the port Octopus reaches the service on. Empty means the
	// service's own port.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`
	// Enabled false opts the service out.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// MaxFarpCapabilities is the most capabilities a farp block can name.
const MaxFarpCapabilities = 8

// clone copies the block, so settings never share its slice or pointer.
func (f *Farp) clone() *Farp {
	if f == nil {
		return nil
	}
	out := *f
	out.Capabilities = slices.Clone(f.Capabilities)
	if f.Enabled != nil {
		enabled := *f.Enabled
		out.Enabled = &enabled
	}
	return &out
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
	// GPU asks for GPUs on the server or node the service runs on.
	GPU *GPU `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	// Shm is MiB of /dev/shm. It counts toward Memory.
	Shm int `json:"shm,omitempty" yaml:"shm,omitempty"`
}

// GPU asks for Count GPUs of Kind on one server or node. Planning checks
// Kind against the target.
type GPU struct {
	Kind  string `json:"kind" yaml:"kind"`
	Count int    `json:"count" yaml:"count"`
}

// MaxGPUs is the most GPUs a manifest can ask for on one server or node.
const MaxGPUs = 8

// Resource is a backing service the application needs, such as Postgres.
type Resource struct {
	// CloudSQLPSC opts a PostgreSQL 17 source into a reviewed manual PSC endpoint.
	CloudSQLPSC *providercontract.CloudSQLSourceEndpoint `json:"cloud_sql_psc,omitempty" yaml:"cloud_sql_psc,omitempty"`
	// Kind is what it is: postgres, mysql, redis and so on, the names
	// repository analysis uses.
	Kind     string `json:"kind" yaml:"kind"`
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Target   string `json:"target,omitempty" yaml:"target,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// Version is the engine's version, such as "17" for Postgres. Empty
	// lets the provider choose.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Gateway puts a Postgres resource behind the database gateway.
	// Only postgres resources can.
	Gateway *bool `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	// Uses chooses, by dependency name, where a catalog resource gets each
	// dependency. A dependency left out gets a new one of its own.
	Uses map[string]Use `json:"uses,omitempty" yaml:"uses,omitempty"`
	// GPU asks for GPUs, for catalog resources that can use them.
	GPU *GPU `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	// Settings are values a catalog kind takes, by setting name. The kind
	// says which it has and what each must look like; the planner checks.
	Settings map[string]string `json:"settings,omitempty" yaml:"settings,omitempty"`
	// Environments override the provider or region in one environment, or
	// leave the resource out of it.
	Environments map[string]ResourceSettings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// DatabaseGateway is where the database gateway runs: one per
// application, in front of every Postgres resource that opts in. Loomplane
// builds it and runs it like a service, on a provider, or on the one Cloud
// target that carries every tag.
type DatabaseGateway struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// CloudTags select the one target in the assigned Cloud that carries
	// every tag. Can't be set with provider.
	CloudTags []string `json:"cloud_tags,omitempty" yaml:"cloud_tags,omitempty"`
	// Environments override the provider or region in one environment, or
	// leave the database gateway out of it.
	Environments map[string]ResourceSettings `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// GatewayIn reports whether the resource is behind the database gateway in
// env: the environment's setting if it has one, else the resource's own,
// else no.
func (r Resource) GatewayIn(env string) bool {
	if o, ok := r.Environments[env]; ok && o.Gateway != nil {
		return *o.Gateway
	}
	return r.Gateway != nil && *r.Gateway
}

// Use says how a composed catalog resource gets one of its dependencies:
// from another resource in this manifest, from an external endpoint, or
// switched on or off when the dependency is optional. With none of them
// set, the resource gets a new one of its own.
type Use struct {
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"` // a resource in this manifest
	External string `json:"external,omitempty" yaml:"external,omitempty"` // an external endpoint's ID
	Enabled  *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`   // optional dependencies
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
	// Gateway turns the database gateway on or off for a Postgres
	// resource in one environment. Nothing else reads it.
	Gateway *bool `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	// GPU replaces the resource's GPU request. Gateways can't use it.
	GPU *GPU `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	// Target replaces the resource's target in this environment. An
	// environment that moves the resource to another provider doesn't keep
	// the resource's own target. Gateways can't use it.
	Target string `json:"target,omitempty" yaml:"target,omitempty"`
	// Settings replace the resource's settings one by one: each key set
	// here wins, and the rest come from the resource. Gateways can't use
	// them.
	Settings map[string]string `json:"settings,omitempty" yaml:"settings,omitempty"`
}

// Ignore lists what analysis found that the application doesn't use, so
// Loomplane stops asking about it.
type Ignore struct {
	// Resources are resource kinds, such as "mysql".
	Resources []string `json:"resources,omitempty" yaml:"resources,omitempty"`
}

// Held is how a plan that holds some repositories back treats their
// services, which keep running with the links they were deployed with.
type Held struct {
	// SameAddress is what a plan does when a held service uses something
	// it creates again over what was deleted outside Loomplane, at an
	// address the planner can tell won't change: SameAddressBlock, the
	// default, refuses the plan until the held repository is in scope, and
	// SameAddressWarn lets it through with a warning.
	SameAddress string `json:"same_address,omitempty" yaml:"same_address,omitempty"`
}

// What Held.SameAddress may say.
const (
	SameAddressBlock = "block"
	SameAddressWarn  = "warn"
)

var sameAddressChoices = []string{SameAddressBlock, SameAddressWarn}

// WarnSameAddress reports whether a held service using something created
// again at the same address is a warning rather than a problem.
func (h Held) WarnSameAddress() bool { return h.SameAddress == SameAddressWarn }

// Service kinds a manifest may name, as repository analysis names them.
var serviceKinds = []string{"http-service", "web-app", "static-site", "worker"}

var (
	cloudTargetRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	nameRE        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	farpPathRE    = regexp.MustCompile(`^/[A-Za-z0-9/._~%+:@!$&'()*,;=-]*$`)
	farpCapRE     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envRE         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	tokenRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	gpuKindRE     = regexp.MustCompile(`^[a-z0-9-]+$`)
	kindRE        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// versionRE allows what engines call versions: 17, 8.4, 7.2.4-alpine.
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`)
	routeRE   = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*/?)?$`)
	dirRE     = regexp.MustCompile(`^[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)
	sourceRE  = regexp.MustCompile(`^(?:[a-z]+:)?[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)
	repoRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// endpointRE is a catalog endpoint's ID: the typeid prefix endpoint and
	// a 26 character suffix.
	endpointRE = regexp.MustCompile(`^endpoint_[0-9a-z]{26}$`)
	// optionRE is the name of an option a service passes to a need, the
	// same rule a catalog kind's consumer options follow.
	optionRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// MaxOptionValue is the most characters one need option's value can have.
const MaxOptionValue = 200

// MaxSettingValue is the most characters one resource setting's value can
// have.
const MaxSettingValue = 1024

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
		Version    any       `yaml:"version"`
		Federation yaml.Node `yaml:"federation"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, yamlProblems(err)
	}
	switch v := head.Version.(type) {
	case nil:
		return nil, Problems{{Path: "version", Message: fmt.Sprintf("is missing; this Loomplane reads version %d", Version)}}
	case int:
		if v != Version && v != FederationVersion {
			return nil, Problems{{Path: "version", Message: fmt.Sprintf("is %d; this Loomplane reads version %d", v, Version)}}
		}
	default:
		return nil, Problems{{Path: "version", Message: fmt.Sprintf("must be the number %d", Version)}}
	}

	if head.Federation.Kind != 0 && (head.Version == Version || head.Federation.Tag == "!!null") {
		return nil, Problems{{Path: "federation", Message: "requires a version 2 object"}}
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
	if m.Version != Version && m.Version != FederationVersion {
		add("version", "is %d; this Loomplane reads version %d", m.Version, Version)
	}
	if m.Defaults.Provider != "" && !tokenRE.MatchString(m.Defaults.Provider) {
		add("defaults.provider", "%q isn't a provider name", m.Defaults.Provider)
	}
	if m.Defaults.Region != "" && !tokenRE.MatchString(m.Defaults.Region) {
		add("defaults.region", "%q isn't a region name", m.Defaults.Region)
	}
	checkCloudTags(add, "defaults.cloud_tags", m.Defaults.CloudTags)
	if len(m.Defaults.CloudTags) > 0 && m.Defaults.Provider != "" {
		add("defaults.cloud_tags", "can't be set with defaults.provider; choose Cloud tags or a default provider")
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
	m.validateDatabaseGateway(add)
	m.validateLinks(add)

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
		if r.CloudSQLPSC != nil && (r.Provider != "cloud-sql" || r.Kind != "postgres" || r.Version != "17" || !r.CloudSQLPSC.Valid(r.Region)) {
			add(path+".cloud_sql_psc", "requires explicit cloud-sql PostgreSQL 17 placement and a manual PSC endpoint in the resource region")
		}
		if r.Version != "" && !versionRE.MatchString(r.Version) {
			add(path+".version", "%q isn't a version", r.Version)
		}
		checkGatewayKind(add, path, r.Kind, r.Gateway)
		m.validateUses(add, name, path, r.Uses)
		for _, env := range sortedKeys(r.Environments) {
			o := r.Environments[env]
			epath := path + ".environments." + env
			if !envRE.MatchString(env) {
				add(epath, "isn't an environment name")
			}
			checkToken(add, epath+".provider", "provider", o.Provider)
			checkToken(add, epath+".region", "region", o.Region)
			checkGatewayKind(add, epath, r.Kind, o.Gateway)
			checkGPU(add, epath+".gpu", o.GPU)
			checkResourceSettings(add, epath+".settings", o.Settings)
		}
		checkGPU(add, path+".gpu", r.GPU)
		checkResourceSettings(add, path+".settings", r.Settings)
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
		for _, need := range sortedKeys(s.NeedOptions) {
			if !slices.Contains(s.Needs, need) {
				add(path, "need_options.%s isn't in needs", need)
			}
			checkNeedOptions(add, path+".need_options."+need, s.NeedOptions[need])
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
	if a := m.Held.SameAddress; a != "" && !slices.Contains(sameAddressChoices, a) {
		add("held.same_address", "%q isn't a choice; use %s", a, strings.Join(sameAddressChoices, " or "))
	}
	m.validateEnvironments(add)
	ps = append(ps, ValidateFederation(m)...)
	if len(ps) > 0 {
		return ps
	}
	return nil
}

// checkNeedOptions checks the options one service passes to one need.
func checkNeedOptions(add func(string, string, ...any), path string, options map[string]string) {
	for _, name := range sortedKeys(options) {
		opath := path + "." + name
		if !optionRE.MatchString(name) {
			add(opath, "%q isn't an option name; use lower case letters, digits and underscores, starting with a letter, at most 32 long", name)
		}
		value := options[name]
		if n := utf8.RuneCountInString(value); n > MaxOptionValue {
			add(opath, "is %d characters; the most is %d", n, MaxOptionValue)
		}
		if strings.ContainsFunc(value, unicode.IsControl) {
			add(opath, "can't hold a newline or other control character")
		}
	}
}

func checkToken(add func(string, string, ...any), path, what, value string) {
	if value != "" && !tokenRE.MatchString(value) {
		add(path, "%q isn't a %s name", value, what)
	}
}

// checkGatewayKind refuses gateway: true on anything but Postgres. False
// asks for nothing, so any resource may say it.
func checkGatewayKind(add func(string, string, ...any), path, kind string, gateway *bool) {
	if gateway != nil && *gateway && kind != "postgres" {
		add(path+".gateway", "only postgres resources can use the database gateway")
	}
}

// checkNoGateway refuses the gateway switch where no resource is: it
// belongs on a resource, and an override of a gateway's own would be
// ignored.
func checkNoGateway(add func(string, string, ...any), path string, gateway *bool) {
	if gateway != nil {
		add(path+".gateway", "only resources can opt into the database gateway")
	}
}

// MaxCloudTags bounds how many tags one service selects a target by.
const MaxCloudTags = 8

func checkCloudTags(add func(string, string, ...any), path string, tags []string) {
	if len(tags) > MaxCloudTags {
		add(path, "has %d tags; the most is %d", len(tags), MaxCloudTags)
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if len(tag) > 32 || !cloudTargetRE.MatchString(tag) {
			add(path, "%q must be a lowercase tag of at most 32 characters", tag)
		}
		if seen[tag] {
			add(path, "names %q twice", tag)
		}
		seen[tag] = true
	}
}

// checkFarp reports what is wrong with a farp block. Which Octopus it
// feeds, and whether the service has a port, are the planner's questions.
func checkFarp(add func(string, string, ...any), path string, f *Farp) {
	if f == nil {
		return
	}
	if f.Gateway != "" && !nameRE.MatchString(f.Gateway) {
		add(path+".gateway", "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
	}
	for _, p := range []struct{ key, value string }{
		{"openapi", f.OpenAPI}, {"asyncapi", f.AsyncAPI}, {"graphql", f.GraphQL}, {"health", f.Health}, {"metrics", f.Metrics},
	} {
		switch {
		case p.value == "":
		case !strings.HasPrefix(p.value, "/"):
			add(path+"."+p.key, "must start with /")
		case len(p.value) > 512 || !farpPathRE.MatchString(p.value):
			add(path+"."+p.key, "must be a path of at most 512 characters, with no query string, fragment or spaces")
		}
	}
	if len(f.Capabilities) > MaxFarpCapabilities {
		add(path+".capabilities", "has %d capabilities; the most is %d", len(f.Capabilities), MaxFarpCapabilities)
	}
	seen := map[string]bool{}
	for _, c := range f.Capabilities {
		if !farpCapRE.MatchString(c) {
			add(path+".capabilities", "%q must be a lowercase word of at most 32 characters", c)
		}
		if seen[c] {
			add(path+".capabilities", "names %q twice", c)
		}
		seen[c] = true
	}
	if f.Port < 0 || f.Port > MaxPort {
		add(path+".port", "must be between 1 and %d", MaxPort)
	}
}

func checkSettings(add func(string, string, ...any), path string, s Settings) {
	if n := s.CloudSQLNetwork; n != nil {
		region := s.Region
		if parts := strings.Split(n.Subnetwork, "/"); region == "" && len(parts) == 6 {
			region = parts[3]
		}
		if !n.Valid(region) {
			add(path+".cloud_sql_network", "requires a canonical VPC and regional subnet in the consumer project")
		}
	}
	if s.CloudTarget != "" && (len(s.CloudTarget) > 48 || !cloudTargetRE.MatchString(s.CloudTarget)) {
		add(path+".cloud_target", "must be a lowercase target slug of at most 48 characters")
	}
	checkCloudTags(add, path+".cloud_tags", s.CloudTags)
	if s.CloudTarget != "" && len(s.CloudTags) > 0 {
		add(path+".cloud_tags", "can't be set with cloud_target; name the target or select it by tags")
	}
	checkToken(add, path+".provider", "provider", s.Provider)
	checkToken(add, path+".region", "region", s.Region)
	checkInfrastructure(add, path, s)
	checkFarp(add, path+".farp", s.Farp)
	if s.Port < 0 || s.Port > MaxPort {
		add(path+".port", "must be between 1 and %d", MaxPort)
	}
	if s.CapacityMode != "" && s.CapacityMode != "guaranteed" && s.CapacityMode != "best_effort" {
		add(path+".capacity_mode", "must be guaranteed or best_effort")
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
		checkGPU(add, path+".size.gpu", s.Size.GPU)
		if s.Size.Shm < 0 {
			add(path+".size.shm", "can't be negative")
		} else if s.Size.Shm >= s.Size.Memory && s.Size.Shm > 0 {
			add(path+".size.shm", "(%d MiB) must be less than memory (%d MiB), which it counts toward", s.Size.Shm, s.Size.Memory)
		}
	}
}

// checkResourceSettings checks a resource's settings: names and lengths.
// Whether the kind takes them is the planner's question.
func checkResourceSettings(add func(string, string, ...any), path string, settings map[string]string) {
	for _, name := range sortedKeys(settings) {
		if !optionRE.MatchString(name) {
			add(path+"."+name, "the name must be a lower case letter, then up to 31 lower case letters, digits and underscores")
		}
		if n := utf8.RuneCountInString(settings[name]); n > MaxSettingValue {
			add(path+"."+name, "is %d characters; the most is %d", n, MaxSettingValue)
		}
	}
}

func checkGPU(add func(string, string, ...any), path string, g *GPU) {
	if g == nil {
		return
	}
	if g.Kind == "" {
		add(path+".kind", "is required, such as nvidia-l4")
	} else if !gpuKindRE.MatchString(g.Kind) {
		add(path+".kind", "%q isn't a GPU kind", g.Kind)
	}
	if g.Count < 1 || g.Count > MaxGPUs {
		add(path+".count", "must be between 1 and %d", MaxGPUs)
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

// validateUses checks one resource's choices of where its dependencies
// come from. Whether the kind has a dependency by that name is the
// planner's question, not the manifest's.
func (m *Manifest) validateUses(add func(string, string, ...any), resource, path string, uses map[string]Use) {
	for _, dep := range sortedKeys(uses) {
		u := uses[dep]
		upath := path + ".uses." + dep
		if !nameRE.MatchString(dep) {
			add(upath, "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
		}
		if u.Resource != "" && u.External != "" {
			add(upath, "choose resource or external, not both")
		}
		if u.Resource != "" {
			if _, ok := m.Resources[u.Resource]; !ok {
				add(upath+".resource", "%q isn't a resource in this manifest", u.Resource)
			} else if u.Resource == resource {
				add(upath+".resource", "a resource can't use itself")
			}
		}
		if u.External != "" && !endpointRE.MatchString(u.External) {
			add(upath+".external", "%q isn't a catalog endpoint ID", u.External)
		}
		if u.Enabled != nil && !*u.Enabled && (u.Resource != "" || u.External != "") {
			add(upath, "enabled: false can't be set with resource or external")
		}
	}
}

func (m *Manifest) validateLinks(add func(string, string, ...any)) {
	if len(m.Links) > MaxLinks {
		add("links", "has %d links; the most is %d", len(m.Links), MaxLinks)
	}
	pairs := map[[2]string]string{}
	for _, name := range m.LinkNames() {
		l := m.Links[name]
		path := "links." + name
		if !nameRE.MatchString(name) {
			add(path, "the name must be lower case letters, digits and hyphens, starting with a letter, at most 40 long")
		}
		_, isService := m.Services[name]
		_, isResource := m.Resources[name]
		_, isGateway := m.Gateways[name]
		if isService || isResource || isGateway {
			add(path, "a service, resource or gateway has the same name")
		}
		pair := l.Pair()
		switch {
		case len(l.Between) != 2 || l.Between[0] == l.Between[1]:
			add(path+".between", "names two different clouds, such as [gcp, aws]")
		case !slices.Contains(LinkClouds, pair):
			add(path+".between", "Loomplane links gcp and aws; it can't link %s and %s", pair[0], pair[1])
		case pairs[pair] != "":
			add(path+".between", "%s already links %s and %s", pairs[pair], pair[0], pair[1])
		default:
			pairs[pair] = name
		}
		checkToken(add, path+".region", "region", l.Region)
		for _, env := range sortedKeys(l.Environments) {
			epath := path + ".environments." + env
			if !envRE.MatchString(env) {
				add(epath, "isn't an environment name")
			}
			checkToken(add, epath+".region", "region", l.Environments[env].Region)
		}
	}
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
			checkNoGateway(add, epath, o.Gateway)
			if o.GPU != nil {
				add(epath+".gpu", "a gateway can't use GPUs")
			}
			checkNoTargetOrSettings(add, epath, o)
		}
	}
}

func (m *Manifest) validateDatabaseGateway(add func(string, string, ...any)) {
	g := m.DatabaseGateway
	if g == nil {
		return
	}
	const path = "database_gateway"
	checkToken(add, path+".provider", "provider", g.Provider)
	checkToken(add, path+".region", "region", g.Region)
	checkCloudTags(add, path+".cloud_tags", g.CloudTags)
	if len(g.CloudTags) > 0 && g.Provider != "" {
		add(path+".cloud_tags", "can't be set with database_gateway.provider; choose Cloud tags or a provider")
	}
	for _, env := range sortedKeys(g.Environments) {
		o := g.Environments[env]
		epath := path + ".environments." + env
		if !envRE.MatchString(env) {
			add(epath, "isn't an environment name")
		}
		checkToken(add, epath+".provider", "provider", o.Provider)
		checkToken(add, epath+".region", "region", o.Region)
		checkNoGateway(add, epath, o.Gateway)
		checkNoTargetOrSettings(add, epath, o)
	}
}

// checkNoTargetOrSettings refuses the overrides only a resource can use
// on a gateway's or the database gateway's environment.
func checkNoTargetOrSettings(add func(string, string, ...any), path string, o ResourceSettings) {
	if o.Target != "" {
		add(path+".target", "a gateway can't use a target")
	}
	if o.Settings != nil {
		add(path+".settings", "a gateway can't use settings")
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
		return cloudTagDefaults(out, d)
	}
	if o.Provider != "" && o.Provider != out.Provider {
		out.Target, out.Endpoint = "", nil
		if out.Provider != "" || o.Provider != "cloud-run" {
			out.CloudSQLNetwork = nil
		}
	}
	if o.Target != "" && o.Target != out.Target {
		out.Endpoint = nil
	}
	out.Provider, out.Region = override(out.Provider, out.Region, o.Provider, o.Region, d)
	// An environment names its target one way, replacing the service's.
	if o.CloudTarget != "" {
		out.CloudTarget, out.CloudTags = o.CloudTarget, nil
	}
	if len(o.CloudTags) > 0 {
		out.CloudTarget, out.CloudTags = "", o.CloudTags
	}
	if o.Target != "" {
		out.Target = o.Target
	}
	if o.Endpoint != nil {
		out.Endpoint = o.Endpoint
	}
	if o.CloudSQLNetwork != nil {
		out.CloudSQLNetwork = o.CloudSQLNetwork
	}
	if o.HealthPath != "" {
		out.HealthPath = o.HealthPath
	}
	if o.Build != nil {
		out.Build = o.Build
	}
	if o.Port != 0 {
		out.Port = o.Port
	}
	if o.CapacityMode != "" {
		out.CapacityMode = o.CapacityMode
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
	// An environment's block replaces the service's whole block.
	if o.Farp != nil {
		out.Farp = o.Farp
	}
	return cloudTagDefaults(out, d)
}

// cloudTagDefaults gives a service with no placement of its own the
// manifest's default Cloud tags.
func cloudTagDefaults(out Settings, d Defaults) Settings {
	out.Farp = out.Farp.clone()
	if out.CloudSQLNetwork != nil {
		network := *out.CloudSQLNetwork
		out.CloudSQLNetwork = &network
	}
	if len(d.CloudTags) > 0 && out.CloudTarget == "" && len(out.CloudTags) == 0 && out.Provider == "" && out.Target == "" {
		out.CloudTags = slices.Clone(d.CloudTags)
	}
	return out
}

// For returns a resource's settings in an environment, the same way. The
// environment's target replaces the resource's, and an environment that
// moves the resource to another provider doesn't keep the resource's
// target. Each setting the environment sets replaces that one setting.
// Settings is a new map, nil when there are none.
func (r Resource) For(env string, d Defaults) ResourceSettings {
	out := ResourceSettings{}
	out.Provider, out.Region = place(r.Provider, r.Region, d)
	out.GPU = r.GPU
	out.Target = r.Target
	o, ok := r.Environments[env]
	if !ok {
		out.Settings = mergeSettings(r.Settings, nil)
		return out
	}
	if o.GPU != nil {
		out.GPU = o.GPU
	}
	if o.Provider != "" && o.Provider != out.Provider {
		out.Target = ""
	}
	if o.Target != "" {
		out.Target = o.Target
	}
	out.Settings = mergeSettings(r.Settings, o.Settings)
	out.Provider, out.Region = override(out.Provider, out.Region, o.Provider, o.Region, d)
	out.Enabled = o.Enabled
	return out
}

// In returns the resource as env runs it: the target, GPU and settings
// For resolves, and the environment's provider and region when it sets
// them. It doesn't fill in the defaults, so r.In(env, d).For(env, d) is
// r.For(env, d). Environments stay, so On and GatewayIn read the same.
// Uses and the GPU are copies, so writing to them leaves r alone.
func (r Resource) In(env string, d Defaults) Resource {
	s := r.For(env, d)
	out := r
	out.Target, out.GPU, out.Settings = s.Target, s.GPU, s.Settings
	if out.GPU != nil {
		gpu := *out.GPU
		out.GPU = &gpu
	}
	if r.Uses != nil {
		out.Uses = make(map[string]Use, len(r.Uses))
		for name, u := range r.Uses {
			if u.Enabled != nil {
				enabled := *u.Enabled
				u.Enabled = &enabled
			}
			out.Uses[name] = u
		}
	}
	o, ok := r.Environments[env]
	if !ok {
		return out
	}
	if o.Provider != "" {
		// A region belongs to a provider, so moving to another one
		// leaves the resource's region behind, as For does.
		if provider, _ := place(r.Provider, r.Region, d); o.Provider != provider {
			out.Region = ""
		}
		out.Provider = o.Provider
	}
	if o.Region != "" {
		out.Region = o.Region
	}
	return out
}

// mergeSettings returns a new map of base with each key in over replacing
// it, or nil when both are empty.
func mergeSettings(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	maps.Copy(out, base)
	maps.Copy(out, over)
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
