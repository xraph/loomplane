package planning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// StatusStale is a plan whose approval came after what's deployed changed:
// carrying it out would no longer do what was reviewed.
const StatusStale = "stale"

const (
	// analysisLookback is how many of a project's latest analyses are
	// searched for one that succeeded.
	analysisLookback = 20
	// settleWait is how long a request waits for the workflow to settle a
	// plan, so the answer usually shows the result instead of "planning".
	settleWait = 3 * time.Second
	settlePoll = 100 * time.Millisecond
	maxNote    = 2000
)

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Projects looks up projects and their environments.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
}

// Analyses reads a project's sources, their analyses and the graphs those
// found. discovery.PgStore is the real one.
type Analyses = discovery.Graphs

// CommitAnalyzer finds or queues an analysis of one exact commit of a
// source, for a promotion from an environment whose release of that
// commit wasn't recorded. *discovery.CommitAnalyzer is the real one.
type CommitAnalyzer interface {
	// AnalyzeCommit returns a succeeded or in-flight analysis of exactly
	// commitSHA, or queues one pinned to it.
	AnalyzeCommit(ctx context.Context, orgID string, src *discovery.Source, commitSHA, createdBy string) (*discovery.Analysis, error)
	Analysis(ctx context.Context, orgID, id string) (*discovery.Analysis, error)
}

// Manifests reads manifest revisions.
type Manifests interface {
	Latest(ctx context.Context, orgID, projectID string) (*manifests.Revision, error)
	Get(ctx context.Context, orgID, id string) (*manifests.Revision, error)
}

// Connections lists an organization's cloud connections.
// *connections.Service is the real one.
type Connections interface {
	All(ctx context.Context, orgID string) ([]*connections.Connection, error)
}

// Workflow runs each plan's deployment workflow. *orchestration.Deployments
// is the real one.
type Workflow interface {
	Start(ctx context.Context, orgID, planID string, window time.Duration) error
	Decide(ctx context.Context, planID, requestID string, d orchestration.Decision) error
	Cancel(ctx context.Context, planID, reason string) error
}

// EndpointChecker checks the external endpoints composed resources
// connect. *catalogconnect.Checker is the real one.
type EndpointChecker interface {
	// Endpoint returns an endpoint without its DSN, or
	// catalogconnect.ErrNotFound.
	Endpoint(ctx context.Context, orgID, id string) (*catalogconnect.Endpoint, error)
	Check(ctx context.Context, orgID, endpointID string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error)
}

// TargetDialer opens connections from a deployment target's network: over
// a server's SSH connection, or from Loomplane for a cluster. Closing the
// io.Closer ends what the dial goes through.
type TargetDialer interface {
	TargetDial(ctx context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error)
}

// Service is the plan API, and the planner the deployment workflow calls.
// API methods check the caller's role; workflow methods trust the plan,
// which a checked call created.
type Service struct {
	capacityTemplates CapacityTemplateValidator
	capacity          CapacityProvisioner
	capacityPrincipal CapacityPrincipalVerifier
	cloudSyncRunner   CloudSyncDeployer
	appliedInputs     AppliedInputReader
	cloudSyncWorkflow CloudSyncWorkflow
	cloudProfiles     cloudprofiles.Resolver
	store             Store
	projects          Projects
	analyses          Analyses
	commits           CommitAnalyzer
	history           ReleaseHistory
	manifests         Manifests
	providers         *provider.Registry
	clouds            Connections
	authz             Authorizer
	workflow          Workflow
	audit             projects.Auditor
	window            time.Duration
	now               func() time.Time
	sleep             func(ctx context.Context, d time.Duration) error
	warn              func(msg string, err error)
	telemetry         *telemetry.Recorder
	dbTrust           func() ([]plane.TrustKey, error)
	kinds             *recipes.Registry
	endpoints         EndpointChecker
	dialer            TargetDialer
}

// Deps are what a Service needs.
type Deps struct {
	CapacityTemplates CapacityTemplateValidator
	Capacity          CapacityProvisioner
	CapacityPrincipal CapacityPrincipalVerifier
	AppliedInputs     AppliedInputReader
	CloudSyncWorkflow CloudSyncWorkflow
	CloudProfiles     cloudprofiles.Resolver
	Store             Store
	Projects          Projects
	Analyses          Analyses
	// Commits analyzes a commit a promotion's source environment runs
	// that nothing analyzed. Nil refuses such a promotion with
	// ErrNoAnalysis.
	Commits CommitAnalyzer
	// History reads what an environment's releases ran, so a promotion
	// deploys the From environment's images where it can. Nil disables
	// image reuse: every service builds again.
	History   ReleaseHistory
	Manifests Manifests
	Providers *provider.Registry
	// Connections are the organization's cloud accounts. Nil means none,
	// so no plan that needs one is executable.
	Connections Connections
	Authz       Authorizer
	Workflow    Workflow
	// Audit may be nil.
	Audit projects.Auditor
	// Window is how long a ready plan waits for a decision.
	Window time.Duration
	// Warn logs a failure that doesn't fail the request. It may be nil.
	Warn func(msg string, err error)
	// Telemetry counts plans and decisions. It may be nil.
	Telemetry *telemetry.Recorder
	// DatabaseGatewayTrust returns the keys a database gateway deployed
	// now would trust, which a plan records so a change to them redeploys
	// the gateway. Nil, or an error, records none, and the deploy says why.
	DatabaseGatewayTrust func() ([]plane.TrustKey, error)
	// Kinds are the catalog's kinds. Nil means recipes.Default().
	Kinds *recipes.Registry
	// Endpoints checks the external endpoints composed resources connect,
	// through Dialer. Without either, no external endpoint can be used.
	Endpoints EndpointChecker
	Dialer    TargetDialer
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if st, ok := d.Store.(*PgStore); ok {
		if wake, available := d.Capacity.(cloudinfra.CapacityCloudWakeup); available {
			st.frontendWake = wake
		}
	}
	warn := d.Warn
	if warn == nil {
		warn = func(string, error) {}
	}
	return &Service{
		capacityTemplates: d.CapacityTemplates, capacity: d.Capacity, capacityPrincipal: d.CapacityPrincipal,
		appliedInputs: d.AppliedInputs, cloudSyncWorkflow: d.CloudSyncWorkflow,
		cloudProfiles: d.CloudProfiles, store: d.Store, projects: d.Projects, analyses: d.Analyses, commits: d.Commits, history: d.History, manifests: d.Manifests,
		providers: d.Providers, clouds: d.Connections, authz: d.Authz, workflow: d.Workflow, audit: d.Audit,
		window: d.Window, now: time.Now, sleep: waitFor, warn: warn, telemetry: d.Telemetry, dbTrust: d.DatabaseGatewayTrust,
		kinds: d.Kinds, endpoints: d.Endpoints, dialer: d.Dialer,
	}
}

var _ orchestration.Planner = (*Service)(nil)

// CreatePlanInput asks for a plan of one environment.
type CreatePlanInput struct {
	// Catalog explicitly plans standalone resources without a repository lockset.
	Catalog bool `json:"catalog,omitempty"`
	// Environment is the environment's name.
	Environment string `json:"environment"`
	// Scope names the repositories the plan moves. Empty means all of
	// them; the rest stay at what's deployed.
	Scope []string `json:"scope,omitempty"`
	// From is the environment to promote from. Each in-scope source is
	// pinned to the commit (and analysis) that environment's last
	// successful deployment of it recorded, and the plan uses the manifest
	// revision that environment last deployed with; the rest are held.
	From string `json:"from,omitempty"`
}

// DecideInput approves or rejects a plan.
type DecideInput struct {
	// Revision is the plan revision you reviewed. Approving needs it, so
	// what gets approved is what you saw.
	Revision string `json:"revision,omitempty"`
	Note     string `json:"note,omitempty"`
}

// refuseAutomation keeps Loomplane automation off the paths people take.
// Its role lets it approve a deployment, so the code, not only Warden,
// limits it to the automatic rollback.
func refuseAutomation(p security.Principal) error {
	if p.IsSystem() {
		return fmt.Errorf("%w: %s only rolls back automatically", security.ErrForbidden, security.SystemActorName)
	}
	return nil
}

// Create plans an environment from the project's current manifest, the
// latest analysis of each repository it moves, and what's deployed of each
// it holds. It supersedes the environment's open plan, if any.
func (s *Service) Create(ctx context.Context, p security.Principal, projectID string, in CreatePlanInput) (*Plan, error) {
	if err := refuseAutomation(p); err != nil {
		return nil, err
	}
	if err := s.authz.Require(ctx, p, security.DeploymentCreate, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(in.Environment))
	if err != nil {
		return nil, err
	}
	if from := strings.TrimSpace(in.From); from != "" {
		return s.createFrom(ctx, p, projectID, env, from, in)
	}
	rev, err := s.manifests.Latest(ctx, p.OrgID, projectID)
	if errors.Is(err, manifests.ErrNotFound) {
		return nil, ErrNoManifest
	}
	if err != nil {
		return nil, err
	}
	var pinned []PlanSource
	if in.Catalog {
		if len(in.Scope) > 0 || !catalogOnly(rev.Manifest) {
			return nil, fmt.Errorf("%w: standalone catalog plans contain resources only", ErrInvalid)
		}
	} else {
		pinned, err = s.pin(ctx, p.OrgID, projectID, env, in.Scope)
		if err != nil {
			return nil, err
		}
	}
	return s.start(ctx, p, &Plan{
		OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Environment: env.Name,
		Sources: pinned, Catalog: in.Catalog, ManifestID: rev.ID, ManifestRevision: rev.Revision,
	})
}

// createFrom plans env from what the from environment runs: its commits
// and the manifest revision it last deployed with.
func (s *Service) createFrom(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, from string, in CreatePlanInput) (*Plan, error) {
	switch {
	case in.Catalog:
		return nil, fmt.Errorf("%w: a standalone catalog plan can't promote from another environment", ErrInvalid)
	case from == env.Name:
		return nil, fmt.Errorf("%w: %s can't promote from itself", ErrInvalid, env.Name)
	}
	source, err := s.environment(ctx, p.OrgID, projectID, from)
	if err != nil {
		return nil, err
	}
	pinned, deployed, err := s.pinFrom(ctx, p, projectID, env, source, in.Scope)
	if err != nil {
		return nil, err
	}
	var rev *manifests.Revision
	if deployed != nil {
		if rev, err = s.manifests.Get(ctx, p.OrgID, deployed.ManifestID); err != nil {
			return nil, fmt.Errorf("load manifest revision %d, which %s deployed with: %w", deployed.ManifestRevision, source.Name, err)
		}
		if rev.Manifest == nil {
			return nil, fmt.Errorf("%w: manifest revision %d, which %s deployed with, no longer parses", ErrInvalid, rev.Revision, source.Name)
		}
	} else {
		rev, err = s.manifests.Latest(ctx, p.OrgID, projectID)
		if errors.Is(err, manifests.ErrNotFound) {
			return nil, ErrNoManifest
		}
		if err != nil {
			return nil, err
		}
	}
	return s.start(ctx, p, &Plan{
		OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Environment: env.Name,
		Kind: KindPromotion, From: source.Name, FromEnvironmentID: source.ID,
		Sources: pinned, ManifestID: rev.ID, ManifestRevision: rev.Revision,
	})
}

// start stores a new plan, superseding the environment's open plan, and
// starts planning it.
func (s *Service) start(ctx context.Context, p security.Principal, plan *Plan) (*Plan, error) {
	id, err := typeid.Generate("plan")
	if err != nil {
		return nil, fmt.Errorf("generate plan id: %w", err)
	}
	plan.ID, plan.Status, plan.CreatedBy, plan.CreatedAt = id.String(), StatusPlanning, p.UserID, s.now().UTC()
	plan.normalize()
	superseded, err := s.store.CreatePlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	for _, old := range superseded {
		// A superseded plan is settled in the database already; stopping
		// its workflow just frees it sooner.
		if cerr := s.workflow.Cancel(context.WithoutCancel(ctx), old, "superseded by "+plan.ID); cerr != nil {
			s.warn("stop a superseded plan's workflow", cerr)
		}
	}
	if err = s.workflow.Start(ctx, p.OrgID, plan.ID, s.window); err != nil {
		reason := "Planning couldn't start: " + err.Error()
		if ferr := s.store.FailPlanning(context.WithoutCancel(ctx), p.OrgID, plan.ID, reason, s.now().UTC()); ferr != nil {
			err = errors.Join(err, ferr)
		}
		return nil, fmt.Errorf("start planning: %w", err)
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "plan.create", Severity: "info", ProjectID: plan.ProjectID, Principal: p,
			Attributes: map[string]any{
				"plan_id": plan.ID, "environment": plan.Environment, "sources": lockDescription(plan.Sources),
				"manifest_revision": plan.ManifestRevision, "superseded": superseded,
				"kind": plan.Kind, "from": plan.From,
			},
		})
	}
	return s.settle(ctx, plan), nil
}

func (s *Service) environment(ctx context.Context, orgID, projectID, name string) (*projects.Environment, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: say which environment to plan, such as production", ErrInvalid)
	}
	envs, err := s.projects.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: this project has no environment called %q", projects.ErrEnvironmentNotFound, name)
}

// pin builds a plan's lockset from what's analyzed and what's deployed.
func (s *Service) pin(ctx context.Context, orgID, projectID string, env *projects.Environment, scope []string) ([]PlanSource, error) {
	sources, err := s.analyses.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, err
	}
	latest := map[string]*discovery.Analysis{}
	for _, src := range sources {
		list, lerr := s.analyses.ListSourceAnalyses(ctx, orgID, src.ID, analysisLookback)
		if lerr != nil {
			return nil, lerr
		}
		for _, a := range list {
			// A pinned analysis is of an older commit a promotion
			// deployed, not the branch's latest.
			if a.Status == discovery.StatusSucceeded && !a.Pinned {
				latest[src.ID] = a
				break
			}
		}
	}
	rows, err := s.store.DeployedSources(ctx, orgID, env.ID)
	if err != nil {
		return nil, err
	}
	deployed := map[string]*DeployedSource{}
	for _, d := range rows {
		deployed[d.SourceID] = d
	}
	return lockset(sources, latest, deployed, scope, env.Name)
}

// lockDescription is a lockset for the audit trail: "api@3f2c1a9 moves".
func lockDescription(ps []PlanSource) []string {
	out := make([]string, 0, len(ps))
	for _, s := range ps {
		state := "held"
		if s.InScope {
			state = "moves"
		}
		out = append(out, s.Alias+"@"+s.CommitSHA+" "+state)
	}
	return out
}

// settle waits a moment for the workflow to move a plan on, and returns it
// as it is then. It never fails: the plan as it was is a fine answer. It
// polls at most settleWait/settlePoll times as well as until the clock
// passes settleWait, so a test that stops the clock still returns.
func (s *Service) settle(ctx context.Context, plan *Plan) *Plan {
	status := plan.Status
	deadline := s.now().Add(settleWait)
	for polls := 0; polls < int(settleWait/settlePoll) && s.now().Before(deadline); polls++ {
		if s.sleep(ctx, settlePoll) != nil {
			return plan
		}
		got, err := s.store.GetPlan(ctx, plan.OrgID, plan.ID)
		if err != nil {
			return plan
		}
		if got.Status != status {
			return got
		}
	}
	return plan
}

// List returns a page of a project's plans older than before, newest
// first, in one environment or, with environment empty, in all of them,
// and where the next page starts: the zero cursor when this is the last.
func (s *Service) List(ctx context.Context, p security.Principal, projectID, environment string, before page.Cursor, limit int) ([]*Plan, page.Cursor, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, page.Cursor{}, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, page.Cursor{}, err
	}
	envID := ""
	if environment = strings.TrimSpace(environment); environment != "" {
		env, err := s.environment(ctx, p.OrgID, projectID, environment)
		if err != nil {
			return nil, page.Cursor{}, err
		}
		envID = env.ID
	}
	// One more than a page says whether there's another.
	plans, err := s.store.ListPlans(ctx, p.OrgID, projectID, envID, before, limit+1)
	if err != nil {
		return nil, page.Cursor{}, err
	}
	plans, next := page.Trim(plans, limit, func(pl *Plan) page.Cursor { return page.Cursor{At: pl.CreatedAt, ID: pl.ID} })
	return plans, next, nil
}

// Get returns one plan.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Plan, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.store.GetPlan(ctx, p.OrgID, id)
}

// Approve approves a ready plan, as of the revision the caller reviewed.
func (s *Service) Approve(ctx context.Context, p security.Principal, id string, in DecideInput) (*Plan, error) {
	return s.decide(ctx, p, id, true, in)
}

// Reject rejects a ready plan.
func (s *Service) Reject(ctx context.Context, p security.Principal, id string, in DecideInput) (*Plan, error) {
	return s.decide(ctx, p, id, false, in)
}

func (s *Service) decide(ctx context.Context, p security.Principal, id string, approve bool, in DecideInput) (*Plan, error) {
	if err := refuseAutomation(p); err != nil {
		return nil, err
	}
	if err := s.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	plan, err := s.store.GetPlan(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	switch {
	case plan.Status == StatusSuperseded:
		return nil, ErrSuperseded
	case plan.Status != StatusReady:
		return nil, ErrNotReady
	case approve && strings.TrimSpace(in.Revision) == "":
		return nil, fmt.Errorf("%w: approving needs the revision you reviewed", ErrInvalid)
	case approve && in.Revision != plan.Revision:
		return nil, ErrStale
	case len(in.Note) > maxNote:
		return nil, fmt.Errorf("%w: the note can be at most %d characters", ErrInvalid, maxNote)
	}
	if approve {
		if checkErr := RequireBackupPolicyPermission(ctx, s.authz, p, plan.ProjectID, plan.Actions); checkErr != nil {
			return nil, checkErr
		}
	}
	reqID, err := typeid.Generate("dec")
	if err != nil {
		return nil, fmt.Errorf("generate decision id: %w", err)
	}
	dec := orchestration.Decision{Actor: &p, Approve: approve, Revision: in.Revision, By: p.UserID, Note: strings.TrimSpace(in.Note)}
	if !approve {
		dec.Revision = plan.Revision
	}
	err = s.workflow.Decide(ctx, plan.ID, reqID.String(), dec)
	if errors.Is(err, orchestration.ErrNotRunning) {
		return nil, ErrNotReady
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if s.audit != nil {
		action := "plan.reject"
		if approve {
			action = "plan.approve"
		}
		s.audit.Record(ctx, projects.AuditEvent{
			Action: action, Severity: "warning", ProjectID: plan.ProjectID, Principal: p,
			Attributes: map[string]any{"plan_id": plan.ID, "environment": plan.Environment, "revision": plan.Revision},
		})
	}
	return s.settle(ctx, plan), nil
}

// Bindings returns what runs each service and resource of a project in an
// environment.
func (s *Service) Bindings(ctx context.Context, p security.Principal, projectID, environment string) ([]*Binding, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(environment))
	if err != nil {
		return nil, err
	}
	return s.store.ListBindings(ctx, p.OrgID, env.ID)
}

// Deployed returns what's deployed of each of a project's live
// repositories in an environment, by alias. A repository nothing has
// deployed there yet isn't listed, and neither is a removed one, which no
// plan can name.
func (s *Service) Deployed(ctx context.Context, p security.Principal, projectID, environment string) ([]*DeployedRepository, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(environment))
	if err != nil {
		return nil, err
	}
	sources, err := s.analyses.ListSources(ctx, p.OrgID, projectID, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.DeployedSources(ctx, p.OrgID, env.ID)
	if err != nil {
		return nil, err
	}
	deployed := map[string]*DeployedSource{}
	for _, d := range rows {
		deployed[d.SourceID] = d
	}
	out := []*DeployedRepository{}
	for _, src := range sources {
		if d := deployed[src.ID]; d != nil {
			out = append(out, &DeployedRepository{
				SourceID: src.ID, Alias: src.Alias, Repository: src.Repository,
				CommitSHA: d.CommitSHA, PlanID: d.PlanID, DeployedAt: d.DeployedAt,
			})
		}
	}
	return out, nil
}

// Providers returns the deployment targets a manifest can name.
func (s *Service) Providers() []*provider.Provider { return s.providers.List() }

// revisionInput is everything a plan's revision covers: what it was made
// from, what's deployed, and what it would do.
type revisionInput struct {
	EnvironmentID string        `json:"environment_id"`
	Environment   string        `json:"environment"`
	Sources       []PlanSource  `json:"sources"`
	Manifest      string        `json:"manifest"`
	Deployed      []deployedRef `json:"deployed"`
	Result        *Result       `json:"result"`
	// From and Rollback are left out when empty, so a release's revision
	// is what it was before promotions and rollbacks.
	From     string    `json:"from,omitempty"`
	Rollback *Rollback `json:"rollback,omitempty"`
}

type deployedRef struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Region     string `json:"region"`
	ExternalID string `json:"external_id"`
	Config     string `json:"config"`
	CommitSHA  string `json:"commit_sha"`
	Revision   string `json:"revision"`
	Drift      string `json:"drift"`
}

// compute plans a plan from its pinned commits and manifest, against what's
// deployed now, and returns the result with its revision.
func (s *Service) compute(ctx context.Context, plan *Plan) (*Result, string, error) {
	// An older plan the backfill couldn't pin would plan from an empty
	// graph, so it fails instead of proposing to tear down what's deployed.
	if len(plan.Sources) == 0 && !plan.Catalog {
		return nil, "", fmt.Errorf("%w: this plan pins no repositories; plan again", ErrInvalid)
	}
	parts := make([]graph.Part, 0, len(plan.Sources))
	held := map[string]bool{}
	for _, ps := range plan.Sources {
		g, err := s.analyses.Graph(ctx, plan.OrgID, ps.AnalysisID)
		if err != nil {
			return nil, "", fmt.Errorf("load the analysis of %s: %w", ps.Alias, err)
		}
		parts = append(parts, graph.Part{
			Source: graph.SourceRef{Alias: ps.Alias, Repository: ps.Repository, Root: ps.Root, CommitSHA: ps.CommitSHA, AnalysisID: ps.AnalysisID},
			Graph:  g,
		})
		if !ps.InScope {
			held[ps.Alias] = true
		}
	}
	g := graph.Compose(parts)
	rev, err := s.manifests.Get(ctx, plan.OrgID, plan.ManifestID)
	if err != nil {
		return nil, "", fmt.Errorf("load the manifest: %w", err)
	}
	if rev.Manifest == nil {
		return nil, "", fmt.Errorf("%w: manifest revision %d no longer parses", ErrInvalid, rev.Revision)
	}
	bindings, err := s.store.ListBindings(ctx, plan.OrgID, plan.EnvironmentID)
	if err != nil {
		return nil, "", err
	}
	if plan.Catalog {
		if !catalogOnly(rev.Manifest) || len(plan.Sources) > 0 {
			return nil, "", fmt.Errorf("%w: standalone catalog plan contains repository content", ErrInvalid)
		}
		for _, b := range bindings {
			if b.Subject != ResourceSubject {
				return nil, "", fmt.Errorf("%w: standalone catalog plan cannot change an environment with source services", ErrInvalid)
			}
		}
	}
	options, err := s.connectionOptions(ctx, plan.OrgID)
	if err != nil {
		return nil, "", err
	}
	chosen, err := s.store.EnvironmentConnections(ctx, plan.OrgID, plan.EnvironmentID)
	if err != nil {
		return nil, "", err
	}
	federation, err := s.federationInput(ctx, plan, rev.Manifest)
	if err != nil {
		return nil, "", err
	}
	fabriqPostgres, err := s.fabriqPostgres(ctx, plan, rev.Manifest)
	if err != nil {
		return nil, "", err
	}
	resolutionManifest, needs, resolutionErr := cloudResolutionManifest(rev.Manifest, Input{Environment: plan.Environment, Graph: g, Bindings: bindings, Held: held})
	// A composed resource on one of the Cloud's targets is placed there too.
	composed := composedResources(rev.Manifest, plan.Environment, s.kinds)
	var assignment *cloudprofiles.Assignment
	// composedLookup says only composed resources asked for the Cloud.
	composedLookup := false
	if resolutionErr == nil && (needs.Explicit || needs.Fallback || len(composed) > 0) && s.cloudProfiles != nil {
		composedLookup = !needs.Explicit && !needs.Fallback
		assignment, resolutionErr = s.cloudProfiles.Resolve(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID)
		// A revoked grant only matters to services that ask for the Cloud.
		if !needs.Explicit && errors.Is(resolutionErr, cloudprofiles.ErrNotFound) {
			assignment, resolutionErr = nil, nil
		}
		if resolutionErr == nil && assignment != nil && (assignment.OrgID != plan.OrgID || assignment.ProjectID != plan.ProjectID || assignment.EnvironmentID != plan.EnvironmentID) {
			resolutionErr = fmt.Errorf("cloud assignment is outside this organization, project or environment scope")
		}
	}
	intents := map[string]*CapacityIntent{}
	var placements map[string]CloudPlacement
	var resolved *manifest.Manifest
	if resolutionErr == nil {
		pools, poolErr := s.resolvePools(ctx, plan, resolutionManifest, assignment, bindings, options, needs.Held, intents)
		if poolErr != nil {
			resolutionErr = poolErr
		} else {
			resolved, placements, resolutionErr = resolveCloudPlacements(resolutionManifest, plan.Environment, assignment, options, pools, needs.Held, intents)
		}
	}
	routes, routeErr := s.releaseRoutes(ctx, plan)
	if routeErr != nil {
		return nil, "", routeErr
	}
	var zones []cloudinfra.ProjectZone
	var trust string
	if len(dbGatewayResources(rev.Manifest, plan.Environment)) > 0 {
		if zones, err = s.projectZones(ctx, plan); err != nil {
			return nil, "", err
		}
		trust = s.databaseGatewayTrust()
	}
	var result *Result
	if resolutionErr != nil {
		finding := Finding{Code: "cloud-target", Path: "services", Message: resolutionErr.Error()}
		var placementErr *CloudPlacementError
		switch {
		case errors.As(resolutionErr, &placementErr):
			finding = Finding{Code: placementErr.Code, Subject: ServiceSubject, Name: strings.TrimSuffix(strings.TrimPrefix(placementErr.Path, "services."), ".cloud_tags"), Path: placementErr.Path, Message: placementErr.Message}
			if strings.HasPrefix(placementErr.Path, "database_gateway.") {
				finding.Name = dbGatewayName
			}
		case composedLookup:
			finding = Finding{Code: "cloud-target", Subject: ResourceSubject, Name: composed[0], Path: "resources." + composed[0] + ".target", Message: resolutionErr.Error()}
		}
		result = &Result{Problems: []Finding{finding}, Actions: []Action{}, Connections: map[string]string{}}
	} else {
		placed := map[string]CloudPlacement{}
		maps.Copy(placed, placements)
		maps.Copy(placed, composedPlacements(resolved, plan.Environment, composed, assignment, options))
		result = Compute(Input{
			Catalog: plan.Catalog, Routes: routes, Zones: zones, DatabaseGatewayTrust: trust,
			Federation: federation, CloudPlacements: placed, UnverifiedPlacements: unverifiedPlacements(resolved, plan.Environment, composed, assignment, options), CapacityIntents: intents,
			Environment: plan.Environment, Graph: g, Manifest: resolved, Providers: s.providers, Bindings: bindings,
			Connections: options, Chosen: chosen, Held: held,
			Kinds: s.kinds, Endpoints: s.checkEndpoints(ctx, plan.OrgID, plan.Environment, resolved),
			FabriqPostgres: fabriqPostgres,
		})
	}
	if resolutionErr == nil && poolRelocation(bindings, placements) {
		result = appliedCloudPlan(bindings, assignment, "", options, s.providers, placements, routes)
		planRelease(Input{Environment: plan.Environment, Manifest: resolved, Providers: s.providers}, result)
	}
	// Asked before anything is reserved, so a store that can't tell fails
	// the plan with nothing to undo.
	if rampErr := s.rampingCanary(ctx, plan, result); rampErr != nil {
		return nil, "", rampErr
	}
	if routeErr = s.reserveRoutedPlacements(ctx, plan, result, bindings); routeErr != nil {
		result.Problems = append(result.Problems, Finding{Code: "route-capacity", Message: routeErr.Error()})
		result.Executable = false
	}
	if frontendErr := s.pinFrontendDemands(ctx, plan, result); frontendErr != nil {
		result.Problems = append(result.Problems, Finding{Code: "frontend-governance", Message: frontendErr.Error()})
		result.Executable = false
	}
	s.checkBackupPreview(ctx, plan, result)
	warnings, err := s.promotionWarnings(ctx, plan, rev.Revision)
	if err != nil {
		return nil, "", err
	}
	result.Warnings = append(result.Warnings, warnings...)
	released, reuse, err := s.releasedImages(ctx, plan)
	if err != nil {
		return nil, "", fmt.Errorf("read what %s ran: %w", plan.From, err)
	}
	if reuse {
		s.applyArtifacts(plan, result, released, plan.From)
	}
	in := revisionInput{
		EnvironmentID: plan.EnvironmentID, Environment: plan.Environment, Sources: plan.Sources,
		Manifest: rev.Digest, Deployed: make([]deployedRef, 0, len(bindings)), Result: result,
		From: plan.From, Rollback: plan.Rollback,
	}
	for _, b := range bindings {
		in.Deployed = append(in.Deployed, deployedRef{
			ID: b.ID, Provider: b.Provider, Region: b.Region, ExternalID: b.ExternalID, Config: string(b.Config),
			CommitSHA: b.CommitSHA, Revision: b.Revision, Drift: b.Drift,
		})
	}
	revision, err := Digest(in)
	if err != nil {
		return nil, "", err
	}
	return result, revision, nil
}

// databaseGatewayTrust is plane.TrustDigest of the keys a database gateway
// deployed now would trust, or "" when they can't be had. A plan that
// records "" for a gateway that runs with keys redeploys it, and that
// deploy fails saying what's wrong with the keys.
func (s *Service) databaseGatewayTrust() string {
	if s.dbTrust == nil {
		return ""
	}
	keys, err := s.dbTrust()
	if err != nil {
		return ""
	}
	return plane.TrustDigest(keys)
}

func (s *Service) connectionOptions(ctx context.Context, orgID string) ([]ConnectionOption, error) {
	if s.clouds == nil {
		return nil, nil
	}
	all, err := s.clouds.All(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list cloud connections: %w", err)
	}
	hosts := map[string]ConnectionHost{}
	if reader, ok := s.store.(interface {
		ConnectionHosts(context.Context, string) (map[string]ConnectionHost, error)
	}); ok {
		hosts, err = reader.ConnectionHosts(ctx, orgID)
		if err != nil {
			return nil, err
		}
	}
	out := make([]ConnectionOption, 0, len(all))
	for _, c := range all {
		out = append(out, ConnectionOption{
			Pooled: hosts[c.ID].Pooled, Servers: slices.Clone(hosts[c.ID].Servers), Account: c.Account, ID: c.ID, Name: c.Name, Cloud: c.Cloud, Ready: c.Status == connections.StatusReady,
			PullReady: connections.RegistryPullReady(c),
			HTTPS:     c.Details["certificate_arn"] != "", Region: c.Details["region"],
			Details: maps.Clone(c.Details),
		})
	}
	return out, nil
}

// checkEndpoints checks each external endpoint the manifest's composed
// resources connect, from each resource's target, for Compute to report
// on. An endpoint resources share on one target, against one requirement,
// is checked once. Without a checker and a dialer nothing is checked, so
// no external endpoint can be used.
func (s *Service) checkEndpoints(ctx context.Context, orgID, environment string, m *manifest.Manifest) map[string]EndpointCheck {
	if s.endpoints == nil || s.dialer == nil || m == nil {
		return nil
	}
	kinds := s.kinds
	if kinds == nil {
		kinds = recipes.Default()
	}
	type use struct {
		id, connection string
		req            recipes.Requirement
		consumer       catalogconnect.Consumer
	}
	uses := map[string]use{} // by endpointKey
	for _, name := range slices.Sorted(maps.Keys(m.Resources)) {
		r := m.Resources[name]
		kind, ok := kinds.Kind(r.Kind)
		if !ok || r.Target == "" || !manifest.On(r.For(environment, m.Defaults).Enabled) {
			continue
		}
		for _, dep := range kind.Dependencies {
			id := r.Uses[dep.Name].External
			if id == "" || dep.External == nil {
				continue
			}
			consumer := catalogconnect.ConsumerOf(kind, dep.Name)
			uses[endpointKey(id, r.Target, *dep.External, consumer)] = use{id: id, connection: r.Target, req: *dep.External, consumer: consumer}
		}
	}
	if len(uses) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(uses))
	// Look each endpoint up once, and dial only for the ones that exist.
	found := map[string]EndpointCheck{}
	targets := map[string][]string{}
	for _, key := range keys {
		u := uses[key]
		if _, ok := found[u.id]; !ok {
			e, err := s.endpoints.Endpoint(ctx, orgID, u.id)
			switch {
			case errors.Is(err, catalogconnect.ErrNotFound):
				found[u.id] = EndpointCheck{ID: u.id}
			case err != nil:
				found[u.id] = EndpointCheck{ID: u.id, Err: err.Error()}
			default:
				found[u.id] = EndpointCheck{ID: u.id, Name: e.Name, Engine: e.Engine, Found: true}
			}
		}
		if found[u.id].Found {
			targets[u.connection] = append(targets[u.connection], key)
		}
	}
	checks := map[string]EndpointCheck{}
	for _, connection := range slices.Sorted(maps.Keys(targets)) {
		dial, closer, err := s.dialer.TargetDial(ctx, orgID, connection)
		for _, key := range targets[connection] {
			u := uses[key]
			c := found[u.id]
			if err != nil {
				c.Err = "the target connection failed: " + err.Error()
				checks[key] = c
				continue
			}
			report, checkErr := s.endpoints.Check(catalogconnect.WithConsumer(ctx, u.consumer), orgID, u.id, u.req, dial)
			switch {
			case errors.Is(checkErr, catalogconnect.ErrNotFound):
				c = EndpointCheck{ID: u.id}
			case checkErr != nil:
				c.Err = checkErr.Error()
			default:
				c.Report = report
			}
			checks[key] = c
		}
		if closer != nil {
			if closeErr := closer.Close(); closeErr != nil {
				s.warn("close the target connection an endpoint check used", closeErr)
			}
		}
	}
	// An endpoint checked once is under its ID, and one checked from more
	// than one target, or for more than one requirement, under each key.
	times := map[string]int{}
	for _, u := range uses {
		times[u.id]++
	}
	out := make(map[string]EndpointCheck, len(uses))
	for _, key := range keys {
		u := uses[key]
		c, ok := checks[key]
		if !ok {
			c = found[u.id]
		}
		if times[u.id] == 1 {
			key = u.id
		}
		out[key] = c
	}
	return out
}

// permanent reports whether planning can never succeed, so retrying is
// pointless: what it was made from is gone or unreadable.
func permanent(err error) bool {
	return errors.Is(err, discovery.ErrNotFound) || errors.Is(err, discovery.ErrNoGraph) ||
		errors.Is(err, manifests.ErrNotFound) || errors.Is(err, ErrInvalid)
}

// Plan implements orchestration.Planner: it computes a planning plan.
func (s *Service) Plan(ctx context.Context, orgID, planID string) (string, error) {
	plan, err := s.store.GetPlan(ctx, orgID, planID)
	if errors.Is(err, ErrNotFound) {
		return StatusFailed, nil // its project was deleted
	}
	if err != nil {
		return "", err
	}
	if plan.Status != StatusPlanning {
		return plan.Status, nil
	}
	failure, err := s.awaitAnalyses(ctx, plan)
	if err != nil {
		return "", err
	}
	if failure != "" {
		return s.failPlanning(ctx, orgID, planID, failure)
	}
	result, revision, err := s.compute(ctx, plan)
	if permanent(err) {
		return s.failPlanning(ctx, orgID, planID, err.Error())
	}
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	plan.setResult(result)
	plan.Revision, plan.PlannedAt = revision, &now
	plan.Status = StatusReady
	if len(result.Problems) > 0 {
		plan.Status = StatusBlocked
	} else {
		expires := now.Add(s.window)
		plan.ExpiresAt = &expires
	}
	ok, err := s.store.FinishPlanning(ctx, plan)
	if err != nil {
		return "", err
	}
	if !ok {
		return s.status(ctx, orgID, planID)
	}
	s.telemetry.Planned(plan.Status)
	return plan.Status, nil
}

// failPlanning settles a plan that can never be planned as failed, saying why.
func (s *Service) failPlanning(ctx context.Context, orgID, planID, reason string) (string, error) {
	if err := s.store.FailPlanning(ctx, orgID, planID, reason, s.now().UTC()); err != nil {
		return "", err
	}
	s.telemetry.Planned(StatusFailed)
	return StatusFailed, nil
}

func (s *Service) status(ctx context.Context, orgID, planID string) (string, error) {
	plan, err := s.store.GetPlan(ctx, orgID, planID)
	if errors.Is(err, ErrNotFound) {
		return StatusFailed, nil
	}
	if err != nil {
		return "", err
	}
	return plan.Status, nil
}

// ApplyDecision implements orchestration.Planner. An approval holds only if
// planning again now gives the same revision: if what's deployed changed
// since, the plan is stale and nothing it says gets done.
func (s *Service) ApplyDecision(ctx context.Context, orgID, planID string, d orchestration.Decision) (string, error) {
	plan, err := s.store.GetPlan(ctx, orgID, planID)
	if errors.Is(err, ErrNotFound) {
		return StatusFailed, nil
	}
	if err != nil {
		return "", err
	}
	if plan.Status != StatusReady {
		return plan.Status, nil
	}
	now := s.now().UTC()
	if !d.Approve {
		return s.settleAs(ctx, plan, StatusRejected, d.By, d.Note, now)
	}
	if d.Revision != plan.Revision {
		// The API checks this before it signals, so this is a decision on
		// a plan that changed in between. The plan keeps waiting.
		return StatusReady, nil
	}
	if checkErr := s.checkBackupDecision(ctx, plan, d); checkErr != nil {
		return "", checkErr
	}
	result, revision, err := s.compute(ctx, plan)
	if permanent(err) {
		return s.settleAs(ctx, plan, StatusStale, d.By, "What the plan was made from is gone: "+err.Error(), now)
	}
	if err != nil {
		return "", err
	}
	if revision != plan.Revision {
		note := "What's deployed changed after this plan was made. Plan again."
		// A canary ramping since says so: planning again is blocked until
		// it finishes.
		for _, f := range result.Problems {
			if f.Code == codeCanaryInProgress {
				note = f.Message
				break
			}
		}
		return s.settleAs(ctx, plan, StatusStale, d.By, note, now)
	}
	if d.Actor != nil {
		if d.Actor.OrgID != orgID || d.Actor.UserID != d.By || d.Actor.ReadOnly {
			return "", security.ErrForbidden
		}
		ctx = security.WithPrincipal(ctx, *d.Actor)
	}
	return s.settleAs(ctx, plan, StatusApproved, d.By, d.Note, now)
}

func (s *Service) settleAs(ctx context.Context, plan *Plan, status, by, note string, now time.Time) (string, error) {
	ok, err := s.store.Decide(ctx, plan.OrgID, plan.ID, status, by, note, now)
	if err != nil {
		return "", err
	}
	if !ok {
		return s.status(ctx, plan.OrgID, plan.ID)
	}
	s.telemetry.Decided(status)
	return status, nil
}

// Expire implements orchestration.Planner.
func (s *Service) Expire(ctx context.Context, orgID, planID string) (string, error) {
	plan, err := s.store.GetPlan(ctx, orgID, planID)
	if errors.Is(err, ErrNotFound) {
		return StatusFailed, nil
	}
	if err != nil {
		return "", err
	}
	if plan.Status != StatusReady {
		return plan.Status, nil
	}
	return s.settleAs(ctx, plan, StatusExpired, "", "Nobody approved or rejected it in time.", s.now().UTC())
}

// Fail implements orchestration.Planner.
func (s *Service) Fail(ctx context.Context, orgID, planID, reason string) error {
	return s.store.FailPlanning(ctx, orgID, planID, reason, s.now().UTC())
}

// ManifestDraft is a proposed first manifest and what it was drawn from.
type ManifestDraft struct {
	// ProjectID keys the draft for the dashboard's client.
	ProjectID string `json:"project_id" forge:"id"`
	// Sources are the commits the draft was drawn from.
	Sources  []graph.SourceRef  `json:"sources"`
	YAML     string             `json:"yaml"`
	Manifest *manifest.Manifest `json:"manifest"`
}

// DraftManifest proposes a manifest from the project's composed graph.
// It saves nothing: the caller reviews it and saves it as the first
// revision.
func (s *Service) DraftManifest(ctx context.Context, p security.Principal, projectID string) (*ManifestDraft, error) {
	if err := refuseAutomation(p); err != nil {
		return nil, err
	}
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	g, states, err := discovery.ProjectGraph(ctx, s.analyses, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(states, func(st discovery.SourceState) bool { return st.Shown != nil }) {
		return nil, ErrNoAnalysis
	}
	m := Draft(g, s.providers)
	data, err := manifest.Marshal(m)
	if err != nil {
		return nil, err
	}
	// A source not analyzed yet has no commit, so it drew nothing.
	drawn := slices.DeleteFunc(slices.Clone(g.Sources), func(r graph.SourceRef) bool { return r.CommitSHA == "" })
	return &ManifestDraft{ProjectID: projectID, Sources: drawn, YAML: string(data), Manifest: m}, nil
}

// EnvironmentConnections returns which connection an environment deploys
// to each cloud with, where someone chose.
func (s *Service) EnvironmentConnections(ctx context.Context, p security.Principal, projectID, environment string) (map[string]string, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountRead, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(environment))
	if err != nil {
		return nil, err
	}
	return s.store.EnvironmentConnections(ctx, p.OrgID, env.ID)
}

// ChooseConnectionInput chooses the connection an environment deploys to a
// cloud with.
type ChooseConnectionInput struct {
	// ConnectionID is the connection, or empty to go back to the
	// organization's only ready one.
	ConnectionID string `json:"connection_id"`
}

// ChooseConnection sets which connection an environment deploys to a
// cloud with. It's an admin's choice, like connecting the account.
func (s *Service) ChooseConnection(ctx context.Context, p security.Principal, projectID, environment, cloud string, in ChooseConnectionInput) (map[string]string, error) {
	if err := refuseAutomation(p); err != nil {
		return nil, err
	}
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, strings.TrimSpace(environment))
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ConnectionID)
	if id != "" {
		if s.clouds == nil {
			return nil, fmt.Errorf("%w: no cloud connections here", ErrInvalid)
		}
		all, err := s.clouds.All(ctx, p.OrgID)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(all, func(c *connections.Connection) bool { return c.ID == id })
		switch {
		case i < 0:
			return nil, fmt.Errorf("%w: this organization has no connection %s", connections.ErrNotFound, id)
		case all[i].Cloud != cloud:
			return nil, fmt.Errorf("%w: %s connects to %s, not %s", ErrInvalid, all[i].Name, all[i].Cloud, cloud)
		}
	}
	if err := s.store.SetEnvironmentConnection(ctx, p.OrgID, projectID, env.ID, cloud, id); err != nil {
		return nil, err
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "environment.connection", Severity: "warning", ProjectID: projectID, Principal: p,
			Attributes: map[string]any{"environment": env.Name, "cloud": cloud, "connection_id": id},
		})
	}
	return s.store.EnvironmentConnections(ctx, p.OrgID, env.ID)
}
