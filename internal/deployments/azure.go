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
	"regexp"
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
	ctx, r, err := a.prepare(ctx, s, job, cfg)
	if err != nil {
		return nil, err
	}
	// The latest revision gets all of the traffic, said outright, in
	// single revision mode.
	app := azureApp(job, cfg, r.location, r.env.ID, r.pull, r.state.Image, azure.SingleRevision, azure.Latest())
	name := a.appName(s, job)
	live, err := s.GetApp(ctx, s.AppPath(name))
	switch {
	case azure.IsNotFound(err):
		live = nil
	case err != nil:
		return nil, err
	}
	// A canary may have left the app in multiple revisions mode with its
	// traffic pinned. The new revision starts beside what serves, with the
	// traffic as it is, and only takes it once it's ready, so a revision
	// that's inactive, or failed its canary, never serves in between and
	// a deploy that fails leaves the traffic where it was.
	multiple := live != nil && app.Properties.Configuration.Ingress != nil && live.Properties.Configuration.Ingress != nil &&
		strings.EqualFold(live.Properties.Configuration.ActiveRevisionsMode, azure.MultipleRevisions)
	if multiple {
		app.Properties.Configuration.ActiveRevisionsMode = azure.MultipleRevisions
		app.Properties.Configuration.Ingress.Traffic = slices.Clone(live.Properties.Configuration.Ingress.Traffic)
		// A new suffix always makes a revision, so the one that takes over
		// is this deploy's own.
		app.Properties.Template.RevisionSuffix = azureDeploySuffix()
	}
	// The PUT replaces all of the app's secrets. What it would drop shows
	// from the app alone, and usually that's nothing.
	prune := false
	if live != nil {
		appID := cmp.Or(live.ID, s.AppPath(name))
		switch dropped := azureDroppable(live, app); {
		case len(dropped) == 0:
		case azureCanaryLeftovers(live):
			// An earlier canary left a revision that may still read secrets
			// of its own, so they're kept until nothing active does.
			if kerr := azureKeepSecrets(ctx, s, appID, &app); kerr != nil {
				return nil, azureLeftoverRefusal(job, name, kerr)
			}
			prune = true
		default:
			job.Progress(StepDeploying, "This release no longer sets "+strings.Join(dropped, ", ")+", so the deploy removes those secrets from the container app")
		}
	}
	job.Progress(StepDeploying, "Deploying "+r.state.Image)
	// An app can't pull with a new identity until its role propagates, so
	// an app that couldn't pull is tried again, whether Azure refused the
	// PUT, failed the app or failed its revision. Container Apps only
	// makes a revision when the template changes, so each retry names a
	// new one.
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			app.Properties.Template.RevisionSuffix = azureRetrySuffix(attempt)
		}
		deployed, derr := azureDeployApp(ctx, s, job, name, app, &r.state, r.save)
		if derr == nil {
			if multiple {
				if terr := azureTakeOver(ctx, s, r.state.AppID, deployed.Properties.LatestRevisionName); terr != nil {
					return nil, azureLeftoverRefusal(job, name, terr)
				}
			}
			if prune {
				azurePruneNote(ctx, s, job, r.state.AppID)
			}
			return &Outcome{ExternalID: r.state.AppID, URL: s.URL(deployed), Image: r.state.Image, Revision: r.state.Image}, nil
		}
		var pe *azure.PullError
		if r.pull.Identity == "" || !errors.As(derr, &pe) {
			return nil, derr
		}
		if time.Now().Add(azurePullRetry).After(r.deadline) {
			return nil, fmt.Errorf("%w. The pull identity may need a few more minutes before it can pull from the container registry, so retry the deployment", derr)
		}
		job.Progress(StepDeploying, "Waiting for the pull identity's role to reach the container registry")
		if serr := azureSleep(ctx, azurePullRetry); serr != nil {
			return nil, serr
		}
	}
}

// azureRelease is what an action deploys on Azure once its image is
// built: where, how its app pulls, and the image, in the checkpoint that
// keeps it for a retry.
type azureRelease struct {
	location string
	env      *azure.Environment
	pull     azurePull
	// deadline bounds waiting for a new pull identity, the grant and the
	// first pulls after it together.
	deadline time.Time
	state    azureState
	save     func()
}

// prepare sets up what an action's app needs, the resource group, the
// container registry, the pull identity and the Container Apps
// environment, and builds the image with a.build, the one build step,
// unless the checkpoint has it already. Apply and ApplyCandidate both
// start here. The context it returns shows progress as the action's.
func (a *Azure) prepare(ctx context.Context, s *azure.Session, job *Job, cfg *planning.ServiceConfig) (context.Context, *azureRelease, error) {
	r := &azureRelease{location: cmp.Or(job.Action.Region, azureRegion)}
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &r.state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	r.save = func() {
		data, _ := json.Marshal(r.state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)         //nolint:errcheck // the next heartbeat retries it
	}
	status := StepBuilding
	ctx = progress.With(ctx, func(m string) {
		job.Progress(status, m)
		r.save()
	})

	job.Progress(StepBuilding, "Setting up the resource group and container registry")
	if rerr := s.EnsureRegistered(ctx); rerr != nil {
		return nil, nil, rerr
	}
	if gerr := s.EnsureGroup(ctx, r.location); gerr != nil {
		return nil, nil, fmt.Errorf("make resource group %s: %w", s.Credential().ResourceGroup, gerr)
	}
	reg, err := s.EnsureRegistry(ctx, r.location)
	if err != nil {
		return nil, nil, err
	}
	mode, err := s.PullMode(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read whether apps may pull with an identity: %w", err)
	}
	r.pull = azurePull{Server: reg.Properties.LoginServer}
	// One wait covers both the grant and the first pulls after it.
	r.deadline = time.Now().Add(azurePullWait)
	if mode == azure.PullModeIdentity {
		// The role propagates while the build runs.
		if r.pull.Identity, err = a.pullIdentity(ctx, s, job, r.location, reg.ID, r.deadline); err != nil {
			return nil, nil, err
		}
	}
	if r.state.Image == "" {
		if r.state.Image = reusedImage(job); r.state.Image == "" {
			image, berr := a.build(ctx, s, job, cfg, &r.state, r.save)
			if berr != nil {
				return nil, nil, berr
			}
			r.state.Image = image
		}
		r.save()
	}

	status = StepDeploying
	job.Progress(StepDeploying, "Setting up the Container Apps environment in "+r.location)
	if r.env, err = s.EnsureEnvironment(ctx, r.location); err != nil {
		return nil, nil, err
	}
	if r.pull.Identity == "" {
		if r.pull.Login, err = s.RegistryLogin(ctx); err != nil {
			return nil, nil, fmt.Errorf("read the container registry's login: %w", err)
		}
	}
	return ctx, r, nil
}

// azureTakeOver gives a new revision, ready beside what served in multiple
// revisions mode, all of the traffic, then puts the app back in single
// revision mode. A revision that isn't active and ready gets none, and
// the traffic is left as it is.
func azureTakeOver(ctx context.Context, s *azure.Session, appID, revision string) error {
	rev, err := s.GetRevision(ctx, appID, revision)
	if err != nil {
		return fmt.Errorf("read whether revision %s can take the traffic: %w", revision, azureDeny(azureRevisionsRead, err))
	}
	if !rev.Up() {
		return fmt.Errorf("revision %s can't take the traffic (%s), so the traffic was left as it is", revision, rev.Why())
	}
	if err := s.SetTraffic(ctx, appID, azure.MultipleRevisions, []azure.TrafficWeight{{RevisionName: revision, Weight: 100}}); err != nil {
		return fmt.Errorf("give revision %s all of the traffic: %w", revision, err)
	}
	if err := s.SetTraffic(ctx, appID, azure.SingleRevision, azure.Latest()); err != nil {
		return fmt.Errorf("revision %s has all of the traffic, but the app couldn't go back to single revision mode: %w", revision, err)
	}
	return nil
}

// What keeping a canary's leftovers takes.
const (
	azureRevisionsRead = "Microsoft.App/containerApps/revisions/read"
	azureListSecrets   = "Microsoft.App/containerApps/listSecrets/action" //nolint:gosec // G101: an Azure action name, not a credential
)

// azureLeftoverSecretRE matches the name of a canary candidate's own
// secret: the variable's, then the candidate's revision suffix.
var azureLeftoverSecretRE = regexp.MustCompile(`-c[0-9a-f]{8}-\d+(-\d+)?$`)

// azureDeploySuffix is a new revision suffix for a deploy in multiple
// revisions mode.
func azureDeploySuffix() string {
	return "d" + strconv.FormatInt(time.Now().UnixMilli(), 36)
}

// azureDroppable are the secrets on the app that a PUT of release would
// drop: those it has that release doesn't set.
func azureDroppable(live *azure.App, release azure.App) []string {
	var out []string
	for _, sec := range live.Properties.Configuration.Secrets {
		if !slices.ContainsFunc(release.Properties.Configuration.Secrets, func(r azure.Secret) bool { return r.Name == sec.Name }) {
			out = append(out, sec.Name)
		}
	}
	slices.Sort(out)
	return out
}

// azureCanaryLeftovers reports whether an earlier canary left something on
// the app: multiple revisions mode, or a candidate's own secrets.
func azureCanaryLeftovers(live *azure.App) bool {
	if strings.EqualFold(live.Properties.Configuration.ActiveRevisionsMode, azure.MultipleRevisions) {
		return true
	}
	return slices.ContainsFunc(live.Properties.Configuration.Secrets, func(sec azure.Secret) bool { return azureLeftoverSecretRE.MatchString(sec.Name) })
}

// azureDeniedError is a call Azure refused because the principal may not make
// it, with the action it takes.
type azureDeniedError struct {
	action string
	err    error
}

func (e *azureDeniedError) Error() string { return e.err.Error() }
func (e *azureDeniedError) Unwrap() error { return e.err }

// azureDeny is err, marked with the action it takes when Azure refused it
// for want of that action.
func azureDeny(action string, err error) error {
	if azure.IsForbidden(err) {
		return &azureDeniedError{action: action, err: err}
	}
	return err
}

// azureLeftoverRefusal is why a deploy can't go ahead over what an
// earlier canary left. When the principal may not read what it has to,
// it says which action that takes.
func azureLeftoverRefusal(job *Job, app string, err error) error {
	var denied *azureDeniedError
	if !errors.As(err, &denied) {
		return err
	}
	conn := "the connection"
	if job.Connection != nil && job.Connection.Name != "" {
		conn = job.Connection.Name
	}
	return fmt.Errorf("%s can't deploy: an earlier canary left container app %s with revisions or secrets of its own, which this deploy would break unless it reads them first, and the service principal of %s isn't allowed %s. Grant it and check the connection again under Integrations: %w",
		job.Action.Name, app, conn, denied.action, err)
}

// azureKeepSecrets adds to app each secret an active revision of the app
// at appID reads that app doesn't set, with its value as Azure lists it.
// A PUT replaces all of an app's secrets, so without them a revision that
// still runs, such as one a canary promoted, which reads secrets of its
// own, couldn't start a replica.
func azureKeepSecrets(ctx context.Context, s *azure.Session, appID string, app *azure.App) error {
	active, err := s.ActiveRevisions(ctx, appID)
	if err != nil {
		return azureDeny(azureRevisionsRead, err)
	}
	read := map[string]bool{}
	for _, rev := range active {
		for _, name := range rev.SecretsRead() {
			read[name] = true
		}
	}
	for _, sec := range app.Properties.Configuration.Secrets {
		delete(read, sec.Name)
	}
	if len(read) == 0 {
		return nil
	}
	listed, err := s.ListSecrets(ctx, appID)
	if err != nil {
		return azureDeny(azureListSecrets, err)
	}
	for _, sec := range listed {
		if !read[sec.Name] {
			continue
		}
		if sec.Value == "" && sec.KeyVaultURL == "" {
			return fmt.Errorf("azure lists secret %s with no value, so it can't be kept", sec.Name)
		}
		app.Properties.Configuration.Secrets = append(app.Properties.Configuration.Secrets, sec)
		delete(read, sec.Name)
	}
	if len(read) > 0 {
		return fmt.Errorf("azure doesn't list the secrets running revisions read: %s", strings.Join(slices.Sorted(maps.Keys(read)), ", "))
	}
	return nil
}

// azurePruneSecrets drops the app's secrets nothing reads any more: no
// active revision's variables and no registry login.
func azurePruneSecrets(ctx context.Context, s *azure.Session, appID string) error {
	active, err := s.ActiveRevisions(ctx, appID)
	if err != nil {
		return err
	}
	app, err := s.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	read := map[string]bool{}
	for _, rev := range active {
		for _, name := range rev.SecretsRead() {
			read[name] = true
		}
	}
	for _, reg := range app.Properties.Configuration.Registries {
		read[reg.PasswordSecretRef] = true
	}
	listed, err := s.ListSecrets(ctx, appID)
	if err != nil {
		return err
	}
	var keep []azure.Secret
	for _, sec := range listed {
		if read[sec.Name] {
			keep = append(keep, sec)
		}
	}
	if len(keep) == len(listed) {
		return nil
	}
	return s.SetSecrets(ctx, appID, keep)
}

// azurePruneNote prunes the app's secrets once a deploy no longer needs
// the ones it kept for revisions that ran before, and says so when it
// can't. Whatever an active revision reads stays.
func azurePruneNote(ctx context.Context, s *azure.Session, job *Job, appID string) {
	if err := azurePruneSecrets(ctx, s, appID); err != nil {
		job.Progress(StepDeploying, "Secrets nothing reads any more are left on the container app: "+err.Error())
	}
}

// azureDeployApp PUTs the container app and waits until it's ready. A PUT
// Azure refused leaves state's app as it was.
func azureDeployApp(ctx context.Context, s *azure.Session, job *Job, name string, app azure.App, state *azureState, save func()) (*azure.App, error) {
	id, perr := s.PutApp(ctx, name, app)
	if perr != nil {
		return nil, fmt.Errorf("deploy container app %s: %w", name, perr)
	}
	state.AppID = id
	if checkpointErr := checkpointCloudIdentity(job, *state); checkpointErr != nil {
		return nil, checkpointErr
	}
	save()
	return s.WaitApp(ctx, id)
}

// pullIdentity makes the resource group's pull identity and gives it
// AcrPull on the registry, and returns its resource ID. Entra ID can take
// a while to replicate a new identity, and until it has, Azure can't
// assign it a role.
func (a *Azure) pullIdentity(ctx context.Context, s *azure.Session, job *Job, location, registryID string, deadline time.Time) (string, error) {
	job.Progress(StepBuilding, "Setting up the managed identity your apps pull with")
	id, err := s.EnsurePullIdentity(ctx, location)
	if err != nil {
		return "", err
	}
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

// azureRetrySuffix is the revision suffix of a deploy's attempt'th try:
// lower case letters and digits, and new each time, since a suffix an
// app already used is refused.
func azureRetrySuffix(attempt int) string {
	return "pull" + strconv.FormatInt(time.Now().Unix(), 36) + "a" + strconv.Itoa(attempt)
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
// state.Repo once the run starts, and returns the image the run pushed,
// by digest.
func (a *Azure) build(ctx context.Context, s *azure.Session, job *Job, cfg *planning.ServiceConfig, state *azureState, save func()) (string, error) {
	if state.RunID == "" {
		tag, err := imageTag(job)
		if err != nil {
			return "", err
		}
		root, err := sourceRoot(ctx, job)
		if err != nil {
			return "", err
		}
		job.Progress(StepBuilding, "Uploading the source to the container registry")
		src, size, err := job.Source(ctx)
		if err != nil {
			return "", err
		}
		where, err := s.UploadSource(ctx, src, size)
		_ = src.Close() // read-only
		if err != nil {
			return "", err
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
			return "", err
		}
		state.Repo = repo
		save()
	}
	job.Progress(StepBuilding, "Building on ACR Tasks")
	job.Collector(state.RunID, job.LogAttempt()).RecordState(ctx, "", "unavailable", azureLogNote)
	run, err := s.WaitBuild(ctx, state.RunID)
	if err != nil {
		return "", err
	}
	for _, pushed := range run.Properties.OutputImages {
		if pushed.Repository+":"+pushed.Tag == state.Repo {
			return byDigest(pushed.Registry+"/"+state.Repo, pushed.Digest)
		}
	}
	return "", errNoDigest
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

// azureApp is the container app a planned service runs as, in mode with
// its traffic split as traffic says: public services take traffic from
// anywhere, private ones only from apps in their environment. Secret
// variables are app secrets, which Azure never shows again, and so is the
// registry's password when the app pulls with it.
func azureApp(job *Job, cfg *planning.ServiceConfig, location, environment string, pull azurePull, image, mode string, traffic []azure.TrafficWeight) azure.App {
	conf := azure.Configuration{ActiveRevisionsMode: mode}
	var identity *azure.ManagedIdentity
	if pull.Identity != "" {
		conf.Registries = []azure.AppRegistry{{Server: pull.Server, Identity: pull.Identity}}
		identity = &azure.ManagedIdentity{Type: "UserAssigned", UserAssignedIdentities: map[string]struct{}{pull.Identity: {}}}
	} else {
		conf.Registries = []azure.AppRegistry{{Server: pull.Server, Username: pull.Login.Username, PasswordSecretRef: azureRegistrySecret}}
		conf.Secrets = []azure.Secret{{Name: azureRegistrySecret, Value: pull.Login.Password}}
	}
	conf.Ingress = azureAppIngress(cfg, traffic)
	t, secrets := azureTemplate(job, cfg, image)
	conf.Secrets = append(conf.Secrets, secrets...)
	return azure.App{
		Location: location,
		Tags:     map[string]string{"managed-by": "loomplane", "loomplane-project": job.Deployment.ProjectID, "loomplane-environment": job.Deployment.Environment},
		Identity: identity,
		Properties: azure.AppProperties{
			EnvironmentID: environment, Configuration: conf, Template: t,
		},
	}
}

// azureAppIngress is how a planned service takes traffic, split as traffic
// says, or nil: a worker serves nothing, so it gets no ingress, and a
// static site is the public web server in its image.
func azureAppIngress(cfg *planning.ServiceConfig, traffic []azure.TrafficWeight) *azure.Ingress {
	if cfg.Workload == provider.Worker {
		return nil
	}
	port, external := cfg.Port, cfg.Public
	if cfg.Workload == provider.StaticSite {
		port, external = cmp.Or(port, azureStaticPort), true
	}
	if port <= 0 {
		return nil
	}
	return &azure.Ingress{External: external, TargetPort: port, Transport: "auto", Traffic: slices.Clone(traffic)}
}

// azureTemplate is the revision template a planned service runs as, and
// the app secrets its secret variables read.
func azureTemplate(job *Job, cfg *planning.ServiceConfig, image string) (azure.Template, []azure.Secret) {
	var secrets []azure.Secret
	c := azure.Container{Name: azureContainer, Image: image, Resources: azureResources(cfg.Size)}
	for _, k := range slices.Sorted(maps.Keys(job.Env)) {
		if !job.Secrets[k] {
			c.Env = append(c.Env, azure.EnvVar{Name: k, Value: job.Env[k]})
			continue
		}
		secret := azureSecretName(k)
		secrets = append(secrets, azure.Secret{Name: secret, Value: job.Env[k]})
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
	return t, secrets
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
