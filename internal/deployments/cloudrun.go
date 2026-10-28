package deployments

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

const (
	// imageRepository is the Artifact Registry repository Loomplane pushes
	// to, one per region.
	imageRepository = "loomplane"
	buildTimeout    = "1200s"
	mainService     = "main"
	dockerBuilder   = "gcr.io/cloud-builders/docker"
	packBuilder     = "gcr.io/k8s-skaffold/pack"
	googleBuilder   = "gcr.io/buildpacks/builder:latest"
)

// CtrlPlane is the part of Ctrl Plane the Cloud Run executor uses.
type CtrlPlane struct {
	Providers *cpprovider.Registry
	Instances instance.Service
	Deploys   deploy.Service
}

// CloudRun builds a service with Cloud Build into Artifact Registry and
// runs it on Cloud Run, as a Ctrl Plane instance. Nothing from the
// repository runs on a Loomplane machine: Cloud Build runs the build in
// Google's own workers.
type CloudRun struct {
	Google *gcp.Client
	Ctrl   CtrlPlane
	// Credentials reveals a connection's key, for the Ctrl Plane provider,
	// which reads it on every call.
	Credentials Credentials
}

var (
	_ Executor = (*CloudRun)(nil)
	_ Locator  = (*CloudRun)(nil)
)

// cloudRunState is what a Cloud Run action checkpoints, so a retry picks
// up the same build instead of starting another.
type cloudRunState struct {
	PSC        *cloudRunPSCState `json:"psc,omitempty"`
	BuildID    string            `json:"build_id,omitempty"`
	BuildRetry int               `json:"build_retry,omitempty"`
	Image      string            `json:"image,omitempty"`
	LogAttempt string            `json:"log_attempt,omitempty"`
	// Unpinned is the permission the connection lacked to read Image's
	// digest, when Image is a tag for want of it.
	Unpinned string `json:"unpinned,omitempty"`
}

// RetainCheckpointOnRetry preserves build progress and PSC route identities.
func (*CloudRun) RetainCheckpointOnRetry() bool { return true }

// Apply implements Executor.
func (c *CloudRun) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	a := job.Action
	if a.Service == nil {
		return nil, fmt.Errorf("%w: cloud run runs services, not %s", ErrNoExecutor, a.Subject)
	}
	psc, prepareErr := prepareCloudRunPSC(job)
	if prepareErr != nil {
		return nil, prepareErr
	}
	// What's bound has to be this connection's before anything is built.
	if _, err := boundInstance(infrastructure.WithPrincipal(ctx, principal(job)), c.Ctrl, job, c.provider(job)); err != nil {
		return nil, err
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, err
	}
	if psc != nil && key.ProjectID != job.Connection.Details["project_id"] {
		return nil, errCloudRunPSCReview
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ctx, built, err := c.image(ctx, cancel, s, job, psc)
	if err != nil {
		return nil, err
	}
	job = built.job
	job.Progress(StepDeploying, "Deploying "+built.image)
	ext, url, err := c.run(ctx, job, built.image, built.check)
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: ext, URL: url, Image: built.image, Revision: built.image}, nil
}

// session authorizes as the job's connection.
func (c *CloudRun) session(ctx context.Context, job *Job) (*gcp.Session, error) {
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, err
	}
	return c.Google.Session(ctx, key)
}

// cloudRunImage is an image ready to deploy, and what deploying it goes
// on with.
type cloudRunImage struct {
	image string
	// job is the job to deploy with: with a Cloud SQL PSC route, a copy
	// whose environment carries the route.
	job *Job
	// check checks a Cloud SQL PSC route again before each change. It's
	// nil without one.
	check func(context.Context) error
}

// image is the job's image: the one an earlier attempt checkpointed, or
// a new build. The context it returns reports each later wait as
// deploying, and keeps the checkpoint fresh. With a Cloud SQL PSC route,
// a checkpoint that can't be kept cancels ctx through cancel, since the
// route's identities live in it.
func (c *CloudRun) image(ctx context.Context, cancel context.CancelCauseFunc, s *gcp.Session, job *Job, psc *cloudRunPSC) (context.Context, cloudRunImage, error) {
	var state cloudRunState
	if len(job.Resume) > 0 {
		if decodeErr := json.Unmarshal(job.Resume, &state); decodeErr != nil && psc != nil {
			return ctx, cloudRunImage{}, errors.New("cloud sql PSC application checkpoint cannot be read")
		}
	}
	if state.PSC != nil && psc == nil {
		return ctx, cloudRunImage{}, errors.New("cloud sql PSC application checkpoint requires its reviewed transport")
	}
	persist := func() error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if job.Checkpoint == nil {
			return errors.New("deployment checkpoint is unavailable")
		}
		data, encodeErr := json.Marshal(state)
		if encodeErr != nil {
			cancel(encodeErr)
			return encodeErr
		}
		checkpointErr := job.Checkpoint(data)
		if checkpointErr != nil && psc != nil {
			cancel(checkpointErr)
		}
		return checkpointErr
	}
	save := func() {
		if checkpointErr := persist(); checkpointErr != nil && psc != nil {
			cancel(checkpointErr)
		}
	}
	var check func(context.Context) error
	if psc != nil {
		check = func(ctx context.Context) error { return psc.check(ctx, s, job, &state, persist) }
		if checkErr := check(ctx); checkErr != nil {
			return ctx, cloudRunImage{}, checkErr
		}
		prepared := *job
		prepared.Env = psc.env
		job = &prepared
	}
	status := StepBuilding
	ctx = progress.With(ctx, func(m string) {
		job.Progress(status, m)
		save()
	})
	if state.Image == "" {
		image := reusedImage(job)
		if image == "" {
			var err error
			if image, err = c.build(ctx, s, job, &state, save); err != nil {
				return ctx, cloudRunImage{}, err
			}
		}
		state.Image = image
		save()
	}
	if state.Unpinned != "" {
		job.Warn(unpinnedWarning(job.Action.Name, state.Unpinned))
	}
	status = StepDeploying
	return ctx, cloudRunImage{image: state.Image, job: job, check: check}, nil
}

// build runs the service's build on Cloud Build and returns its image.
func (c *CloudRun) build(ctx context.Context, s *gcp.Session, job *Job, state *cloudRunState, save func()) (string, error) {
	a, cfg := job.Action, job.Action.Service
	project := s.Key().ProjectID
	tag, err := imageTag(job)
	if err != nil {
		return "", err
	}
	image := gcp.ImageName(project, a.Region, imageRepository, imageName(job), tag)
	retry := 0
	if job.Deployment != nil {
		retry = job.Deployment.Retries
	}
	if state.BuildID != "" && state.BuildRetry < retry {
		// An explicit retry may replace a confirmed failed build. A running
		// or unreadable operation still owns its checkpoint and must resume.
		previous, readErr := s.GetBuild(ctx, state.BuildID)
		if readErr != nil {
			return "", readErr
		}
		if previous.Done() && previous.Status != "SUCCESS" {
			state.BuildID, state.LogAttempt = "", ""
			state.BuildRetry = retry
			save()
		}
	}
	if state.BuildID == "" {
		job.Progress(StepBuilding, "Uploading the source to Cloud Build")
		bucket := project + "_loomplane"
		if err := s.EnsureBucket(ctx, bucket, a.Region); err != nil {
			return "", fmt.Errorf("create the source bucket: %w", err)
		}
		root, err := sourceRoot(ctx, job)
		if err != nil {
			return "", err
		}
		object := "sources/" + job.SourceID + ".tar.gz"
		src, size, err := job.Source(ctx)
		if err != nil {
			return "", err
		}
		err = s.Upload(ctx, bucket, object, src, size, "application/gzip")
		_ = src.Close() // read-only
		if err != nil {
			return "", fmt.Errorf("upload the source: %w", err)
		}
		if err = s.EnsureRepository(ctx, a.Region, imageRepository); err != nil {
			return "", fmt.Errorf("create the image repository: %w", err)
		}
		b := buildFor(cfg, root, image)
		b.Source.StorageSource.Bucket, b.Source.StorageSource.Object = bucket, object
		if state.BuildID, err = s.StartBuild(ctx, b); err != nil {
			return "", fmt.Errorf("start the build: %w", err)
		}
		state.BuildRetry = retry
		save()
	}
	job.Progress(StepBuilding, "Building on Cloud Build")
	if state.LogAttempt == "" {
		state.LogAttempt = job.LogAttempt()
		save()
	}
	output := startManagedOutput(ctx, job, state.BuildID, state.LogAttempt, func(ctx context.Context, cursor string, _ bool, emit buildlogs.Emit) (string, bool, error) {
		return s.BuildLogs(ctx, state.BuildID, cursor, emit)
	})
	build, buildErr := s.WaitBuild(ctx, state.BuildID)
	output.finish(ctx, build != nil)
	if buildErr != nil {
		return "", buildErr
	}
	for _, pushed := range build.Results.Images {
		if pushed.Name == image && pushed.Digest != "" {
			return byDigest(image, pushed.Digest)
		}
	}
	return pushedImage(ctx, s, a.Region, image, imageName(job), tag, state)
}

// pushedImage is the image a build pushed itself, as pack does, rather
// than through Cloud Build: what Artifact Registry's tag points at. An
// account that may not read tags gets the image by tag, and state says
// why.
func pushedImage(ctx context.Context, s *gcp.Session, region, image, name, tag string, state *cloudRunState) (string, error) {
	digest, err := s.ImageDigest(ctx, region, imageRepository, name, tag)
	if gcp.IsForbidden(err) {
		state.Unpinned = "artifactregistry.tags.get"
		return image, nil
	}
	if gcp.IsNotFound(err) {
		return "", errNoDigest
	}
	if err != nil {
		return "", fmt.Errorf("read the image's digest: %w", err)
	}
	return byDigest(image, digest)
}

// imageName names the service's image, unique within the project.
// imageTag tags a build's image with the first 12 characters of what it
// was built from. Anything shorter isn't a commit or a generated source,
// so the build stops rather than tag every build alike.
func imageTag(job *Job) (string, error) {
	if len(job.SourceID) < 12 {
		return "", fmt.Errorf("%s has no commit to build from", job.Action.Name)
	}
	return job.SourceID[:12], nil
}

func imageName(job *Job) string {
	return strings.ToLower(job.Action.Name + "-" + idSuffix(job.Deployment.ProjectID))
}

func idSuffix(s string) string {
	s = s[strings.LastIndex(s, "_")+1:]
	if len(s) > 8 {
		s = s[len(s)-8:]
	}
	return strings.ToLower(s)
}

// buildFor is the Cloud Build for a service: its Dockerfile, built from
// its build context (the Dockerfile's own directory unless the manifest or
// a Compose file says another), or Google's buildpacks when it has none.
func buildFor(cfg *planning.ServiceConfig, root, image string) gcp.Build {
	b := gcp.Build{Timeout: buildTimeout, Tags: []string{"loomplane"}, Options: map[string]string{"logging": "CLOUD_LOGGING_ONLY"}}
	if cfg.Image != "" {
		ctx := cfg.ContextDir()
		dir := root
		if ctx != "." {
			dir += "/" + ctx
		}
		b.Steps = []gcp.BuildStep{{
			Name: dockerBuilder, Dir: dir,
			Args: []string{"build", "-t", image, "-f", cfg.DockerfileIn(ctx), "."},
		}}
		b.Images = []string{image}
		return b
	}
	path, env := packPlan(cfg)
	args := []string{"build", image, "--builder", googleBuilder, "--publish"}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	dir := root
	if path != "." {
		dir += "/" + path
	}
	args = append(args, "--path", dir)
	b.Steps = []gcp.BuildStep{{Name: packBuilder, Entrypoint: "pack", Args: args}}
	return b
}

// sourceRoot is the one top-level directory of GitHub's tarball, which
// Cloud Build extracts into /workspace.
func sourceRoot(ctx context.Context, job *Job) (string, error) {
	src, _, err := job.Source(ctx)
	if err != nil {
		return "", err
	}
	defer src.Close() // read-only
	zr, err := gzip.NewReader(src)
	if err != nil {
		return "", fmt.Errorf("read the source: %w", err)
	}
	defer zr.Close() // read-only
	h, err := tar.NewReader(zr).Next()
	if err != nil {
		return "", fmt.Errorf("read the source: %w", err)
	}
	root, _, _ := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
	if root == "" {
		return "", errors.New("the source has no top-level directory")
	}
	return root, nil
}

// principal is who Ctrl Plane sees acting: whoever approved the plan, in
// its organization. That's Loomplane automation for a rollback it approved.
func principal(job *Job) security.Principal {
	return security.ActorPrincipal(job.Deployment.OrgID, job.Deployment.StartedBy)
}

// provider registers the connection's Cloud Run provider for the region,
// once, and returns its key.
func (c *CloudRun) provider(job *Job) string {
	key := cloudrun.Key(job.Connection.ID, job.Action.Region)
	if _, err := c.Ctrl.Providers.Get(key); err == nil {
		return key
	}
	orgID, connID := job.Deployment.OrgID, job.Connection.ID
	session := func(ctx context.Context) (*gcp.Session, error) {
		_, cred, err := c.Credentials.Credential(ctx, orgID, connID)
		if err != nil {
			return nil, err
		}
		k, err := gcp.ParseKey(cred)
		if err != nil {
			return nil, err
		}
		return c.Google.Session(ctx, k)
	}
	// Two activities registering at once: the registry keeps one, and
	// both are the same provider.
	c.Ctrl.Providers.Register(key, cloudrun.New(connID, job.Action.Region, session, c.Ctrl.Instances))
	return key
}

// spec is what Ctrl Plane runs: the service without its secrets, which
// stay in Secret Manager. secrets are the labels naming their versions.
func spec(job *Job, image string, secrets map[string]string) (s cpprovider.ServiceSpec, labels map[string]string) {
	cfg := job.Action.Service
	env := map[string]string{}
	for k, v := range job.Env {
		if !job.Secrets[k] {
			env[k] = v
		}
	}
	s = cpprovider.ServiceSpec{Name: mainService, Image: image, Env: env}
	if cfg.Port > 0 {
		s.Ports = []cpprovider.PortSpec{{Container: cfg.Port, Protocol: "http"}}
	}
	labels = map[string]string{cloudrun.LabelPublic: strconv.FormatBool(cfg.Public)}
	if cfg.Scale != nil {
		labels[cloudrun.LabelMinScale] = strconv.Itoa(cfg.Scale.Min)
		labels[cloudrun.LabelMaxScale] = strconv.Itoa(cfg.Scale.Max)
	}
	if cfg.Size != nil {
		s.Resources.MemoryMB = cfg.Size.Memory
		labels[cloudrun.LabelCPU] = strconv.FormatFloat(cfg.Size.CPU, 'f', -1, 64)
	}
	maps.Copy(labels, secrets)
	var instances, pscInstances []string
	for _, b := range job.Attach {
		switch {
		case b.Provider == "cloud-sql" && b.ExternalID != "":
			if _, psc, err := cloudRunPSCBinding(b); err == nil && psc {
				pscInstances = append(pscInstances, b.ExternalID)
			} else {
				instances = append(instances, b.ExternalID)
			}
		case b.Provider == "memorystore":
			// Memorystore has a private address on the default network.
			labels[cloudrun.LabelVPC] = "default"
		}
	}
	if len(pscInstances) > 0 && cfg.CloudSQLNetwork != nil {
		slices.Sort(pscInstances)
		raw, _ := json.Marshal(cloudrun.PSCTransport{Network: *cfg.CloudSQLNetwork, Instances: slices.Compact(pscInstances)}) //nolint:errcheck // contains only strings and string slices
		labels[cloudrun.LabelCloudSQLPSC] = string(raw)
	}
	if len(instances) > 0 {
		slices.Sort(instances)
		labels[cloudrun.LabelCloudSQL] = strings.Join(instances, ",")
	}
	if cfg.PrivateEgress {
		// What it needs over a link is reached through the default
		// network, which the link joins to the other cloud.
		labels[cloudrun.LabelVPC] = "default"
	}
	return s, labels
}

// storeSecrets puts each secret variable in Secret Manager as a new
// version, lets the service's runtime account read it, and returns the
// labels that point the service at those versions.
func (c *CloudRun) storeSecrets(ctx context.Context, s *gcp.Session, job *Job) (map[string]string, error) {
	if len(job.Secrets) == 0 {
		return nil, nil
	}
	job.Progress(StepDeploying, "Storing secrets in Secret Manager")
	account, err := s.RuntimeAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the runtime service account: %w", err)
	}
	labels := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(job.Secrets)) {
		secretID := secretName(job, key)
		if err := s.EnsureSecret(ctx, secretID); err != nil {
			return nil, fmt.Errorf("create secret %s: %w", key, err)
		}
		version, err := s.AddSecretVersion(ctx, secretID, []byte(job.Env[key]))
		if err != nil {
			return nil, fmt.Errorf("store secret %s: %w", key, err)
		}
		if err := s.AllowSecret(ctx, secretID, account); err != nil {
			return nil, fmt.Errorf("let the service read secret %s: %w", key, err)
		}
		labels[cloudrun.LabelSecret+key] = secretID + ":" + version
	}
	return labels, nil
}

// secretName is the Secret Manager secret a service's variable is kept
// in: letters, digits, hyphens and underscores.
func secretName(job *Job, key string) string {
	name := "lp-" + instanceSlug(job) + "-" + key
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, name)
}

// run creates the Ctrl Plane instance the first time, and deploys to it
// after that. Ctrl Plane calls the Cloud Run provider, which waits until
// the revision serves.
func (c *CloudRun) run(ctx context.Context, job *Job, image string, check func(context.Context) error) (externalID, url string, err error) {
	key := c.provider(job)
	gkey, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return "", "", err
	}
	s, err := c.Google.Session(ctx, gkey)
	if err != nil {
		return "", "", err
	}
	if check != nil {
		if checkErr := check(ctx); checkErr != nil {
			return "", "", checkErr
		}
	}
	secrets, err := c.storeSecrets(ctx, s, job)
	if err != nil {
		return "", "", err
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	sp, labels := spec(job, image, secrets)

	inst, name, err := currentInstance(ctx, c.Ctrl, job, key)
	if err != nil {
		return "", "", err
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return "", "", err
		}
	}
	if inst == nil {
		created, err := c.Ctrl.Instances.Create(ctx, instance.CreateRequest{
			Name: name, ProviderName: key, Region: job.Action.Region,
			Services: []cpprovider.ServiceSpec{sp}, Labels: labels,
		})
		if err != nil {
			return "", "", fmt.Errorf("create the cloud run service: %w", err)
		}
		return created.ID.String(), endpoint(created.Endpoints), nil
	}
	if _, err := c.Ctrl.Instances.Update(ctx, inst.ID, instance.UpdateRequest{Services: []cpprovider.ServiceSpec{sp}, Labels: labels}); err != nil {
		return "", "", fmt.Errorf("update the cloud run service: %w", err)
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return "", "", err
		}
	}
	if _, err := c.Ctrl.Deploys.Deploy(ctx, deploy.DeployRequest{
		InstanceID: inst.ID, Services: []cpprovider.ServiceDeploySpec{{Name: mainService, Image: image, Env: sp.Env}},
		CommitSHA: job.CommitSHA, Notes: "Plan " + job.Plan.ID,
	}); err != nil {
		return "", "", fmt.Errorf("deploy to cloud run: %w", err)
	}
	url = endpoint(inst.Endpoints)
	if url == "" && job.Binding != nil {
		url = job.Binding.URL
	}
	return inst.ID.String(), url, nil
}

// currentInstance finds the Ctrl Plane instance an action deploys to: its
// binding's, or one an earlier attempt created before it could record a
// binding. name is what to call the instance when there's none.
//
// Nothing here deletes an instance, so an old one never goes before what
// takes its place has started. An instance an action replaces, on another
// provider, region or connection, isn't this one: the runner removes it
// once this one is up. A bound instance under another provider key, as a
// binding from before bindings recorded their connection can have, can't
// be updated through this connection, and replacing it here would give it
// a new address that nothing the plan keeps or holds would get. So it
// creates nothing, and fails with an ElsewhereError naming the connection
// and region its key names, which the runner records on the binding. The
// next plan then sees the move and replaces it properly. An instance that
// only holds the slug keeps it, and this one takes the next free name.
func currentInstance(ctx context.Context, ctrl CtrlPlane, job *Job, key string) (inst *instance.Instance, name string, err error) {
	if len(job.Action.Routes) > 0 {
		sum := sha256.Sum256([]byte(job.Deployment.ID + "/" + job.Action.ID))
		name = fmt.Sprintf("%s-release-%x", instanceName(job), sum[:8])
		inst, err = ctrl.Instances.GetBySlug(ctx, strings.ToLower(name))
		if errors.Is(err, ctrlplane.ErrNotFound) {
			return nil, name, nil
		}
		if err != nil {
			return nil, "", err
		}
		if inst.ProviderName != key || inst.TenantID != job.Deployment.OrgID || inst.Labels[labelReleaseAttempt] != job.Deployment.ID+"/"+job.Action.ID || replaced(job, inst) {
			return nil, "", errors.New("routed candidate name belongs to another runtime")
		}
		return inst, name, nil
	}

	if inst, err = boundInstance(ctx, ctrl, job, key); err != nil {
		return nil, "", err
	}
	if inst != nil {
		return inst, cmp.Or(inst.Name, instanceName(job)), nil
	}
	return freeInstance(ctx, ctrl, job, key)
}

// boundInstance is the instance the binding names, when it's there and
// under this provider key, or nil. One under another key is an
// ElsewhereError. Executors check it before they build or set anything
// up, so a step that can't deploy creates nothing.
func boundInstance(ctx context.Context, ctrl CtrlPlane, job *Job, key string) (*instance.Instance, error) {
	if job.Binding == nil || job.Binding.ExternalID == "" {
		return nil, nil
	}
	iid, err := id.Parse(job.Binding.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("the binding names no instance: %w", err)
	}
	inst, err := ctrl.Instances.Get(ctx, iid)
	switch {
	case errors.Is(err, ctrlplane.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	case inst.ProviderName != key:
		conn, region := keyPlace(inst.ProviderName)
		return nil, &ElsewhereError{Name: job.Action.Name, Instance: inst.ID.String(), ConnectionID: conn, Region: region, Key: inst.ProviderName}
	}
	return inst, nil
}

// ElsewhereError is a bound instance another connection deployed, which
// the connection a plan deploys with can't update. ConnectionID and Region
// are the ones its provider key names, empty when the key can't be read.
// Recorded says the runner recorded ConnectionID on the binding, so the
// next plan sees the move.
type ElsewhereError struct {
	Name, Instance, ConnectionID, Region, Key string
	Recorded, SameConnection                  bool
}

func (e *ElsewhereError) Error() string {
	at := fmt.Sprintf("%s runs as Ctrl Plane instance %s", e.Name, e.Instance)
	switch {
	case e.ConnectionID == "":
		return fmt.Sprintf("%s under provider %s, which names no connection Loomplane can read, so nothing was deployed. Choose the connection that deployed it for this environment, then deploy again", at, e.Key)
	case e.SameConnection:
		return fmt.Sprintf("%s through this connection, but in region %s, under provider %s, so nothing was deployed. The connection's region may have changed since it was deployed, and planning again wouldn't move it. Set the connection's region back to %s to keep %s where it is",
			at, e.Region, e.Key, e.Region, e.Name)
	case e.Recorded:
		return fmt.Sprintf("%s through connection %s, not the one this plan deploys with, so nothing was deployed. Loomplane has recorded that on its binding: plan again, and the plan replaces %s through this connection, deploys again what uses it, and removes the old one once the new one is up",
			at, e.ConnectionID, e.Name)
	}
	return fmt.Sprintf("%s through connection %s, not the one this plan deploys with, so nothing was deployed. Loomplane couldn't record that on its binding, so a plan can't see the move yet. Retry this step to record it, then plan again, or choose connection %s for this environment to keep %s where it is",
		at, e.ConnectionID, e.ConnectionID, e.Name)
}

// keyPlace reads the connection and region a Cloud Run or ECS provider
// key names, or nothing from a key it doesn't know.
func keyPlace(providerKey string) (connectionID, region string) {
	for _, prefix := range []string{"server.", "kubernetes."} {
		if connection, ok := strings.CutPrefix(providerKey, prefix); ok {
			return connection, ""
		}
	}
	for _, prefix := range []string{cloudrun.KeyPrefix, ecs.KeyPrefix} {
		rest, ok := strings.CutPrefix(providerKey, prefix)
		if !ok {
			continue
		}
		if i := strings.LastIndex(rest, "."); i > 0 && i < len(rest)-1 {
			return rest[:i], rest[i+1:]
		}
	}
	return "", ""
}

// freeInstance finds an instance by the action's name, the way an earlier
// attempt that created it before it could record a binding would have
// named it, or the first free name. An instance by that name under this
// provider key is this action's. One under another key, or the one the
// action replaces, keeps its slug, so this one tries the name with "-2",
// then "-3", and so on. A move back takes the first name again, once
// what held it is gone.
func freeInstance(ctx context.Context, ctrl CtrlPlane, job *Job, key string) (*instance.Instance, string, error) {
	const tries = 20
	base := instanceName(job)
	for n := 1; n <= tries; n++ {
		name := base
		if n > 1 {
			name = base + "-" + strconv.Itoa(n)
		}
		found, err := ctrl.Instances.GetBySlug(ctx, strings.ToLower(name))
		switch {
		case errors.Is(err, ctrlplane.ErrNotFound):
			return nil, name, nil
		case err != nil:
			return nil, "", err
		case found.ProviderName == key && !replaced(job, found):
			return found, name, nil
		}
	}
	return nil, "", fmt.Errorf("every name from %s to %s-%d is taken in Ctrl Plane by an instance that isn't this one", base, base, tries)
}

// replaced reports whether inst is the instance the action replaces.
func replaced(job *Job, inst *instance.Instance) bool {
	return job.Replaces != nil && job.Replaces.ExternalID == inst.ID.String()
}

// instanceName names the Ctrl Plane instance: the service, the
// environment and the project, which Ctrl Plane slugs and keeps unique
// in the organization.
func instanceName(job *Job) string {
	return job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID)
}

func instanceSlug(job *Job) string { return strings.ToLower(instanceName(job)) }

func endpoint(eps []cpprovider.Endpoint) string {
	for _, e := range eps {
		if e.URL != "" {
			return e.URL
		}
	}
	return ""
}

// Remove implements Executor: Ctrl Plane deletes the instance, and the
// provider the Cloud Run service.
func (c *CloudRun) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID == "" {
		return nil
	}
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return fmt.Errorf("the binding names no instance: %w", err)
	}
	if job.Connection != nil {
		job.Action.Region = b.Region
		c.provider(job)
	}
	err = c.Ctrl.Instances.Delete(infrastructure.WithPrincipal(ctx, principal(job)), iid)
	if errors.Is(err, ctrlplane.ErrNotFound) {
		return nil
	}
	return err
}

// Locate implements Locator: the Cloud Run service Ctrl Plane named after
// the instance.
func (c *CloudRun) Locate(b *planning.Binding) string {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("the Cloud Run service %s in %s", cloudrun.ServiceName(iid), b.Region)
}

// Observe implements Executor: it reads the Cloud Run service straight
// from Google, without going through Ctrl Plane, which only knows what it
// was last told.
func (c *CloudRun) Observe(ctx context.Context, _ *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("the binding names no instance: %w", err)
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	svc, err := s.GetService(ctx, b.Region, cloudrun.ServiceName(iid))
	if gcp.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	obs := &Observation{Exists: true}
	if len(svc.Template.Containers) > 0 {
		obs.Revision = svc.Template.Containers[0].Image
	}
	return obs, nil
}
