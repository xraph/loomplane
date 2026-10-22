package deployments

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Azure builds a service with ACR Tasks, from its Dockerfile or with
// Google's buildpacks when it has none, and runs it on Azure Container
// Apps. It's a Loomplane capability adapter, like Render's:
// Loomplane uploads the approved commit's source to the connection's
// container registry, ACR Tasks builds and pushes it there, and the
// container app runs that image. Nothing from the repository runs on a
// Loomplane machine.
//
// Everything lives in the connection's resource group: one registry, one
// Container Apps environment per region, and a container app for each
// service and environment, named the way a Render service is.
type Azure struct {
	Client *azure.Client
}

var (
	_ Executor = (*Azure)(nil)
	_ Locator  = (*Azure)(nil)
)

const (
	azureRegion = "eastus"
	// azureContainer is the container app's one container.
	azureContainer = "main"
	// azureRegistrySecret is the app secret the registry's password is
	// kept in.
	azureRegistrySecret = "registry-password"
	// azureStaticPort is the ingress port of a static site that sets none:
	// the web server in its image.
	azureStaticPort = 80
	azureLogNote    = "ACR Tasks' build logs are under Services, Tasks, Runs in the container registry. Progress remains available."
)

// While a new pull identity's role propagates, its apps can't pull yet.
// Loomplane tries again every azurePullRetry for up to azurePullWait,
// the same for a role assignment Entra ID can't make yet because it
// hasn't replicated the identity. Tests shorten both.
var (
	azurePullWait  = 10 * time.Minute
	azurePullRetry = 30 * time.Second
)

// azureState is what an Azure action checkpoints, so a retry picks up the
// same build instead of starting another.
type azureState struct {
	RunID string `json:"run_id,omitempty"`
	// Repo is the image's repository and tag, without the registry.
	Repo  string `json:"repo,omitempty"`
	Image string `json:"image,omitempty"`
	AppID string `json:"app_id,omitempty"`
}

// AzureName is the container app an action runs as in an environment.
// Container Apps has App Platform's rules for names, 32 characters
// included.
func AzureName(job *Job) string {
	return doName(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
}

func azureSession(ctx context.Context, client *azure.Client, conn *connections.Connection, secret []byte) (*azure.Session, error) {
	var settings map[string]string
	if conn != nil {
		settings = conn.Details
	}
	cred, err := azure.ParseCredential(secret, settings)
	if err != nil {
		return nil, err
	}
	return client.Session(ctx, cred)
}

// Apply implements Executor.
func (a *Azure) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: azure container apps runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	var state azureState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
	status := StepBuilding
	ctx = progress.With(ctx, func(m string) {
		job.Progress(status, m)
		save()
	})
	location := job.Action.Region
	if location == "" {
		location = azureRegion
	}

	job.Progress(StepBuilding, "Setting up the resource group and container registry")
	if rerr := s.EnsureRegistered(ctx); rerr != nil {
		return nil, rerr
	}
	if gerr := s.EnsureGroup(ctx, location); gerr != nil {
		return nil, fmt.Errorf("make resource group %s: %w", s.Credential().ResourceGroup, gerr)
	}
	reg, err := s.EnsureRegistry(ctx, location)
	if err != nil {
		return nil, err
	}
	mode, err := s.PullMode(ctx)
	if err != nil {
		return nil, fmt.Errorf("read whether apps may pull with an identity: %w", err)
	}
	pull := azurePull{Server: reg.Properties.LoginServer}
	if mode == azure.PullModeIdentity {
		// The role propagates while the build runs.
		if pull.Identity, err = a.pullIdentity(ctx, s, job, location, reg.ID); err != nil {
			return nil, err
		}
	}
	if state.Image == "" {
		if berr := a.build(ctx, s, job, cfg, &state, save); berr != nil {
			return nil, berr
		}
		state.Image = reg.Properties.LoginServer + "/" + state.Repo
		save()
	}

	status = StepDeploying
	job.Progress(StepDeploying, "Setting up the Container Apps environment in "+location)
	env, err := s.EnsureEnvironment(ctx, location)
	if err != nil {
		return nil, err
	}
	if pull.Identity == "" {
		if pull.Login, err = s.RegistryLogin(ctx); err != nil {
			return nil, fmt.Errorf("read the container registry's login: %w", err)
		}
	}
	app := azureApp(job, cfg, location, env.ID, pull, state.Image)
	name := a.appName(s, job)
	job.Progress(StepDeploying, "Deploying "+state.Image)
	// An app can't pull with a new identity until its role propagates, so
	// a revision that couldn't pull is tried again, with the same app.
	deadline := time.Now().Add(azurePullWait)
	for {
		if state.AppID, err = s.PutApp(ctx, name, app); err != nil {
			return nil, fmt.Errorf("deploy container app %s: %w", name, err)
		}
		if checkpointErr := checkpointCloudIdentity(job, state); checkpointErr != nil {
			return nil, checkpointErr
		}
		save()
		live, werr := s.WaitApp(ctx, state.AppID)
		if werr == nil {
			return &Outcome{ExternalID: state.AppID, URL: s.URL(live), Image: state.Image, Revision: state.Image}, nil
		}
		var pe *azure.PullError
		if pull.Identity == "" || !errors.As(werr, &pe) {
			return nil, werr
		}
		if time.Now().Add(azurePullRetry).After(deadline) {
			return nil, fmt.Errorf("%w. The pull identity may need a few more minutes before it can pull from the container registry, so retry the deployment", werr)
		}
		job.Progress(StepDeploying, "Waiting for the pull identity's role to reach the container registry")
		if serr := azureSleep(ctx, azurePullRetry); serr != nil {
			return nil, serr
		}
	}
}

// pullIdentity makes the resource group's pull identity and gives it
// AcrPull on the registry, and returns its resource ID. Entra ID can take
// a while to replicate a new identity, and until it has, Azure can't
// assign it a role.
func (a *Azure) pullIdentity(ctx context.Context, s *azure.Session, job *Job, location, registryID string) (string, error) {
	job.Progress(StepBuilding, "Setting up the managed identity your apps pull with")
	id, err := s.EnsurePullIdentity(ctx, location)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(azurePullWait)
	for {
		_, gerr := s.GrantPull(ctx, registryID, id.PrincipalID)
		var ae *azure.APIError
		if gerr == nil {
			return id.ID, nil
		}
		if !errors.As(gerr, &ae) || !strings.EqualFold(ae.Code, "PrincipalNotFound") {
			return "", gerr
		}
		if time.Now().Add(azurePullRetry).After(deadline) {
			return "", fmt.Errorf("%w. Entra ID may need a few more minutes to know the new pull identity, so retry the deployment", gerr)
		}
		job.Progress(StepBuilding, "Waiting for Entra ID to know the pull identity")
		if serr := azureSleep(ctx, azurePullRetry); serr != nil {
			return "", serr
		}
	}
}

// azureSleep waits for d, or until ctx is done.
func azureSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// azurePull is how an app pulls from the registry at Server: as the
// managed identity Identity names when it's set, else as the registry's
// admin user.
type azurePull struct {
	Server, Identity string
	Login            *azure.RegistryLogin
}

// build uploads the source and builds it on ACR Tasks, setting
// state.Repo once the run succeeds.
func (a *Azure) build(ctx context.Context, s *azure.Session, job *Job, cfg *planning.ServiceConfig, state *azureState, save func()) error {
	if state.RunID == "" {
		tag, err := imageTag(job)
		if err != nil {
			return err
		}
		root, err := sourceRoot(ctx, job)
		if err != nil {
			return err
		}
		job.Progress(StepBuilding, "Uploading the source to the container registry")
		src, size, err := job.Source(ctx)
		if err != nil {
			return err
		}
		where, err := s.UploadSource(ctx, src, size)
		_ = src.Close() // read-only
		if err != nil {
			return err
		}
		repo := imageName(job) + ":" + tag
		req := azure.BuildRequest{Source: where, Image: repo}
		// The tarball's one top-level directory is in every path.
		if cfg.Image == "" {
			dir, env := packPlan(cfg)
			req.Pack = &azure.PackBuild{Image: packBuilder, Builder: googleBuilder, Path: path.Join(root, dir), Env: env}
		} else {
			dir := cfg.ContextDir()
			req.Dockerfile, req.Context = path.Join(root, dir, cfg.DockerfileIn(dir)), path.Join(root, dir)
		}
		state.RunID, err = s.StartBuild(ctx, req)
		if err != nil {
			return err
		}
		state.Repo = repo
		save()
	}
	job.Progress(StepBuilding, "Building on ACR Tasks")
	job.Collector(state.RunID, job.LogAttempt()).RecordState(ctx, "", "unavailable", azureLogNote)
	return s.WaitBuild(ctx, state.RunID)
}

// appName is the container app to deploy to: the bound one, or the
// action's name. One the action replaces in the same resource group, as
// after a move to another region, keeps that name, so this one takes
// another.
func (a *Azure) appName(s *azure.Session, job *Job) string {
	if job.Binding != nil && s.Owns(job.Binding.ExternalID) {
		return path.Base(job.Binding.ExternalID)
	}
	name := AzureName(job)
	if job.Replaces != nil && strings.EqualFold(job.Replaces.ExternalID, s.AppPath(name)) {
		name = doName(name + "-2")
	}
	return name
}

// azureApp is the container app a planned service runs as: public
// services take traffic from anywhere, private ones only from apps in
// their environment. Secret variables are app secrets, which Azure never
// shows again, and so is the registry's password when the app pulls
// with it.
func azureApp(job *Job, cfg *planning.ServiceConfig, location, environment string, pull azurePull, image string) azure.App {
	conf := azure.Configuration{ActiveRevisionsMode: "Single"}
	var identity *azure.ManagedIdentity
	if pull.Identity != "" {
		conf.Registries = []azure.AppRegistry{{Server: pull.Server, Identity: pull.Identity}}
		identity = &azure.ManagedIdentity{Type: "UserAssigned", UserAssignedIdentities: map[string]struct{}{pull.Identity: {}}}
	} else {
		conf.Registries = []azure.AppRegistry{{Server: pull.Server, Username: pull.Login.Username, PasswordSecretRef: azureRegistrySecret}}
		conf.Secrets = []azure.Secret{{Name: azureRegistrySecret, Value: pull.Login.Password}}
	}
	// A worker serves nothing, so it gets no ingress. A static site is the
	// public web server in its image.
	if cfg.Workload != provider.Worker {
		port, external := cfg.Port, cfg.Public
		if cfg.Workload == provider.StaticSite {
			port, external = cmp.Or(port, azureStaticPort), true
		}
		if port > 0 {
			conf.Ingress = &azure.Ingress{External: external, TargetPort: port, Transport: "auto"}
		}
	}
	c := azure.Container{Name: azureContainer, Image: image, Resources: azureResources(cfg.Size)}
	for _, k := range slices.Sorted(maps.Keys(job.Env)) {
		if !job.Secrets[k] {
			c.Env = append(c.Env, azure.EnvVar{Name: k, Value: job.Env[k]})
			continue
		}
		secret := azureSecretName(k)
		conf.Secrets = append(conf.Secrets, azure.Secret{Name: secret, Value: job.Env[k]})
		c.Env = append(c.Env, azure.EnvVar{Name: k, SecretRef: secret})
	}
	t := azure.Template{Containers: []azure.Container{c}}
	switch {
	case cfg.Workload == provider.Worker:
		// Nothing calls a worker to wake it, so it never scales to zero.
		scale := &azure.Scale{MinReplicas: 1}
		if cfg.Scale != nil {
			scale.MinReplicas, scale.MaxReplicas = max(1, cfg.Scale.Min), cfg.Scale.Max
		}
		t.Scale = scale
	case cfg.Scale != nil:
		t.Scale = &azure.Scale{MinReplicas: cfg.Scale.Min, MaxReplicas: cfg.Scale.Max}
	}
	return azure.App{
		Location: location,
		Tags:     map[string]string{"managed-by": "loomplane", "loomplane-project": job.Deployment.ProjectID, "loomplane-environment": job.Deployment.Environment},
		Identity: identity,
		Properties: azure.AppProperties{
			EnvironmentID: environment, Configuration: conf, Template: t,
		},
	}
}

// azureSecretName is the app secret a variable is kept in: lower case
// letters, digits and hyphens, with a hash of the variable's name so two
// names that read alike don't meet.
func azureSecretName(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	sum := sha256.Sum256([]byte(key))
	name := strings.Trim(b.String(), "-")
	if len(name) > 40 {
		name = strings.Trim(name[:40], "-")
	}
	return "env-" + name + "-" + hex.EncodeToString(sum[:])[:6]
}

// azureResources is the smallest of Container Apps' consumption sizes
// that gives what size asks for: a quarter CPU at a time, each with twice
// as many GiB of memory, up to 4 CPUs.
func azureResources(size *manifest.Size) *azure.Resources {
	cpu := 0.5
	if size != nil {
		cpu = 0.25
		for cpu < 4 && (cpu < size.CPU || cpu*2048 < float64(size.Memory)) {
			cpu += 0.25
		}
	}
	return &azure.Resources{CPU: cpu, Memory: strconv.FormatFloat(cpu*2, 'f', -1, 64) + "Gi"}
}

// Remove implements Executor. The registry and the environment stay for
// the resource group's other apps.
func (a *Azure) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID == "" {
		return nil
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return err
	}
	return s.DeleteApp(ctx, b.ExternalID)
}

// Observe implements Executor: what's live is the image the app runs.
func (a *Azure) Observe(ctx context.Context, conn *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	s, err := azureSession(ctx, a.Client, conn, cred)
	if err != nil {
		return nil, err
	}
	app, err := s.GetApp(ctx, b.ExternalID)
	if azure.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: app.Image()}, nil
}

// Locate implements Locator.
func (a *Azure) Locate(b *planning.Binding) string {
	if b.ExternalID == "" {
		return ""
	}
	return fmt.Sprintf("the container app %s in %s", path.Base(b.ExternalID), b.Region)
}

// PendingCloudResource reads the exact container app the operation made.
func (a *Azure) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "app_id")
}
