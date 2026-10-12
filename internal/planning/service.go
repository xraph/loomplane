package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// StatusStale is a plan whose approval came after what's deployed changed:
// carrying it out would no longer do what was reviewed.
const StatusStale = "stale"

const (
	listLimit = 30
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

// Service is the plan API, and the planner the deployment workflow calls.
// API methods check the caller's role; workflow methods trust the plan,
// which a checked call created.
type Service struct {
	store     Store
	projects  Projects
	analyses  Analyses
	manifests Manifests
	providers *provider.Registry
	clouds    Connections
	authz     Authorizer
	workflow  Workflow
	audit     projects.Auditor
	window    time.Duration
	now       func() time.Time
	warn      func(msg string, err error)
}

// Deps are what a Service needs.
type Deps struct {
	Store     Store
	Projects  Projects
	Analyses  Analyses
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
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	warn := d.Warn
	if warn == nil {
		warn = func(string, error) {}
	}
	return &Service{
		store: d.Store, projects: d.Projects, analyses: d.Analyses, manifests: d.Manifests,
		providers: d.Providers, clouds: d.Connections, authz: d.Authz, workflow: d.Workflow, audit: d.Audit,
		window: d.Window, now: time.Now, warn: warn,
	}
}

var _ orchestration.Planner = (*Service)(nil)

// CreatePlanInput asks for a plan of one environment.
type CreatePlanInput struct {
	// Environment is the environment's name.
	Environment string `json:"environment"`
}

// DecideInput approves or rejects a plan.
type DecideInput struct {
	// Revision is the plan revision you reviewed. Approving needs it, so
	// what gets approved is what you saw.
	Revision string `json:"revision,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Create plans an environment from the project's current manifest and its
// latest analysis. It supersedes the environment's open plan, if any.
func (s *Service) Create(ctx context.Context, p security.Principal, projectID string, in CreatePlanInput) (*Plan, error) {
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
	rev, err := s.manifests.Latest(ctx, p.OrgID, projectID)
	if errors.Is(err, manifests.ErrNotFound) {
		return nil, ErrNoManifest
	}
	if err != nil {
		return nil, err
	}
	analysis, err := s.latestAnalysis(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}

	id, err := typeid.Generate("plan")
	if err != nil {
		return nil, fmt.Errorf("generate plan id: %w", err)
	}
	plan := &Plan{
		ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Environment: env.Name,
		Status: StatusPlanning, AnalysisID: analysis.ID, CommitSHA: analysis.CommitSHA,
		ManifestID: rev.ID, ManifestRevision: rev.Revision, CreatedBy: p.UserID, CreatedAt: s.now().UTC(),
	}
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
			Action: "plan.create", Severity: "info", ProjectID: projectID, Principal: p,
			Attributes: map[string]any{
				"plan_id": plan.ID, "environment": env.Name, "commit_sha": plan.CommitSHA,
				"manifest_revision": plan.ManifestRevision, "superseded": superseded,
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

func (s *Service) latestAnalysis(ctx context.Context, orgID, projectID string) (*discovery.Analysis, error) {
	// A plan still pins one analysis; planning from every source's
	// analysis comes later, so this serves the first source that has one.
	sources, err := s.analyses.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		list, err := s.analyses.ListSourceAnalyses(ctx, orgID, src.ID, analysisLookback)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			if a.Status == discovery.StatusSucceeded {
				return a, nil
			}
		}
	}
	return nil, ErrNoAnalysis
}

// settle waits a moment for the workflow to move a plan on, and returns it
// as it is then. It never fails: the plan as it was is a fine answer.
func (s *Service) settle(ctx context.Context, plan *Plan) *Plan {
	status := plan.Status
	deadline := time.NewTimer(settleWait)
	defer deadline.Stop()
	tick := time.NewTicker(settlePoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return plan
		case <-deadline.C:
			return plan
		case <-tick.C:
		}
		got, err := s.store.GetPlan(ctx, plan.OrgID, plan.ID)
		if err != nil {
			return plan
		}
		if got.Status != status {
			return got
		}
	}
}

// List returns a project's latest plans, newest first, in one environment
// or, with environment empty, in all of them.
func (s *Service) List(ctx context.Context, p security.Principal, projectID, environment string) ([]*Plan, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	envID := ""
	if environment = strings.TrimSpace(environment); environment != "" {
		env, err := s.environment(ctx, p.OrgID, projectID, environment)
		if err != nil {
			return nil, err
		}
		envID = env.ID
	}
	return s.store.ListPlans(ctx, p.OrgID, projectID, envID, listLimit)
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
	reqID, err := typeid.Generate("dec")
	if err != nil {
		return nil, fmt.Errorf("generate decision id: %w", err)
	}
	dec := orchestration.Decision{Approve: approve, Revision: in.Revision, By: p.UserID, Note: strings.TrimSpace(in.Note)}
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

// Providers returns the deployment targets a manifest can name.
func (s *Service) Providers() []*provider.Provider { return s.providers.List() }

// revisionInput is everything a plan's revision covers: what it was made
// from, what's deployed, and what it would do.
type revisionInput struct {
	EnvironmentID string        `json:"environment_id"`
	Environment   string        `json:"environment"`
	AnalysisID    string        `json:"analysis_id"`
	CommitSHA     string        `json:"commit_sha"`
	Manifest      string        `json:"manifest"`
	Deployed      []deployedRef `json:"deployed"`
	Result        *Result       `json:"result"`
}

type deployedRef struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Region     string `json:"region"`
	ExternalID string `json:"external_id"`
	Config     string `json:"config"`
}

// compute plans a plan from its pinned commit and manifest, against what's
// deployed now, and returns the result with its revision.
func (s *Service) compute(ctx context.Context, plan *Plan) (*Result, string, error) {
	g, err := s.analyses.Graph(ctx, plan.OrgID, plan.AnalysisID)
	if err != nil {
		return nil, "", fmt.Errorf("load the analysis: %w", err)
	}
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
	options, err := s.connectionOptions(ctx, plan.OrgID)
	if err != nil {
		return nil, "", err
	}
	chosen, err := s.store.EnvironmentConnections(ctx, plan.OrgID, plan.EnvironmentID)
	if err != nil {
		return nil, "", err
	}
	result := Compute(Input{
		Environment: plan.Environment, Graph: g, Manifest: rev.Manifest, Providers: s.providers, Bindings: bindings,
		Connections: options, Chosen: chosen,
	})
	in := revisionInput{
		EnvironmentID: plan.EnvironmentID, Environment: plan.Environment, AnalysisID: plan.AnalysisID,
		CommitSHA: plan.CommitSHA, Manifest: rev.Digest, Deployed: make([]deployedRef, 0, len(bindings)), Result: result,
	}
	for _, b := range bindings {
		in.Deployed = append(in.Deployed, deployedRef{
			ID: b.ID, Provider: b.Provider, Region: b.Region, ExternalID: b.ExternalID, Config: string(b.Config),
		})
	}
	revision, err := Digest(in)
	if err != nil {
		return nil, "", err
	}
	return result, revision, nil
}

func (s *Service) connectionOptions(ctx context.Context, orgID string) ([]ConnectionOption, error) {
	if s.clouds == nil {
		return nil, nil
	}
	all, err := s.clouds.All(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list cloud connections: %w", err)
	}
	out := make([]ConnectionOption, 0, len(all))
	for _, c := range all {
		out = append(out, ConnectionOption{ID: c.ID, Name: c.Name, Cloud: c.Cloud, Ready: c.Status == connections.StatusReady})
	}
	return out, nil
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
	result, revision, err := s.compute(ctx, plan)
	if permanent(err) {
		if ferr := s.store.FailPlanning(ctx, orgID, planID, err.Error(), s.now().UTC()); ferr != nil {
			return "", ferr
		}
		return StatusFailed, nil
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
	return plan.Status, nil
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
	_, revision, err := s.compute(ctx, plan)
	if permanent(err) {
		return s.settleAs(ctx, plan, StatusStale, d.By, "What the plan was made from is gone: "+err.Error(), now)
	}
	if err != nil {
		return "", err
	}
	if revision != plan.Revision {
		return s.settleAs(ctx, plan, StatusStale, d.By, "What's deployed changed after this plan was made. Plan again.", now)
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
