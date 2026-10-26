// Package client calls Loomplane's API with an API key. The loomctl CLI is
// built on it, and you can use it from your own Go programs: CI jobs that
// plan and approve, scripts that report what's deployed, an agent's tools.
//
//	c := client.New("https://loomplane.example.com", os.Getenv("LOOMPLANE_API_KEY"))
//	projects, err := c.Projects(ctx)
//
// Make a key under Settings, API keys. It acts as you, in one
// organization, with your role.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/manifest"
)

// Client calls one Loomplane with one API key.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New returns a Client for the Loomplane at baseURL.
func New(baseURL, apiKey string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		key:  apiKey,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// WithHTTPClient replaces the HTTP client, for proxies or tests.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// Error is a response Loomplane refused, with its status and message.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("loomplane answered %d", e.Status)
	}
	return e.Message
}

// Is lets errors.Is match an Error by status: errors.Is(err, ErrNotFound).
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Status == e.Status && t.Message == ""
}

// Errors to compare with errors.Is.
var (
	ErrUnauthorized = &Error{Status: http.StatusUnauthorized}
	ErrForbidden    = &Error{Status: http.StatusForbidden}
	ErrNotFound     = &Error{Status: http.StatusNotFound}
	ErrConflict     = &Error{Status: http.StatusConflict}
)

// do sends a request and decodes the JSON answer into out, if it isn't nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return &Error{Status: res.StatusCode, Message: message(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("read loomplane's answer to %s %s: %w", method, path, err)
	}
	return nil
}

// message pulls the human part out of an error body: Forge's
// {"error": ...} or {"message": ...}, or the body itself.
func message(data []byte) string {
	var e struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return strings.TrimSpace(string(data))
}

func esc(s string) string { return url.PathEscape(s) }

// --- Who you are ---

// Session is who a key acts as, and what it may do.
type Session struct {
	UserID      string   `json:"user_id"`
	OrgID       string   `json:"org_id"`
	Permissions []string `json:"permissions"`
}

// Session reads who the key acts as. It's the cheapest way to check a
// key works.
func (c *Client) Session(ctx context.Context) (*Session, error) {
	var s Session
	return &s, c.do(ctx, http.MethodGet, "/api/v1/session", nil, &s)
}

// --- Projects ---

// Project is an application Loomplane deploys, built from any number of
// repositories.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description,omitempty"`
	// Repositories are the "owner/name" of each repository the project
	// builds from.
	Repositories []string  `json:"repositories,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// NewProject is a project to create.
type NewProject struct {
	Name string `json:"name"`
	// RepositoryURL is attached as the project's first repository.
	RepositoryURL string `json:"repository_url,omitempty"`
	// Repositories are attached after it, in order, up to ten in all.
	Repositories []NewRepository `json:"repositories,omitempty"`
	Description  string          `json:"description,omitempty"`
}

// NewRepository is a repository a new project starts with.
type NewRepository struct {
	// Repository is a GitHub URL or "owner/name".
	Repository string `json:"repository"`
	// Alias names it in the project. Empty picks one from its name.
	Alias string `json:"alias,omitempty"`
	// Root is the directory the project builds from, and Ref the branch
	// analyses read. Empty means the whole repository and its default
	// branch.
	Root string `json:"root,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// Projects lists the organization's projects.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	return out.Projects, c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &out)
}

// Project reads one project by ID.
func (c *Client) Project(ctx context.Context, id string) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(id), nil, &p)
}

// FindProject finds a project by its ID, slug or name.
func (c *Client) FindProject(ctx context.Context, ref string) (*Project, error) {
	projects, err := c.Projects(ctx)
	if err != nil {
		return nil, err
	}
	var byName []Project
	for _, p := range projects {
		if p.ID == ref || p.Slug == ref {
			return &p, nil
		}
		if strings.EqualFold(p.Name, ref) {
			byName = append(byName, p)
		}
	}
	switch len(byName) {
	case 1:
		return &byName[0], nil
	case 0:
		return nil, &Error{Status: http.StatusNotFound, Message: fmt.Sprintf("no project is called %q", ref)}
	}
	return nil, &Error{Status: http.StatusConflict, Message: fmt.Sprintf("more than one project is called %q; use its slug or ID", ref)}
}

// CreateProject creates a project, with its production and preview
// environments.
func (c *Client) CreateProject(ctx context.Context, in NewProject) (*Project, error) {
	var p Project
	return &p, c.do(ctx, http.MethodPost, "/api/v1/projects", in, &p)
}

// Environment is where a project deploys: production, preview, staging.
type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Environments lists a project's environments.
func (c *Client) Environments(ctx context.Context, projectID string) ([]Environment, error) {
	var out struct {
		Environments []Environment `json:"environments"`
	}
	return out.Environments, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/environments", nil, &out)
}

// --- Analysis ---

// Analysis is a reading of one of a project's repositories at one commit.
type Analysis struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// Source is the alias of the repository it read, and Repository its
	// "owner/name".
	Source     string `json:"source,omitempty"`
	Repository string `json:"repository,omitempty"`
	Status     string `json:"status"`
	Ref        string `json:"ref,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Error      string `json:"error,omitempty"`
	Summary    struct {
		Services   int `json:"services"`
		Deployable int `json:"deployable"`
		Packages   int `json:"packages"`
		Resources  int `json:"resources"`
	} `json:"summary"`
}

// Done reports whether the analysis has finished, either way.
func (a *Analysis) Done() bool { return a.Status == "succeeded" || a.Status == "failed" }

// AnalyzeInput is what to analyze: the repositories named by alias, or
// every one of the project's when Sources is empty. Ref is a branch, tag
// or commit, for one repository only; empty means its tracked branch.
type AnalyzeInput struct {
	Sources []string `json:"sources,omitempty"`
	Ref     string   `json:"ref,omitempty"`
}

// Analyze starts one analysis per repository asked for.
func (c *Client) Analyze(ctx context.Context, projectID string, in AnalyzeInput) ([]Analysis, error) {
	var out struct {
		Analyses []Analysis `json:"analyses"`
	}
	return out.Analyses, c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(projectID)+"/analyses", in, &out)
}

// Analysis reads one analysis.
func (c *Client) Analysis(ctx context.Context, id string) (*Analysis, error) {
	var a Analysis
	return &a, c.do(ctx, http.MethodGet, "/api/v1/analyses/"+esc(id), nil, &a)
}

// --- Manifest ---

// ManifestRevision is one saved version of a project's loomplane.yaml.
type ManifestRevision struct {
	ID        string    `json:"id"`
	Revision  int       `json:"revision"`
	YAML      string    `json:"yaml"`
	CreatedAt time.Time `json:"created_at"`
}

// Manifest reads the project's current loomplane.yaml.
func (c *Client) Manifest(ctx context.Context, projectID string) (*ManifestRevision, error) {
	var m ManifestRevision
	return &m, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/manifest", nil, &m)
}

// DraftManifest drafts a loomplane.yaml from the project's latest analysis.
func (c *Client) DraftManifest(ctx context.Context, projectID string) (string, error) {
	var d struct {
		YAML string `json:"yaml"`
	}
	return d.YAML, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/manifest/draft", nil, &d)
}

// SaveManifest saves a new revision of the project's loomplane.yaml.
// Saving what's already current adds nothing.
func (c *Client) SaveManifest(ctx context.Context, projectID, yaml string) (*ManifestRevision, error) {
	var m ManifestRevision
	return &m, c.do(ctx, http.MethodPut, "/api/v1/projects/"+esc(projectID)+"/manifest", map[string]string{"yaml": yaml}, &m)
}

// ManifestEdit is one change to the project's manifest, which keeps the
// YAML's comments and order.
type ManifestEdit struct {
	AddResource *NewResource `json:"add_resource,omitempty"`
	// Link makes a service need something, with the options it passes to
	// it. Unlink removes the need and its options. SetUse chooses where one
	// dependency of a catalog resource comes from.
	Link   *ServiceLink `json:"link,omitempty"`
	Unlink *ServiceLink `json:"unlink,omitempty"`
	SetUse *ResourceUse `json:"set_use,omitempty"`
}

// ServiceLink names a service and something it needs. Options are what the
// service passes to the need, and Unlink ignores them.
type ServiceLink struct {
	Service string            `json:"service"`
	Need    string            `json:"need"`
	Options map[string]string `json:"options,omitempty"`
}

// ResourceUse sets where one dependency of a catalog resource comes from.
// A Use with nothing set asks for a new dependency.
type ResourceUse struct {
	Resource   string       `json:"resource"`
	Dependency string       `json:"dependency"`
	Use        manifest.Use `json:"use"`
}

// NewResource is a resource to add to the manifest. Target is the checked
// connection it runs on, and Uses says where its dependencies come from.
type NewResource struct {
	Name     string                  `json:"name"`
	Kind     string                  `json:"kind"`
	Provider string                  `json:"provider,omitempty"`
	Target   string                  `json:"target,omitempty"`
	Version  string                  `json:"version,omitempty"`
	Uses     map[string]manifest.Use `json:"uses,omitempty"`
}

// EditManifest applies one change to the project's current manifest and
// saves the result as a new revision. A change that's already there adds
// nothing.
func (c *Client) EditManifest(ctx context.Context, projectID string, in ManifestEdit) (*ManifestRevision, error) {
	var m ManifestRevision
	return &m, c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(projectID)+"/manifest/edits", in, &m)
}

// --- Plans ---

// Finding is a problem, which blocks a plan, or a warning, which doesn't.
type Finding struct {
	Code    string `json:"code"`
	Subject string `json:"subject,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// Change is one setting an update changes.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// Action is one thing a plan would do.
type Action struct {
	// Resource preserves the complete provider request for JSON plan review.
	Resource json.RawMessage `json:"resource,omitempty"`
	ID       string          `json:"id"`
	Op       string          `json:"op"`
	Subject  string          `json:"subject"`
	Name     string          `json:"name"`
	Provider string          `json:"provider"`
	Region   string          `json:"region,omitempty"`
	Creates  string          `json:"creates,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Step     int             `json:"step"`
	Changes  []Change        `json:"changes,omitempty"`
}

// AutomationName is how Loomplane automation, which rolls back a release
// whose watch or canary failed, is named wherever it acts.
const AutomationName = "Loomplane automation"

// ActorName is how to show a created_by, decided_by or started_by, such as
// Plan.DecidedBy: "Loomplane automation" for the organization's automation
// (loomplane:<org>), else the ID as given.
func ActorName(id string) string {
	if org, ok := strings.CutPrefix(id, "loomplane:"); ok && org != "" {
		return AutomationName
	}
	return id
}

// Plan is what deploying a manifest to an environment would do, waiting
// for someone to approve or reject it.
type Plan struct {
	ID           string            `json:"id"`
	ProjectID    string            `json:"project_id"`
	Catalog      bool              `json:"catalog"`
	Connections  map[string]string `json:"connections"`
	Environment  string            `json:"environment"`
	Status       string            `json:"status"`
	Revision     string            `json:"revision"`
	Sources      []PlanSource      `json:"sources"`
	Executable   bool              `json:"executable"`
	Error        string            `json:"error,omitempty"`
	Actions      []Action          `json:"actions"`
	Problems     []Finding         `json:"problems"`
	Warnings     []Finding         `json:"warnings"`
	DecidedBy    string            `json:"decided_by,omitempty"`
	DecisionNote string            `json:"decision_note,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	// Kind is "release", "promotion" or "rollback".
	Kind string `json:"kind"`
	// From is the environment a promotion deploys from.
	From      string           `json:"from_environment"`
	Rollback  *Rollback        `json:"rollback"`
	Leftovers []Leftover       `json:"leftovers"`
	Artifacts []ArtifactChoice `json:"artifacts"`
}

// Rollback is what a rollback plan undoes and what it restores.
type Rollback struct {
	Reason              string    `json:"reason"`
	Trigger             string    `json:"trigger"`
	TargetDeploymentID  string    `json:"target_deployment_id"`
	TargetPlanID        string    `json:"target_plan_id"`
	RestoreDeploymentID string    `json:"restore_deployment_id"`
	RestorePlanID       string    `json:"restore_plan_id"`
	RestoreFinishedAt   time.Time `json:"restore_finished_at"`
	WatchID             string    `json:"watch_id,omitempty"`
}

// Leftover is something a rollback leaves as the bad release made it.
type Leftover struct {
	Subject     string `json:"subject"`
	Name        string `json:"name"`
	Op          string `json:"op"`
	DataBearing bool   `json:"data_bearing"`
	Reason      string `json:"reason"`
}

// ArtifactChoice says whether a service reuses an earlier build.
type ArtifactChoice struct {
	Service   string `json:"service"`
	Reused    bool   `json:"reused"`
	Reference string `json:"reference,omitempty"`
	Revision  string `json:"revision,omitempty"`
	From      string `json:"from,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// PlanSource is one repository a plan was made from, at the commit it
// pins. A plan moves the repositories in its scope; the rest stay at what's
// deployed.
type PlanSource struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	CommitSHA  string `json:"commit_sha"`
	InScope    bool   `json:"in_scope"`
	FromPlanID string `json:"from_plan_id,omitempty"`
}

// Planning reports whether the plan is still being worked out.
func (p *Plan) Planning() bool { return p.Status == "planning" }

// Commits names each repository the plan moves at its short commit, as
// "web@3f2c1a9 api@9a1b02e".
func (p *Plan) Commits() string {
	var parts []string
	for _, s := range p.Sources {
		if s.InScope {
			parts = append(parts, s.Alias+"@"+s.CommitSHA[:min(7, len(s.CommitSHA))])
		}
	}
	return strings.Join(parts, " ")
}

// Plans lists a project's plans, newest first.
func (c *Client) Plans(ctx context.Context, projectID string) ([]Plan, error) {
	var out struct {
		Plans []Plan `json:"plans"`
	}
	return out.Plans, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/plans", nil, &out)
}

// NewPlan is a plan to make: for an environment, moving the repositories
// in Scope, by alias, or all of them when it's empty.
type NewPlan struct {
	Catalog     bool     `json:"catalog,omitempty"`
	Environment string   `json:"environment"`
	Scope       []string `json:"scope,omitempty"`
	// From is the environment to promote from.
	From string `json:"from,omitempty"`
}

// CreatePlan plans the project's current manifest for an environment.
// Planning runs in the background; read the plan until Planning is false.
func (c *Client) CreatePlan(ctx context.Context, projectID string, in NewPlan) (*Plan, error) {
	var p Plan
	return &p, c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(projectID)+"/plans", in, &p)
}

// Plan reads one plan.
func (c *Client) Plan(ctx context.Context, id string) (*Plan, error) {
	var p Plan
	return &p, c.do(ctx, http.MethodGet, "/api/v1/plans/"+esc(id), nil, &p)
}

// Approve approves the plan at the revision you reviewed. If what's
// deployed changed since, the plan comes back stale instead.
func (c *Client) Approve(ctx context.Context, planID, revision, note string) (*Plan, error) {
	var p Plan
	return &p, c.do(ctx, http.MethodPost, "/api/v1/plans/"+esc(planID)+"/approve", map[string]string{"revision": revision, "note": note}, &p)
}

// Reject rejects the plan.
func (c *Client) Reject(ctx context.Context, planID, note string) (*Plan, error) {
	var p Plan
	return &p, c.do(ctx, http.MethodPost, "/api/v1/plans/"+esc(planID)+"/reject", map[string]string{"note": note}, &p)
}

// --- Deployments ---

// Step is one action of a deployment, as it went.
type Step struct {
	ActionID string `json:"action_id"`
	Name     string `json:"name"`
	Op       string `json:"op"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
}

// Deployment is an approved plan being carried out.
type Deployment struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	PlanID      string `json:"plan_id"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
	// CommitSHA names what the deployment moves: the commit of its one
	// repository, or each repository at its short commit.
	CommitSHA string `json:"commit_sha,omitempty"`
	// Sources are the repositories it moves, each at its commit.
	Sources    []DeploymentSource `json:"sources,omitempty"`
	Error      string             `json:"error,omitempty"`
	Retries    int                `json:"retries"`
	Steps      []Step             `json:"steps"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
}

// DeploymentSource is one repository a deployment moves.
type DeploymentSource struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	CommitSHA  string `json:"commit_sha"`
}

// Done reports whether the deployment has finished, either way.
func (d *Deployment) Done() bool { return d.Status == "succeeded" || d.Status == "failed" }

// Deployments lists a project's deployments, newest first.
func (c *Client) Deployments(ctx context.Context, projectID string) ([]Deployment, error) {
	var out struct {
		Deployments []Deployment `json:"deployments"`
	}
	return out.Deployments, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/deployments", nil, &out)
}

// Deployment reads one deployment.
func (c *Client) Deployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	return &d, c.do(ctx, http.MethodGet, "/api/v1/deployments/"+esc(id), nil, &d)
}

// DeploymentForPlan reads the deployment an approved plan started.
func (c *Client) DeploymentForPlan(ctx context.Context, planID string) (*Deployment, error) {
	var d Deployment
	return &d, c.do(ctx, http.MethodGet, "/api/v1/plans/"+esc(planID)+"/deployment", nil, &d)
}

// Retry retries a failed deployment from the step that failed.
func (c *Client) Retry(ctx context.Context, deploymentID string) (*Deployment, error) {
	var d Deployment
	return &d, c.do(ctx, http.MethodPost, "/api/v1/deployments/"+esc(deploymentID)+"/retry", nil, &d)
}

// Binding is what runs a service or resource in an environment now.
type Binding struct {
	PhysicalID    string     `json:"physical_id,omitempty"`
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environment_id"`
	Subject       string     `json:"subject"`
	Name          string     `json:"name"`
	Provider      string     `json:"provider"`
	Region        string     `json:"region,omitempty"`
	URL           string     `json:"url,omitempty"`
	Revision      string     `json:"revision,omitempty"`
	CommitSHA     string     `json:"commit_sha,omitempty"`
	Drift         string     `json:"drift,omitempty"`
	DriftMessage  string     `json:"drift_message,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
}

// Bindings lists what runs each of a project's services and resources in
// an environment.
func (c *Client) Bindings(ctx context.Context, projectID, environment string) ([]Binding, error) {
	var out struct {
		Bindings []Binding `json:"bindings"`
	}
	path := "/api/v1/projects/" + esc(projectID) + "/bindings?environment=" + url.QueryEscape(environment)
	return out.Bindings, c.do(ctx, http.MethodGet, path, nil, &out)
}

// --- Variables ---

// Variable is a value a project's services get. A secret's value is never
// returned.
type Variable struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	Secret      bool   `json:"secret"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// NewVariable is a variable to set. An empty Environment means every
// environment; an empty Service, every service.
type NewVariable struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Secret      bool   `json:"secret,omitempty"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
}

// Variables lists a project's variables.
func (c *Client) Variables(ctx context.Context, projectID string) ([]Variable, error) {
	var out struct {
		Variables []Variable `json:"variables"`
	}
	return out.Variables, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/variables", nil, &out)
}

// SetVariable sets a project variable.
func (c *Client) SetVariable(ctx context.Context, projectID string, in NewVariable) (*Variable, error) {
	var v Variable
	return &v, c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(projectID)+"/variables", in, &v)
}

// DeleteVariable deletes a variable.
func (c *Client) DeleteVariable(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/variables/"+esc(id), nil, nil)
}

// --- Waiting ---

// Every is how often the Wait helpers read again.
var Every = 2 * time.Second

// WaitAnalysis reads the analysis until it finishes, calling seen with
// each reading, which may be nil.
func (c *Client) WaitAnalysis(ctx context.Context, id string, seen func(*Analysis)) (*Analysis, error) {
	return wait(ctx, func() (*Analysis, bool, error) {
		a, err := c.Analysis(ctx, id)
		if err != nil {
			return nil, false, err
		}
		if seen != nil {
			seen(a)
		}
		done := a.Done()
		return a, done, nil
	})
}

// WaitPlan reads the plan until it's planned.
func (c *Client) WaitPlan(ctx context.Context, id string) (*Plan, error) {
	return wait(ctx, func() (*Plan, bool, error) {
		p, err := c.Plan(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return p, !p.Planning(), nil
	})
}

// WaitDeployment reads the deployment until it finishes, calling seen with
// each reading, which may be nil.
func (c *Client) WaitDeployment(ctx context.Context, id string, seen func(*Deployment)) (*Deployment, error) {
	return wait(ctx, func() (*Deployment, bool, error) {
		d, err := c.Deployment(ctx, id)
		if err != nil {
			return nil, false, err
		}
		if seen != nil {
			seen(d)
		}
		done := d.Done()
		return d, done, nil
	})
}

func wait[T any](ctx context.Context, read func() (T, bool, error)) (T, error) {
	for {
		v, done, err := read()
		if err != nil || done {
			return v, err
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(Every):
		}
	}
}
