package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Render runs services on Render (web services, private services,
// background workers and static sites) and provisions Render Postgres and
// Key Value for them. It's a Loomplane capability adapter, like Vercel's:
// Render builds each service itself, from the repository through its own
// connection to the repository's host, and Loomplane tells it which commit.
//
// Each service and environment is a Render service of its own, named the
// way a Vercel project is, created with automatic deploys off so that
// only an approved commit ever runs.
//
// With Environments set, a Loomplane project is one Render project and
// each environment's services and datastores go in a Render environment
// of it with the environment's name. An environment Render won't give a
// project or environment falls back to standalone services and
// datastores, which is how every environment deploys when Environments is
// nil.
type Render struct {
	Client       *render.Client
	Environments *NativeEnvironments
}

var _ Executor = (*Render)(nil)

// renderPlans are Render's instance types, smallest first, with the CPUs
// and MiB of memory each gives.
var renderPlans = []struct {
	name   string
	cpu    float64
	memory int
}{
	{"starter", 0.5, 512}, {"standard", 1, 2048}, {"pro", 2, 4096},
	{"pro_plus", 4, 8192}, {"pro_max", 4, 16384}, {"pro_ultra", 8, 32768},
}

// renderPublish maps the frameworks analysis names to the directory a
// static site's build writes. Anything else is assumed to write dist.
var renderPublish = map[string]string{"next": "out", "gatsby": "public", "create-react-app": "build", "docusaurus": "build"}

// Datastore plans: the smallest of each that keeps its data.
const (
	renderPostgresPlan = "basic_256mb"
	renderKeyValuePlan = "starter"
	renderRegion       = "oregon"
)

type renderState struct {
	ServiceID string `json:"service_id,omitempty"`
	DeployID  string `json:"deploy_id,omitempty"`
	// Mode is ModeNative or ModeFallback once the job knows which; empty
	// when Environments is nil.
	Mode          string `json:"mode,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	// CreatedProject is set when this job made the Render project, with
	// the environment as its first. A fallback removes it, and nothing
	// else, when no other environment uses it.
	CreatedProject bool `json:"created_project,omitempty"`
}

func renderKey(job *Job) render.Key {
	k := render.Key{Value: strings.TrimSpace(string(job.Credential))}
	if job.Connection != nil {
		k.OwnerID = job.Connection.Details["owner_id"]
	}
	return k
}

// RenderName is the Render service or datastore an action runs as in an
// environment.
func RenderName(job *Job) string {
	return strings.ToLower(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
}

// RenderProjectName is the Render project a Loomplane project's native
// environments share.
func RenderProjectName(job *Job) string {
	return "loomplane-" + idSuffix(job.Deployment.ProjectID)
}

// renderCheckpoint reads the job's checkpoint and returns a function that
// saves it.
func renderCheckpoint(job *Job) (state *renderState, save func()) {
	state = &renderState{}
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	return state, func() {
		if job.Checkpoint == nil {
			return
		}
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
}

// Apply implements Executor.
func (r *Render) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Resource != nil {
		return r.applyDatastore(ctx, job)
	}
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: render runs services and provisions datastores, not %s", ErrNoExecutor, job.Action.Subject)
	}
	key := renderKey(job)
	state, save := renderCheckpoint(job)
	ctx = progress.With(ctx, func(m string) {
		job.Progress(StepBuilding, m)
		save()
	})

	repo, err := repositoryFor(job)
	if err != nil {
		return nil, err
	}
	if job.CommitSHA == "" {
		return nil, errors.New("render deploys a commit, and the deployment has none for this service")
	}
	spec, err := renderSpec(job, cfg, renderRepoURL(repo))
	if err != nil {
		return nil, err
	}
	if spec.EnvironmentID, err = r.environment(ctx, job, key, state, save); err != nil {
		return nil, err
	}
	job.Progress(StepDeploying, "Setting up the Render service")
	svc, err := r.Client.EnsureService(ctx, key, spec)
	if err != nil {
		return nil, err
	}
	state.ServiceID = svc.ID
	if checkpointErr := checkpointCloudIdentity(job, *state); checkpointErr != nil {
		return nil, checkpointErr
	}
	if recordErr := r.recordNative(ctx, job, state); recordErr != nil {
		return nil, recordErr
	}
	if state.DeployID == "" {
		d, derr := r.Client.DeployCommit(ctx, key, svc.ID, job.CommitSHA)
		if derr != nil {
			return nil, fmt.Errorf("deploy %s on render: %w", job.CommitSHA, derr)
		}
		state.DeployID = d.ID
		save()
	}
	job.Progress(StepBuilding, "Building on Render")
	job.Collector(state.DeployID, job.LogAttempt()).RecordState(ctx, "", "unavailable", "Render's build-log API cannot isolate one deployment. Progress remains available.")
	d, err := r.Client.WaitDeploy(ctx, key, svc.ID, state.DeployID)
	if err != nil {
		// Render returns the deploy with an error only once it ended in a
		// failed, cancelled or deactivated status.
		if d != nil {
			return nil, &finishedError{err: err}
		}
		return nil, err
	}
	if svc, err = r.Client.GetService(ctx, key, svc.ID); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: svc.ID, URL: renderAddress(spec.Type, svc, cfg.Port), Revision: d.ID}, nil
}

// environment is the Render environment the job's service or datastore
// goes in: the Loomplane environment's, in the Loomplane project's Render
// project, made when it's missing. It's "" when the resource stands on its
// own: without Environments, and when the environment fell back, now or
// in an earlier deploy (a fallback is sticky). Render refusing the project
// or environment falls back here, so the same job carries on standalone.
func (r *Render) environment(ctx context.Context, job *Job, key render.Key, state *renderState, save func()) (string, error) {
	if r.Environments == nil || job.Connection == nil || state.Mode == ModeFallback {
		return "", nil
	}
	rec, err := r.Environments.Record(ctx, job, "render", "")
	if err != nil {
		return "", err
	}
	if rec != nil && rec.Mode == ModeFallback {
		// The checkpointed project and environment belong to the native
		// layout, not the standalone resource.
		*state = renderState{Mode: ModeFallback, ServiceID: state.ServiceID, DeployID: state.DeployID}
		save()
		return "", nil
	}
	envID, err := r.nativeEnvironment(ctx, job, key, state, rec, save)
	var refusal *nativeRefusalError
	if !errors.As(err, &refusal) {
		return envID, err
	}
	if state.DeployID != "" {
		// A deploy of this job is already running in the environment.
		return "", refusal.err
	}
	if rec != nil && rec.EnvironmentRef != "" {
		// The environment's resources already sit in its Render
		// environment. Falling back would make this one standalone and
		// leave the native one running, untracked.
		return "", fmt.Errorf("%s already deploys into its Render environment, so it doesn't fall back to standalone resources: %w",
			job.Deployment.Environment, refusal.err)
	}
	if err := r.undoNative(ctx, job, key, state, save); err != nil {
		return "", err
	}
	if err := r.Environments.FallBack(ctx, job, "render", "", refusal.reason, refusal.permanent); err != nil {
		return "", err
	}
	*state = renderState{Mode: ModeFallback, ServiceID: state.ServiceID, DeployID: state.DeployID}
	save()
	return "", nil
}

// nativeEnvironment ensures the Render project and the environment in it,
// claiming the project before anything else is made, so another
// environment's fallback never removes it from under this one. Render
// refusing either comes back as a *nativeRefusalError.
func (r *Render) nativeEnvironment(ctx context.Context, job *Job, key render.Key, state *renderState, rec *ProviderEnvironment, save func()) (string, error) {
	state.Mode = ModeNative
	name := job.Deployment.Environment
	job.Progress(StepDeploying, "Setting up the Render project")
	project, created, err := r.Client.EnsureProjectCreated(ctx, key, RenderProjectName(job), name)
	if err != nil {
		return "", refusedRenderNative(err)
	}
	if state.ProjectID != "" && state.ProjectID != project.ID {
		return "", errors.New("the Render project identity changed since this deployment started")
	}
	state.ProjectID = project.ID
	if created {
		state.CreatedProject = true
	}
	save()
	if rec == nil || rec.ProjectRef != project.ID {
		if claimErr := r.Environments.Native(ctx, job, "render", "", project.ID, ""); claimErr != nil {
			return "", fmt.Errorf("claim the render project: %w", claimErr)
		}
	}
	env, _, err := r.Client.EnsureEnvironmentCreated(ctx, key, project.ID, name)
	if err != nil {
		return "", refusedRenderNative(err)
	}
	state.EnvironmentID = env.ID
	save()
	return env.ID, nil
}

// refusedRenderNative marks err as a refusal when Render's answer means it
// won't give the workspace a project or environment. Anything else,
// transient errors included, is returned as it is and fails the deploy.
func refusedRenderNative(err error) error {
	if reason, permanent, ok := render.FallbackReason(err); ok {
		return &nativeRefusalError{reason: reason, permanent: permanent, err: err}
	}
	return err
}

// undoNative removes what this job made on Render for the native
// environment Render then refused: the project, with the environment it
// was made with, when no other environment records or claims it. Nothing
// that existed before the job is touched, and no service or datastore is,
// since none is moved before the environment exists.
func (r *Render) undoNative(ctx context.Context, job *Job, key render.Key, state *renderState, save func()) error {
	if !state.CreatedProject {
		return nil
	}
	d := job.Deployment
	others, err := otherNativeUsers(ctx, r.Environments.Store, "render", d.OrgID, state.ProjectID, d.EnvironmentID, "")
	if err != nil {
		return fmt.Errorf("check who uses the render project: %w", err)
	}
	if !others {
		if err := r.Client.DeleteProject(ctx, key, state.ProjectID); err != nil {
			return fmt.Errorf("remove the unused render project: %w", err)
		}
	}
	state.CreatedProject, state.ProjectID = false, ""
	save()
	return nil
}

// recordNative records the environment as native once a resource is in
// it.
func (r *Render) recordNative(ctx context.Context, job *Job, state *renderState) error {
	if state.Mode != ModeNative || state.EnvironmentID == "" {
		return nil
	}
	if err := r.Environments.Native(ctx, job, "render", "", state.ProjectID, state.EnvironmentID); err != nil {
		return fmt.Errorf("record the native render environment: %w", err)
	}
	return nil
}

// renderAddress is where a service answers: its public address, or for a
// private service, its name on the workspace's private network. A worker
// answers nowhere.
func renderAddress(kind string, svc *render.Service, port int) string {
	switch kind {
	case render.WebService, render.StaticSite:
		return svc.ServiceDetails.URL
	case render.PrivateService:
		return "http://" + svc.Slug + ":" + strconv.Itoa(port)
	}
	return ""
}

// renderRepoURL is the web address Render builds a repository from: the
// repository's own page on its host. A key that doesn't parse is a GitHub
// owner/name, as keys always were.
func renderRepoURL(key string) string {
	r, err := repositories.ParseKey(key)
	if err != nil {
		return "https://github.com/" + key
	}
	return r.URL()
}

// repositoryFor is the repository a service is built from, as the source
// stores it: owner/name for GitHub, host/owner/.../name otherwise.
func repositoryFor(job *Job) (string, error) {
	alias := ""
	if job.Action.Service != nil {
		cfg := job.Action.Service
		alias = cfg.Repo
		// Migration plans preserve sources per service, since two services of
		// one repository can be serving different commits after a partial release.
		if pin := cfg.AppliedInputs; pin != nil && pin.ArtifactKey != "" {
			src := pin.Source
			if src == nil || src.Alias != alias || src.CommitSHA != cfg.CommitSHA || src.CommitSHA != job.CommitSHA || src.Repository == "" {
				return "", errors.New("applied repository does not match the service revision")
			}
			return src.Repository, nil
		}
	}
	var found string
	for _, s := range job.Deployment.Sources {
		if s.Alias == alias || (len(job.Deployment.Sources) == 1 && (alias == "" || s.Alias == "")) {
			found = s.Repository
		}
	}
	if found == "" && job.Plan != nil {
		for _, s := range job.Plan.Sources {
			if s.Alias == alias || (len(job.Plan.Sources) == 1 && alias == "") {
				found = s.Repository
			}
		}
	}
	if found == "" {
		return "", errors.New("this target builds from the repository, and the deployment doesn't say which one this service is in")
	}
	return found, nil
}

// renderSpec is the Render service a planned service runs as.
func renderSpec(job *Job, cfg *planning.ServiceConfig, repo string) (render.ServiceSpec, error) {
	spec := render.ServiceSpec{
		Name: RenderName(job), Repo: repo, Region: job.Action.Region, Plan: renderPlan(cfg.Size), Instances: 1,
		Env: map[string]string{},
	}
	if spec.Region == "" {
		spec.Region = renderRegion
	}
	if cfg.Scale != nil {
		spec.Instances = max(1, cfg.Scale.Min)
	}
	for k, v := range job.Env {
		spec.Env[k] = v
	}
	root := strings.Trim(cfg.Source, "/")
	if root == "." {
		root = ""
	}
	switch {
	case cfg.Execution == provider.Static:
		spec.Type, spec.RootDir = render.StaticSite, root
		spec.Build = "npm install && npm run build"
		spec.PublishPath = renderPublish[cfg.Framework]
		if spec.PublishPath == "" {
			spec.PublishPath = "dist"
		}
		return spec, nil
	case cfg.Workload == provider.Worker:
		spec.Type = render.BackgroundWorker
	case cfg.Public:
		spec.Type = render.WebService
	default:
		spec.Type = render.PrivateService
	}
	if spec.Type != render.BackgroundWorker && cfg.Port > 0 {
		if _, set := spec.Env["PORT"]; !set {
			// Render tells a service which port to listen on; tell it the
			// one the service already does.
			spec.Env["PORT"] = strconv.Itoa(cfg.Port)
		}
	}
	if cfg.Image != "" {
		spec.Runtime, spec.Dockerfile, spec.Context = "docker", "./"+cfg.Image, "./"+cfg.ContextDir()
		return spec, nil
	}
	spec.RootDir = root
	switch cfg.Ecosystem {
	case "node":
		spec.Runtime, spec.Build, spec.Start = "node", "npm install", "npm start"
		if cfg.Build != "" {
			spec.Build += " && npm run build"
		}
	case "go":
		// go finds the module from the package's directory, wherever its
		// go.mod is.
		spec.Runtime = "go"
		spec.Build, spec.Start = "go build -tags netgo -ldflags '-s -w' -o app .", "./app"
	case "python":
		spec.Runtime = "python"
		spec.Build = "if [ -f requirements.txt ]; then pip install -r requirements.txt; else pip install .; fi"
		spec.Start = cfg.Start
		if spec.Start == "" {
			spec.Start = "gunicorn main:app"
		}
	default:
		return spec, fmt.Errorf("render has no native runtime for %s; give the service a Dockerfile", cfg.Ecosystem)
	}
	return spec, nil
}

// renderPlan is the smallest instance type that gives what size asks for.
func renderPlan(size *manifest.Size) string {
	if size == nil {
		return renderPlans[0].name
	}
	for _, p := range renderPlans {
		if p.cpu >= size.CPU && p.memory >= size.Memory {
			return p.name
		}
	}
	return renderPlans[len(renderPlans)-1].name
}

func (r *Render) applyDatastore(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	spec := render.DatastoreSpec{Name: RenderName(job), Region: cfg.Region}
	switch cfg.Kind {
	case "postgres":
		spec.Kind, spec.Plan, spec.Version = render.Postgres, renderPostgresPlan, cfg.Version
	case "redis":
		spec.Kind, spec.Plan = render.KeyValue, renderKeyValuePlan
	default:
		return nil, fmt.Errorf("%w: render provisions postgres and redis, not %s", ErrNoExecutor, cfg.Kind)
	}
	if spec.Region == "" {
		spec.Region = renderRegion
	}
	key := renderKey(job)
	state, save := renderCheckpoint(job)
	var err error
	if spec.EnvironmentID, err = r.environment(ctx, job, key, state, save); err != nil {
		return nil, err
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
	job.Progress(StepDeploying, "Making "+spec.Name+" on Render")
	d, err := r.Client.EnsureDatastore(ctx, key, spec)
	if err != nil {
		return nil, err
	}
	if recordErr := r.recordNative(ctx, job, state); recordErr != nil {
		return nil, recordErr
	}
	addr, err := r.Client.Address(ctx, key, spec.Kind, d.ID)
	if err != nil {
		return nil, err
	}
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: spec.Kind + "/" + d.ID, Revision: spec.Kind}, nil
}

// datastoreID splits a datastore binding's ExternalID, kind/id. A
// service's has no kind.
func datastoreID(external string) (kind, id string, ok bool) {
	kind, id, ok = strings.Cut(external, "/")
	if !ok || (kind != render.Postgres && kind != render.KeyValue) {
		return "", "", false
	}
	return kind, id, true
}

// Remove implements Executor. A datastore goes with its data. With
// Environments, the Render environment the resource was in goes too once
// it's empty, if it's the one Loomplane recorded for the binding's
// environment, and then the Render project once no other environment uses
// it. A retry after the resource went finishes that cleanup from the
// record.
func (r *Render) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	key := renderKey(job)
	kind, id, datastore := datastoreID(b.ExternalID)
	envID, err := r.resourceEnvironment(ctx, key, b.ExternalID)
	if err != nil {
		return err
	}
	if datastore {
		if err := r.Client.DeleteDatastore(ctx, key, kind, id); err != nil {
			return err
		}
		if err := job.Forget(ctx); err != nil {
			return err
		}
	} else if err := r.Client.DeleteService(ctx, key, b.ExternalID); err != nil {
		return err
	}
	if r.Environments == nil {
		return nil
	}
	return r.removeEnvironment(ctx, job, key, b, envID)
}

// resourceEnvironment is the Render environment a binding's service or
// datastore is in, or "" without Environments or when it's gone.
func (r *Render) resourceEnvironment(ctx context.Context, key render.Key, external string) (string, error) {
	if r.Environments == nil || external == "" {
		return "", nil
	}
	if kind, id, ok := datastoreID(external); ok {
		d, err := r.Client.GetDatastore(ctx, key, kind, id)
		if render.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return d.EnvironmentID, nil
	}
	s, err := r.Client.GetService(ctx, key, external)
	if render.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.EnvironmentID, nil
}

// removeEnvironment deletes the Render environment a removed resource was
// in once it's empty, then the project when no other environment uses it,
// and then the binding's record. envID is "" when the resource was already
// gone, as on a retry, and the record says which environment it was in.
// An environment Loomplane didn't record for the binding's environment is
// never touched, and the record goes last so a retry still knows the
// project.
func (r *Render) removeEnvironment(ctx context.Context, job *Job, key render.Key, b *planning.Binding, envID string) error {
	environmentID := b.EnvironmentID
	if environmentID == "" {
		environmentID = job.Deployment.EnvironmentID
	}
	connID := b.ConnectionID
	if connID == "" && job.Connection != nil {
		connID = job.Connection.ID
	}
	store := r.Environments.Store
	rec, err := store.ProviderEnvironment(ctx, b.OrgID, environmentID, "render", connID, "")
	if err != nil {
		return err
	}
	if rec == nil || rec.Mode != ModeNative {
		return nil
	}
	if envID == "" {
		envID = rec.EnvironmentRef
	}
	if envID == "" || rec.EnvironmentRef != envID {
		return nil
	}
	empty, err := r.Client.EnvironmentEmpty(ctx, key, envID)
	if err != nil && !render.IsNotFound(err) {
		return fmt.Errorf("check the render environment: %w", err)
	}
	if err == nil && !empty {
		return nil
	}
	if delErr := r.Client.DeleteEnvironment(ctx, key, envID); delErr != nil {
		return fmt.Errorf("remove the empty render environment: %w", delErr)
	}
	others, err := otherNativeUsers(ctx, store, "render", b.OrgID, rec.ProjectRef, environmentID, "")
	if err != nil {
		return fmt.Errorf("check who uses the render project: %w", err)
	}
	if !others && rec.ProjectRef != "" {
		if delErr := r.Client.DeleteProject(ctx, key, rec.ProjectRef); delErr != nil {
			return fmt.Errorf("remove the unused render project: %w", delErr)
		}
	}
	if forgetErr := store.DeleteProviderEnvironment(ctx, rec); forgetErr != nil {
		return fmt.Errorf("forget the native render environment: %w", forgetErr)
	}
	return nil
}

// Observe implements Executor. A service is changed when its newest
// deploy isn't the one Loomplane made, as when someone redeploys it in
// Render's dashboard.
func (r *Render) Observe(ctx context.Context, conn *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	key := render.Key{Value: strings.TrimSpace(string(cred))}
	if conn != nil {
		key.OwnerID = conn.Details["owner_id"]
	}
	if kind, id, ok := datastoreID(b.ExternalID); ok {
		if _, err := r.Client.GetDatastore(ctx, key, kind, id); err != nil {
			if render.IsNotFound(err) {
				return &Observation{}, nil
			}
			return nil, err
		}
		return &Observation{Exists: true, Revision: kind}, nil
	}
	if _, err := r.Client.GetService(ctx, key, b.ExternalID); err != nil {
		if render.IsNotFound(err) {
			return &Observation{}, nil
		}
		return nil, err
	}
	d, err := r.Client.LatestDeploy(ctx, key, b.ExternalID)
	if err != nil {
		return nil, err
	}
	o := &Observation{Exists: true}
	if d != nil {
		o.Revision = d.ID
	}
	return o, nil
}
