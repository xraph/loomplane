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
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Render runs services on Render (web services, private services,
// background workers and static sites) and provisions Render Postgres and
// Key Value for them. It's a Loomplane capability adapter, like Vercel's:
// Render builds each service itself, from the repository through its own
// GitHub connection, and Loomplane tells it which commit.
//
// Each service and environment is a Render service of its own, named the
// way a Vercel project is, created with automatic deploys off so that
// only an approved commit ever runs.
type Render struct {
	Client *render.Client
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
	var state renderState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
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
	spec, err := renderSpec(job, cfg, "https://github.com/"+repo)
	if err != nil {
		return nil, err
	}
	job.Progress(StepDeploying, "Setting up the Render service")
	svc, err := r.Client.EnsureService(ctx, key, spec)
	if err != nil {
		return nil, err
	}
	state.ServiceID = svc.ID
	if checkpointErr := checkpointCloudIdentity(job, state); checkpointErr != nil {
		return nil, checkpointErr
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
		return nil, err
	}
	if svc, err = r.Client.GetService(ctx, key, svc.ID); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: svc.ID, URL: renderAddress(spec.Type, svc, cfg.Port), Revision: d.ID}, nil
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

// repositoryFor is the GitHub repository a service is built from, as
// owner/name.
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
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
	job.Progress(StepDeploying, "Making "+spec.Name+" on Render")
	d, err := r.Client.EnsureDatastore(ctx, key, spec)
	if err != nil {
		return nil, err
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

// Remove implements Executor. A datastore goes with its data.
func (r *Render) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	key := renderKey(job)
	if kind, id, ok := datastoreID(b.ExternalID); ok {
		if err := r.Client.DeleteDatastore(ctx, key, kind, id); err != nil {
			return err
		}
		return job.Forget(ctx)
	}
	return r.Client.DeleteService(ctx, key, b.ExternalID)
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
