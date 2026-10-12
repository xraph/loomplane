package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// Executor carries out actions on one provider. Each call is a durable
// activity's work: it may run again after a crash or a timeout, so it must
// be safe to repeat, and it should pick up a cloud operation it started
// (Job.Resume) rather than start another.
type Executor interface {
	// Apply creates or updates what runs an action, and returns where it
	// answers.
	Apply(ctx context.Context, job *Job) (*Outcome, error)
	// Remove deletes what runs an action. Something already gone is not
	// an error.
	Remove(ctx context.Context, job *Job, b *planning.Binding) error
}

// Job is one action to carry out, with everything its executor needs.
type Job struct {
	Deployment *Deployment
	Plan       *planning.Plan
	Action     planning.Action
	Connection *connections.Connection
	// Credential is the connection's secret. It's only ever passed to the
	// cloud's client, in memory.
	Credential []byte
	// Env are the variables the service gets: its resolved variables and
	// the addresses of what it needs.
	Env map[string]string
	// Source opens the tarball of the repository the service is built
	// from, at the plan's commit, as GitHub serves it: gzip, with one
	// top-level directory.
	Source func(ctx context.Context) (io.ReadCloser, int64, error)
	// Binding is what runs the action now, or nil.
	Binding *planning.Binding
	// Resume is what Checkpoint saved in an earlier attempt, such as a
	// build's operation name, or nil.
	Resume []byte
	// Checkpoint saves progress that a retry resumes from, and reports
	// that the activity is alive.
	Checkpoint func(data []byte) error
	// Progress shows what the step is doing.
	Progress func(status, message string)
}

// Outcome is what an executor did.
type Outcome struct {
	// ExternalID is what the provider calls it: a Ctrl Plane instance or a
	// Vercel project.
	ExternalID string
	URL        string
	// Image is the container image a build produced.
	Image string
}

// Plans reads plans and bindings, and records what's deployed.
type Plans interface {
	GetPlan(ctx context.Context, orgID, id string) (*planning.Plan, error)
	ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	PutBinding(ctx context.Context, b *planning.Binding) error
	DeleteBinding(ctx context.Context, orgID, id string) error
	// RecordDeployed records the commit of one repository now deployed in
	// an environment, which the next plan holds the repository at.
	RecordDeployed(ctx context.Context, orgID string, d *planning.DeployedSource) error
}

// Credentials reveals a connection's credential.
type Credentials interface {
	Credential(ctx context.Context, orgID, id string) (*connections.Connection, []byte, error)
}

// Variables resolves what a service gets in an environment.
type Variables interface {
	Values(ctx context.Context, orgID, projectID, environment, service string) (map[string]string, error)
}

// Sources downloads a commit and stores it as an artifact.
type Sources interface {
	// Store puts the tarball of repository ("owner/name") at sha at key,
	// unless it's there already.
	Store(ctx context.Context, orgID, repository, sha, key string) error
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// Runner carries out approved plans, one activity at a time. Its methods
// are the deployment workflow's activities after approval.
type Runner struct {
	store       Store
	plans       Plans
	credentials Credentials
	variables   Variables
	sources     Sources
	providers   *provider.Registry
	executors   map[string]Executor
	http        *http.Client
	now         func() time.Time
	// verifyWait is how long a deployed service has to answer.
	verifyWait time.Duration
}

// RunnerDeps are what a Runner needs.
type RunnerDeps struct {
	Store       Store
	Plans       Plans
	Credentials Credentials
	Variables   Variables
	Sources     Sources
	Providers   *provider.Registry
	// Executors carry out each provider's actions, by provider name.
	Executors map[string]Executor
}

// NewRunner returns a Runner.
func NewRunner(d RunnerDeps) *Runner {
	return &Runner{
		store: d.Store, plans: d.Plans, credentials: d.Credentials, variables: d.Variables,
		sources: d.Sources, providers: d.Providers, executors: d.Executors,
		http: &http.Client{Timeout: 15 * time.Second}, now: time.Now, verifyWait: 3 * time.Minute,
	}
}

// Start records a deployment for an approved plan, or returns the one
// already recorded, and lists its actions by plan step, in the order they
// run. A plan that isn't executable deploys nothing, and Start returns no
// deployment: approving it recorded a decision.
func (r *Runner) Start(ctx context.Context, orgID, planID string) (deploymentID string, steps [][]string, err error) {
	plan, err := r.plans.GetPlan(ctx, orgID, planID)
	if err != nil {
		return "", nil, err
	}
	if plan.Status != planning.StatusApproved || !plan.Executable {
		return "", nil, nil
	}
	id, err := typeid.Generate("dep")
	if err != nil {
		return "", nil, fmt.Errorf("generate deployment id: %w", err)
	}
	moves := inScope(plan)
	d := &Deployment{
		ID: id.String(), OrgID: orgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID,
		Environment: plan.Environment, PlanID: plan.ID, CommitSHA: commitLabel(moves), Status: StatusRunning,
		Sources: sourceArtifacts(moves), StartedBy: plan.DecidedBy, StartedAt: r.now().UTC(),
	}
	if len(moves) == 1 {
		d.CommitSHA = moves[0].CommitSHA
	}
	var order [][]string
	for _, a := range plan.Actions {
		if a.Op == planning.Keep {
			continue
		}
		d.Steps = append(d.Steps, Step{
			ActionID: a.ID, Name: a.Name, Subject: string(a.Subject), Op: string(a.Op), Provider: a.Provider,
			Order: a.Step, Status: StepPending,
		})
		if len(order) == 0 || d.Steps[len(d.Steps)-2].Order != a.Step {
			order = append(order, nil)
		}
		order[len(order)-1] = append(order[len(order)-1], a.ID)
	}
	if d, err = r.store.CreateDeployment(ctx, d); err != nil {
		return "", nil, err
	}
	return d.ID, order, nil
}

// inScope returns the repositories a plan moves, in alias order.
func inScope(plan *planning.Plan) []planning.PlanSource {
	out := slices.DeleteFunc(slices.Clone(plan.Sources), func(ps planning.PlanSource) bool { return !ps.InScope })
	slices.SortFunc(out, func(a, b planning.PlanSource) int { return strings.Compare(a.Alias, b.Alias) })
	return out
}

func sourceArtifacts(ps []planning.PlanSource) []SourceArtifact {
	out := make([]SourceArtifact, 0, len(ps))
	for _, s := range ps {
		out = append(out, SourceArtifact{Alias: s.Alias, Repository: s.Repository, CommitSHA: s.CommitSHA})
	}
	return out
}

// commitLabel names each repository a plan moves at its short commit, as
// "web@3f2c1a9 api@9a1b02e", for a list that has room for one line.
func commitLabel(ps []planning.PlanSource) string {
	var parts []string
	for _, s := range ps {
		if s.InScope {
			parts = append(parts, s.Alias+"@"+s.CommitSHA[:min(7, len(s.CommitSHA))])
		}
	}
	return strings.Join(parts, " ")
}

// SourceKey is where the tarball of one repository's commit is stored.
func SourceKey(orgID, projectID, alias, sha string) string {
	return "sources/" + orgID + "/" + projectID + "/" + alias + "/" + sha + ".tar.gz"
}

// FetchSource stores the commit of each repository the deployment moves as
// an artifact, once. A retry skips what an earlier attempt stored.
func (r *Runner) FetchSource(ctx context.Context, orgID, deploymentID string) error {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	sources := d.Sources
	if len(sources) == 0 {
		// A deployment started before deployments fetched each repository,
		// and not fetched yet, takes its repositories from its plan.
		plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
		if err != nil {
			return err
		}
		sources = sourceArtifacts(inScope(plan))
	}
	for i := range sources {
		s := &sources[i]
		if s.Key != "" {
			continue
		}
		key := SourceKey(orgID, d.ProjectID, s.Alias, s.CommitSHA)
		if err := r.sources.Store(ctx, orgID, s.Repository, s.CommitSHA, key); err != nil {
			return fmt.Errorf("store %s@%s: %w", s.Alias, s.CommitSHA, err)
		}
		s.Key = key
		// Each key is saved as it's stored, so a retry after a failure
		// doesn't ask GitHub again for what's already here.
		if err := r.store.SetSources(ctx, orgID, d.ID, sources); err != nil {
			return err
		}
	}
	return nil
}

// sourceFor returns the artifact key of the repository a service is built
// from. A plan made from a graph with no sources names no repository, and
// a deployment from before deployments fetched each repository has one
// source with no alias; either way, a deployment of one repository builds
// from it.
func sourceFor(d *Deployment, repo string) (string, error) {
	var found *SourceArtifact
	for i := range d.Sources {
		if d.Sources[i].Alias == repo {
			found = &d.Sources[i]
		}
	}
	if found == nil && len(d.Sources) == 1 && (repo == "" || d.Sources[0].Alias == "") {
		found = &d.Sources[0]
	}
	switch {
	case found == nil && repo == "":
		return "", errors.New("the service names no repository, so the deployment can't tell which to build it from")
	case found == nil:
		return "", errors.New("the deployment has no source stored for " + repo)
	case found.Key == "":
		return "", errors.New("the deployment's source hasn't been stored")
	}
	return found.Key, nil
}

// Run carries out one action of a deployment. resume is the last
// checkpoint an earlier attempt saved, or nil; heartbeat saves one and
// tells Dispatch the activity is alive.
func (r *Runner) Run(ctx context.Context, orgID, deploymentID, actionID string, resume []byte, heartbeat func([]byte) error) error {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	step := d.Step(actionID)
	if step == nil {
		return fmt.Errorf("%w: deployment %s has no step %s", ErrNotFound, deploymentID, actionID)
	}
	if step.Status == StepSucceeded {
		return nil
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return err
	}
	var action planning.Action
	for _, a := range plan.Actions {
		if a.ID == actionID {
			action = a
		}
	}
	now := r.now().UTC()
	step.StartedAt = &now
	progress := func(status, message string) {
		step.Status, step.Message = status, message
		_ = r.store.UpdateStep(context.WithoutCancel(ctx), orgID, d.ID, *step) //nolint:errcheck // progress is best effort; the step's end is recorded
	}
	if err := r.run(ctx, d, plan, action, step, resume, heartbeat, progress); err != nil {
		finished := r.now().UTC()
		step.Status, step.Message, step.FinishedAt = StepFailed, err.Error(), &finished
		if uerr := r.store.UpdateStep(context.WithoutCancel(ctx), orgID, d.ID, *step); uerr != nil {
			err = errors.Join(err, uerr)
		}
		return err
	}
	finished := r.now().UTC()
	step.Status, step.FinishedAt = StepSucceeded, &finished
	step.Message = doneMessage(step)
	return r.store.UpdateStep(ctx, orgID, d.ID, *step)
}

func doneMessage(s *Step) string {
	switch {
	case s.Op == string(planning.Delete):
		return "Removed"
	case s.URL != "":
		return "Answering at " + s.URL
	default:
		return "Deployed"
	}
}

func (r *Runner) run(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, step *Step,
	resume []byte, heartbeat func([]byte) error, progress func(string, string),
) error {
	exec, ok := r.executors[a.Provider]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoExecutor, a.Provider)
	}
	prov, err := r.providers.Get(a.Provider)
	if err != nil {
		return err
	}
	connID := plan.Connections[prov.Cloud]
	if connID == "" {
		return fmt.Errorf("the plan names no %s connection", prov.Cloud)
	}
	conn, cred, err := r.credentials.Credential(ctx, d.OrgID, connID)
	if err != nil {
		return err
	}
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	var current *planning.Binding
	byName := map[string]*planning.Binding{}
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name == a.ID {
			current = b
		}
		if b.Subject == planning.ServiceSubject {
			byName[b.Name] = b
		}
	}
	job := &Job{
		Deployment: d, Plan: plan, Action: a, Connection: conn, Credential: cred, Binding: current,
		Resume: resume, Checkpoint: heartbeat, Progress: progress,
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return nil, 0, errors.New("only a service is built from a repository")
		},
	}
	if a.Op == planning.Delete {
		if current == nil {
			return nil
		}
		progress(StepDeploying, "Removing "+a.Name)
		if rerr := exec.Remove(ctx, job, current); rerr != nil {
			return rerr
		}
		return r.plans.DeleteBinding(ctx, d.OrgID, current.ID)
	}
	if a.Service != nil {
		repo := a.Service.Repo
		job.Source = func(ctx context.Context) (io.ReadCloser, int64, error) {
			key, serr := sourceFor(d, repo)
			if serr != nil {
				return nil, 0, serr
			}
			return r.sources.Open(ctx, key)
		}
		if job.Env, err = r.env(ctx, d, a, byName); err != nil {
			return err
		}
	}
	out, err := exec.Apply(ctx, job)
	if err != nil {
		return err
	}
	step.URL, step.Image = out.URL, out.Image
	if out.URL != "" && a.Service != nil && a.Service.Public {
		progress(StepVerifying, "Checking "+out.URL+" answers")
		if err := r.verify(ctx, out.URL); err != nil {
			return err
		}
	}
	return r.bind(ctx, d, a, current, out)
}

// env is what a service gets: its resolved variables, then the address of
// each thing it needs. A variable someone set wins over an address
// Loomplane would set, so you can point a service elsewhere.
func (r *Runner) env(ctx context.Context, d *Deployment, a planning.Action, services map[string]*planning.Binding) (map[string]string, error) {
	env, err := r.variables.Values(ctx, d.OrgID, d.ProjectID, d.Environment, a.Service.Service)
	if err != nil {
		return nil, fmt.Errorf("resolve variables: %w", err)
	}
	for _, link := range a.Service.Env {
		if _, set := env[link.Key]; set {
			continue
		}
		if link.Subject != planning.ServiceSubject {
			return nil, fmt.Errorf("%s needs %s, which Loomplane can't provision yet", a.Name, link.From)
		}
		dep := services[link.From]
		if dep == nil || dep.URL == "" {
			return nil, fmt.Errorf("%s needs %s, which has no address yet", a.Name, link.From)
		}
		env[link.Key] = dep.URL
	}
	return env, nil
}

// verify waits for a deployed service to answer with anything but a
// server error.
func (r *Runner) verify(ctx context.Context, url string) error {
	deadline := r.now().Add(r.verifyWait)
	var last string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			return err
		}
		res, err := r.http.Do(req)
		if err == nil {
			_ = res.Body.Close() // only the status matters
			if res.StatusCode < 500 {
				return nil
			}
			last = res.Status
		} else {
			last = err.Error()
		}
		if r.now().After(deadline) {
			return fmt.Errorf("%s didn't answer properly within %s: %s", url, r.verifyWait, strings.TrimSpace(last))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (r *Runner) bind(ctx context.Context, d *Deployment, a planning.Action, current *planning.Binding, out *Outcome) error {
	var cfg any = a.Service
	if a.Subject == planning.ResourceSubject {
		cfg = a.Resource
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode binding: %w", err)
	}
	now := r.now().UTC()
	b := &planning.Binding{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Subject: a.Subject, Name: a.Name,
		Provider: a.Provider, Region: a.Region, Engine: a.Engine, ExternalID: out.ExternalID, Config: raw, URL: out.URL,
		PlanID: d.PlanID, CreatedAt: now, UpdatedAt: now,
	}
	if current != nil {
		b.ID, b.CreatedAt = current.ID, current.CreatedAt
	} else {
		id, err := typeid.Generate("bind")
		if err != nil {
			return fmt.Errorf("generate binding id: %w", err)
		}
		b.ID = id.String()
	}
	return r.plans.PutBinding(ctx, b)
}

// Finish settles a deployment: failed with reason, or succeeded. Steps
// still pending after a failure are marked skipped. A success records the
// commit of each repository it moved as what the environment runs; a
// repository the plan held keeps what it had.
func (r *Runner) Finish(ctx context.Context, orgID, deploymentID, reason string) error {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	status := StatusSucceeded
	if reason != "" {
		status = StatusFailed
		for _, s := range d.Steps {
			if s.Status == StepPending {
				s.Status, s.Message = StepSkipped, "Skipped after an earlier step failed"
				if err := r.store.UpdateStep(ctx, orgID, d.ID, s); err != nil {
					return err
				}
			}
		}
	}
	now := r.now().UTC()
	if err := r.store.Finish(ctx, orgID, d.ID, status, reason, now); err != nil {
		return err
	}
	// A deployment that already failed stays failed, so a late success
	// for it records nothing.
	if reason != "" || d.Status == StatusFailed {
		return nil
	}
	return r.recordDeployed(ctx, d, now)
}

// recordDeployed records the commit of each repository a deployment's plan
// moved as what its environment runs.
func (r *Runner) recordDeployed(ctx context.Context, d *Deployment, at time.Time) error {
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return err
	}
	for _, ps := range plan.Sources {
		if !ps.InScope {
			continue
		}
		if err := r.plans.RecordDeployed(ctx, d.OrgID, &planning.DeployedSource{
			EnvironmentID: d.EnvironmentID, SourceID: ps.SourceID, AnalysisID: ps.AnalysisID, CommitSHA: ps.CommitSHA,
			PlanID: d.PlanID, DeployedAt: at,
		}); err != nil {
			return fmt.Errorf("record what's deployed: %w", err)
		}
	}
	return nil
}
