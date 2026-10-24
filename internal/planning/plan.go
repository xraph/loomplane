// Package planning turns an application graph, its manifest and the
// provider catalog into a plan: the actions that would make an environment
// match the manifest, in order, with who carries out each one. Planning
// never touches a cloud. A plan is reviewed and approved before anything
// acts on it, and approval names the plan's revision, so what gets approved
// is exactly what was reviewed.
package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Plan statuses. A plan starts planning, then is ready for a decision or
// blocked by problems; a ready plan is approved, rejected, expires, or is
// superseded by a newer plan for the same environment.
const (
	StatusPlanning   = "planning"
	StatusReady      = "ready"
	StatusBlocked    = "blocked"
	StatusApproved   = "approved"
	StatusRejected   = "rejected"
	StatusExpired    = "expired"
	StatusSuperseded = "superseded"
	StatusFailed     = "failed"
)

// Errors the service and stores return.
var (
	ErrNotFound      = errors.New("plan not found")
	ErrInvalid       = errors.New("invalid plan request")
	ErrNoManifest    = errors.New("this project has no manifest yet; choose where its services run first")
	ErrNoAnalysis    = errors.New("this project's repository hasn't been analyzed yet; analyze it first")
	ErrNotReady      = errors.New("this plan isn't waiting for a decision")
	ErrStale         = errors.New("the plan changed since you reviewed it; review it again")
	ErrSuperseded    = errors.New("a newer plan for this environment replaced this one")
	ErrUnavailable   = errors.New("plans can't be decided right now; try again")
	ErrNoSources     = errors.New("this project has no repositories; add one first")
	ErrNeverDeployed = errors.New("a repository this plan holds back has never been deployed here")
)

// Op is what an action does.
type Op string

// Ops.
const (
	Create  Op = "create"
	Update  Op = "update"
	Replace Op = "replace"
	Delete  Op = "delete"
	Keep    Op = "keep"
)

// Subject is what an action acts on.
type Subject string

// Subjects.
const (
	ServiceSubject  Subject = "service"
	ResourceSubject Subject = "resource"
)

// Action is one step of a plan.
type Action struct {
	Compensation *CloudCompensation `json:"compensation,omitempty"`

	Routes []cloudinfra.Route `json:"routes,omitempty"`

	// AdoptCloudPlacement persists reviewed metadata without a provider call.
	AdoptCloudPlacement bool           `json:"adopt_cloud_placement,omitempty"`
	CloudAdoption       *CloudAdoption `json:"cloud_adoption,omitempty"`
	// ID is stable for a subject: "service:api", "resource:db".
	ID       string  `json:"id"`
	Op       Op      `json:"op"`
	Subject  Subject `json:"subject"`
	Name     string  `json:"name"`
	Provider string  `json:"provider"`
	// ConnectionID pins a service target independently of cloud defaults.
	ConnectionID string          `json:"connection_id,omitempty"`
	Region       string          `json:"region,omitempty"`
	Engine       provider.Engine `json:"engine"`
	// Creates names what the provider creates, such as "Cloud Run service".
	Creates string `json:"creates"`
	// Service is the desired service, for service actions other than
	// deletes.
	Service *ServiceConfig `json:"service,omitempty"`
	// Resource is the desired resource, for resource actions other than
	// deletes.
	Resource *ResourceConfig `json:"resource,omitempty"`
	// Changes are what an update or a replacement changes.
	Changes []Change `json:"changes,omitempty"`
	// Reason says why the action is in the plan, for people.
	Reason string `json:"reason"`
	// DependsOn are actions that must finish first.
	DependsOn []string `json:"depends_on,omitempty"`
	// Step orders actions: every action in a step can run at once, after
	// the steps before it.
	Step int `json:"step"`
}

// ServiceConfig is everything a deployment of a service is made from. A
// binding keeps the one last applied, and the next plan compares against it.
type AppliedInputs struct {
	Variables   string      `json:"variables,omitempty"`
	Source      *PlanSource `json:"source,omitempty"`
	ArtifactKey string      `json:"artifact_key,omitempty"`
}

type ServiceConfig struct {
	CapacityIntent *CapacityIntent    `json:"capacity_intent,omitempty"`
	AppliedInputs  *AppliedInputs     `json:"applied_inputs,omitempty"`
	CloudPlacement *CloudPlacement    `json:"cloud_placement,omitempty"`
	Catalog        *recipes.Recipe    `json:"catalog,omitempty"`
	Provider       string             `json:"provider"`
	Region         string             `json:"region,omitempty"`
	Target         string             `json:"target,omitempty"`
	TargetSettings map[string]string  `json:"target_settings,omitempty"`
	Endpoint       *manifest.Endpoint `json:"endpoint,omitempty"`
	HealthPath     string             `json:"health_path,omitempty"`
	BuildTarget    *BuildTarget       `json:"build_target,omitempty"`
	Workload       provider.Workload  `json:"workload"`
	Execution      provider.Execution `json:"execution"`
	// Service is the service's ID in the analysis's graph, which its
	// variables are set against.
	Service string `json:"service"`
	// Source is the service's entrypoint in its repository.
	Source string `json:"source"`
	// Repo is the alias of the repository the service is built from, and
	// CommitSHA the commit: what a deployment fetches. A new commit is a
	// changed configuration, which is what deploying new code means.
	Repo      string              `json:"repo,omitempty"`
	CommitSHA string              `json:"commit_sha,omitempty"`
	Ecosystem discovery.Ecosystem `json:"ecosystem"`
	Framework string              `json:"framework,omitempty"`
	// Image is the Dockerfile that builds it, if there is one.
	Image string `json:"image,omitempty"`
	// Context is the directory Image builds from, when it isn't the
	// Dockerfile's own. ContextDir gives the one to use.
	Context string          `json:"context,omitempty"`
	Build   string          `json:"build,omitempty"`
	Start   string          `json:"start,omitempty"`
	Port    int             `json:"port,omitempty"`
	Scale   *manifest.Scale `json:"scale,omitempty"`
	Size    *manifest.Size  `json:"size,omitempty"`
	// Public says whether it gets an address on the internet.
	Public bool `json:"public"`
	// Internal gives a private service an address on its environment's
	// internal load balancer, for services that reach it over a link.
	Internal bool `json:"internal,omitempty"`
	// PrivateEgress sends the service's traffic to private addresses into
	// its cloud's network, and over a link to the other cloud.
	PrivateEgress bool `json:"private_egress,omitempty"`
	// Env are the variables Loomplane sets so the service finds what it
	// needs. The values exist only once those things are deployed.
	Env []EnvLink `json:"env,omitempty"`
	// Gateway are a gateway's routes. A service with routes is a gateway
	// Loomplane generates and builds, not code from the repository.
	Gateway []gateway.Route `json:"gateway,omitempty"`
	// Component is the catalog component the service runs, when a
	// composed catalog kind runs one as a service.
	Component *ComponentConfig `json:"component,omitempty"`
}

// BuildTarget pins the builder, registry and image reviewed in a plan.
type BuildTarget struct {
	Builder          string            `json:"builder,omitempty"`
	Registry         string            `json:"registry,omitempty"`
	Image            string            `json:"image,omitempty"`
	BuilderSettings  map[string]string `json:"builder_settings,omitempty"`
	RegistrySettings map[string]string `json:"registry_settings,omitempty"`
}

// ContextDir is the directory the service's Dockerfile builds from.
func (c *ServiceConfig) ContextDir() string {
	if c.Context != "" {
		return c.Context
	}
	return c.ownDir()
}

// ownDir is the Dockerfile's own directory.
func (c *ServiceConfig) ownDir() string { return path.Dir(c.Image) }

// DockerfileIn is the service's Dockerfile as a path from dir, its build
// context, which is how docker build's -f reads it.
func (c *ServiceConfig) DockerfileIn(dir string) string {
	if dir == "." {
		return c.Image
	}
	if rest, ok := strings.CutPrefix(c.Image, dir+"/"); ok {
		return rest
	}
	// A Dockerfile outside its context: climb out of the context first.
	return strings.Repeat("../", strings.Count(dir, "/")+1) + c.Image
}

// EnvLink is a variable a service gets with the address of something it
// needs.
type EnvLink struct {
	Key string `json:"key"`
	// From is the manifest name of the resource or service.
	From    string  `json:"from"`
	Subject Subject `json:"subject"`
	// Address is the private address the service reaches it at, when
	// they share a network: http://name:port through ECS Service Connect.
	// Empty means its public URL, known once it's deployed.
	Address string `json:"address,omitempty"`
	// Field is what of a composed catalog resource the variable holds:
	// "url" for its primary output, "output:<name>" for another output,
	// "consumer:<name>" for one issued to this service alone. It's empty
	// for everything else, which gives its address.
	Field string `json:"field,omitempty"`
	// Options are what the service chose for a "consumer:" link, from
	// services.<name>.need_options.<need>. No other link has them.
	Options map[string]string `json:"options,omitempty"`
}

// ResourceConfig is everything a resource is provisioned from.
type ResourceConfig struct {
	Target         string                 `json:"target,omitempty"`
	TargetSettings map[string]string      `json:"target_settings,omitempty"`
	Catalog        *recipes.Recipe        `json:"catalog,omitempty"`
	Managed        *ManagedDatabaseConfig `json:"managed,omitempty"`
	Provider       string                 `json:"provider"`
	Region         string                 `json:"region,omitempty"`
	Kind           string                 `json:"kind"`
	Version        string                 `json:"version,omitempty"`
	// Composition is set on the action for a composed catalog resource
	// itself, and Component on each of its components' actions.
	Composition *CompositionConfig `json:"composition,omitempty"`
	Component   *ComponentConfig   `json:"component,omitempty"`
	// RoutablePorts are the container ports of a process component that
	// may get a public HTTP route. Control ports are never listed, and
	// nothing else has any.
	RoutablePorts []int `json:"routable_ports,omitempty"`
	// CloudPlacement is the environment's Cloud target a composed resource
	// and each of its components run on, when its target is one. Nothing
	// else has one.
	CloudPlacement *CloudPlacement `json:"cloud_placement,omitempty"`
}

// ComponentActionName names the action for one part of a composed catalog
// resource: one of its components, or a dependency provisioned for it.
func ComponentActionName(resource, part string) string { return resource + "--" + part }

// Where a composed catalog resource gets one of its dependencies.
const (
	// DependencyNew provisions one of its own, named by
	// ComponentActionName.
	DependencyNew = "new"
	// DependencyResource connects another resource in the manifest.
	DependencyResource = "resource"
	// DependencyExternal connects an external endpoint.
	DependencyExternal = "external"
	// DependencyOff leaves an optional dependency out.
	DependencyOff = "off"
)

// DependencyChoice is where a composed catalog resource gets one
// dependency.
type DependencyChoice struct {
	Mode string `json:"mode"`
	// Resource is the action name: ComponentActionName(parent, dependency)
	// for a new one, the manifest name for a connected resource.
	Resource string `json:"resource,omitempty"`
	// External is the endpoint's ID.
	External string `json:"external,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// CompositionConfig is the composed catalog resource itself: the release
// its components run, where its dependencies come from, and what it gives
// the services that need it.
type CompositionConfig struct {
	Kind         string                      `json:"kind"`
	Version      string                      `json:"version"`
	Revision     string                      `json:"revision"`
	Dependencies map[string]DependencyChoice `json:"dependencies,omitempty"`
	Outputs      []recipes.Output            `json:"outputs,omitempty"`
	Secrets      []recipes.GeneratedSecret   `json:"secrets,omitempty"`
}

// ComponentConfig is one component of a composed catalog resource, pinned
// to its release's image for the target's architecture.
type ComponentConfig struct {
	Kind string `json:"kind"`
	// Resource is the composed resource's manifest name.
	Resource     string                      `json:"resource"`
	Revision     string                      `json:"revision"`
	Component    recipes.ResolvedComponent   `json:"component"`
	Dependencies map[string]DependencyChoice `json:"dependencies,omitempty"`
	Protection   recipes.Protection          `json:"protection"`
}

// Change is one setting an update changes.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Finding is a problem, which blocks a plan, or a warning, which doesn't.
type Finding struct {
	// Code says what kind of finding it is, for programs: the dashboard
	// offers to confirm or ignore an "unconfirmed-resource".
	Code    string  `json:"code"`
	Subject Subject `json:"subject,omitempty"`
	Name    string  `json:"name,omitempty"`
	// Path is where in the manifest to fix it, such as services.api.provider.
	Path     string               `json:"path,omitempty"`
	Message  string               `json:"message"`
	Evidence []discovery.Evidence `json:"evidence,omitempty"`
}

// Summary counts a plan's actions.
type Summary struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Keep    int `json:"keep"`
}

// Result is what the planner decides.
type Result struct {
	CloudSyncID string    `json:"cloud_sync_id,omitempty"`
	Actions     []Action  `json:"actions"`
	Problems    []Finding `json:"problems"`
	Warnings    []Finding `json:"warnings"`
	Summary     Summary   `json:"summary"`
	// Executable says whether Loomplane could carry the plan out: no
	// problems, and an adapter for every provider it uses.
	Executable bool `json:"executable"`
	// Connections are the cloud connections the plan deploys with, by
	// cloud.
	Connections map[string]string `json:"connections"`
}

// PlanSource pins one repository of a plan to the analysis it was planned
// from, copying what it needs to recompose the graph exactly later.
type PlanSource struct {
	SourceID   string `json:"source_id"`
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root"`
	AnalysisID string `json:"analysis_id"`
	CommitSHA  string `json:"commit_sha"`
	// Ref is the branch the project reads this repository from, "" for
	// its default branch. Targets that build from a branch, not a commit,
	// such as DigitalOcean App Platform, need it.
	Ref string `json:"ref,omitempty"`
	// InScope says whether the plan moves this repository. One it holds
	// stays at what's deployed.
	InScope bool `json:"in_scope"`
}

// DeployedSource is the commit of one repository deployed in one
// environment. Only a successful deployment writes it, and only for the
// repositories its plan moved.
type DeployedSource struct {
	EnvironmentID string    `json:"environment_id"`
	SourceID      string    `json:"source_id"`
	AnalysisID    string    `json:"analysis_id"`
	CommitSHA     string    `json:"commit_sha"`
	PlanID        string    `json:"plan_id"`
	DeployedAt    time.Time `json:"deployed_at"`
}

// DeployedRepository is what's deployed of one of a project's live
// repositories in an environment, named for people: a scope picker locks
// on every repository without one, since a plan can't hold it.
type DeployedRepository struct {
	SourceID   string    `json:"source_id"`
	Alias      string    `json:"alias"`
	Repository string    `json:"repository"`
	CommitSHA  string    `json:"commit_sha"`
	PlanID     string    `json:"plan_id"`
	DeployedAt time.Time `json:"deployed_at"`
}

// Plan is one plan for one environment of a project, pinned to one commit
// of each repository and the manifest revision it was made from.
type Plan struct {
	CloudSyncID   string `json:"cloud_sync_id,omitempty"`
	Catalog       bool   `json:"catalog"`
	ID            string `json:"id" forge:"id"`
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	Status        string `json:"status"`
	// Revision is a digest of everything the plan was made from and
	// everything it would do. Approving names it.
	Revision         string       `json:"revision"`
	Sources          []PlanSource `json:"sources"`
	ManifestID       string       `json:"manifest_id"`
	ManifestRevision int          `json:"manifest_revision"`
	Actions          []Action     `json:"actions"`
	Problems         []Finding    `json:"problems"`
	Warnings         []Finding    `json:"warnings"`
	Summary          Summary      `json:"summary"`
	Executable       bool         `json:"executable"`
	// Connections are the cloud connections the plan deploys with, by
	// cloud, settled when it was planned.
	Connections map[string]string `json:"connections"`
	// Error says why planning failed, when it did.
	Error     string     `json:"error"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	PlannedAt *time.Time `json:"planned_at"`
	// ExpiresAt is when a ready plan stops waiting for a decision.
	ExpiresAt    *time.Time `json:"expires_at"`
	DecidedBy    string     `json:"decided_by"`
	DecidedAt    *time.Time `json:"decided_at"`
	DecisionNote string     `json:"decision_note"`
}

// Open reports whether the plan may still be decided or is still planning.
func (p *Plan) Open() bool { return p.Status == StatusPlanning || p.Status == StatusReady }

func (p *Plan) setResult(r *Result) {
	p.CloudSyncID = r.CloudSyncID
	p.Actions, p.Problems, p.Warnings = r.Actions, r.Problems, r.Warnings
	p.Summary, p.Executable, p.Connections = r.Summary, r.Executable, r.Connections
}

func (p *Plan) normalize() {
	p.CreatedAt = p.CreatedAt.UTC()
	for _, t := range []**time.Time{&p.PlannedAt, &p.ExpiresAt, &p.DecidedAt} {
		if *t != nil {
			u := (*t).UTC()
			*t = &u
		}
	}
	if p.Sources == nil {
		p.Sources = []PlanSource{}
	}
	if p.Actions == nil {
		p.Actions = []Action{}
	}
	if p.Problems == nil {
		p.Problems = []Finding{}
	}
	if p.Warnings == nil {
		p.Warnings = []Finding{}
	}
	if p.Connections == nil {
		p.Connections = map[string]string{}
	}
}

// Binding records what runs one service or resource of a project in one
// environment: the map from Loomplane's names to the Ctrl Plane instance
// or provider resource. Deployments write bindings; planning reads them to
// know what exists.
type Binding struct {
	ID            string          `json:"id" forge:"id"`
	OrgID         string          `json:"org_id"`
	ProjectID     string          `json:"project_id"`
	EnvironmentID string          `json:"environment_id"`
	Subject       Subject         `json:"subject"`
	Name          string          `json:"name"`
	Provider      string          `json:"provider"`
	Region        string          `json:"region"`
	Engine        provider.Engine `json:"engine"`
	// ExternalID is the Ctrl Plane instance's ID, or the provider's ID for
	// a resource Loomplane manages itself.
	ExternalID string `json:"external_id"`
	// PhysicalID identifies the immutable provider incarnation when the adapter
	// can verify it. ExternalID may be a reusable native name.
	PhysicalID string `json:"physical_id,omitempty"`
	// Config is the ServiceConfig or ResourceConfig last applied.
	Config json.RawMessage `json:"config"`
	// URL is where a deployed service answers, which the services that
	// need it are given.
	URL string `json:"url"`
	// ConnectionID is the cloud connection that deployed it, which drift
	// checks read it back with.
	ConnectionID string `json:"connection_id"`
	// Revision is what's live: a container image, or a Vercel deployment.
	Revision string `json:"revision"`
	// CommitSHA is the commit a service was built from.
	CommitSHA string `json:"commit_sha"`
	// Drift is what the last check found: empty when it matched, or
	// DriftMissing or DriftChanged.
	Drift        string     `json:"drift"`
	DriftMessage string     `json:"drift_message"`
	ObservedAt   *time.Time `json:"observed_at"`
	// PlanID is the plan whose deployment last wrote the binding.
	PlanID    string    `json:"plan_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Replaced is what the action ran as before it moved and still needs
	// removing, each with the provider, region and connection that reach
	// it. The deployment that moved it records it with the new binding,
	// so it's tracked whatever happens to that deployment, and any later
	// one that deploys the action removes it. Plans warn about it until
	// then. The API doesn't show it.
	Replaced []Binding `json:"-"`
}

// Drift a check can find.
const (
	// DriftMissing is something deleted outside Loomplane.
	DriftMissing = "missing"
	// DriftChanged is something changed outside Loomplane.
	DriftChanged = "changed"
)

// Store persists plans and bindings, scoped to an organization: another
// organization's read as not found.
type Store interface {
	// CreatePlan inserts p and supersedes every other open plan for its
	// environment, returning their IDs.
	CreatePlan(ctx context.Context, p *Plan) (superseded []string, err error)
	GetPlan(ctx context.Context, orgID, id string) (*Plan, error)
	// ListPlans returns up to limit of a project's plans older than before,
	// newest first, in one environment or, with environmentID empty, in all
	// of them. The zero cursor starts at the newest.
	ListPlans(ctx context.Context, orgID, projectID, environmentID string, before page.Cursor, limit int) ([]*Plan, error)
	// FinishPlanning stores a planning result. It returns false, with no
	// error, when the plan isn't planning any more.
	FinishPlanning(ctx context.Context, p *Plan) (bool, error)
	// FailPlanning finishes a planning plan as failed.
	FailPlanning(ctx context.Context, orgID, id, reason string, now time.Time) error
	// Decide moves a ready plan to status. It returns false, with no error,
	// when the plan isn't ready any more.
	Decide(ctx context.Context, orgID, id, status, by, note string, now time.Time) (bool, error)

	// EnvironmentConnections returns the environment's choice of
	// connection, by cloud.
	EnvironmentConnections(ctx context.Context, orgID, environmentID string) (map[string]string, error)
	// SetEnvironmentConnection chooses the connection an environment
	// deploys to a cloud with. An empty connectionID clears the choice.
	SetEnvironmentConnection(ctx context.Context, orgID, projectID, environmentID, cloud, connectionID string) error

	ListBindings(ctx context.Context, orgID, environmentID string) ([]*Binding, error)
	// AllBindings pages through every organization's bindings, in ID
	// order after the given ID, for the drift check.
	AllBindings(ctx context.Context, after string, limit int) ([]*Binding, error)
	// RecordDrift stores what a drift check found.
	RecordDrift(ctx context.Context, orgID, id, drift, message string, at time.Time) error
	PutBinding(ctx context.Context, b *Binding) error
	DeleteBinding(ctx context.Context, orgID, id string) error
	// SetReplaced records what a binding's action ran as before it moved
	// and still needs removing, replacing what was recorded.
	SetReplaced(ctx context.Context, orgID, id string, replaced []Binding) error

	// DeployedSources returns the commit of each repository deployed in an
	// environment.
	DeployedSources(ctx context.Context, orgID, environmentID string) ([]*DeployedSource, error)
	// RecordDeployed records d as what's deployed of its repository in
	// its environment, replacing what was.
	RecordDeployed(ctx context.Context, orgID string, d *DeployedSource) error
}

// Digest returns a hex SHA-256 of v's JSON. Go's JSON is deterministic for
// structs and sorts map keys, so equal values give equal digests.
func Digest(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("digest: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
