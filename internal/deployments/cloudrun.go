package deployments

import (
	"archive/tar"
	"compress/gzip"
	"context"
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

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
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

var _ Executor = (*CloudRun)(nil)

// cloudRunState is what a Cloud Run action checkpoints, so a retry picks
// up the same build instead of starting another.
type cloudRunState struct {
	BuildID string `json:"build_id,omitempty"`
	Image   string `json:"image,omitempty"`
}

// Apply implements Executor.
func (c *CloudRun) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	a := job.Action
	if a.Service == nil {
		return nil, fmt.Errorf("%w: cloud run runs services, not %s", ErrNoExecutor, a.Subject)
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	var state cloudRunState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // a missed heartbeat is retried by the next one
	}
	status := StepBuilding
	ctx = progress.With(ctx, func(m string) {
		job.Progress(status, m)
		save()
	})

	if state.Image == "" {
		if state.Image, err = c.build(ctx, s, job, &state, save); err != nil {
			return nil, err
		}
		save()
	}
	status = StepDeploying
	job.Progress(StepDeploying, "Deploying "+state.Image)
	ext, url, err := c.run(ctx, job, state.Image)
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: ext, URL: url, Image: state.Image, Revision: state.Image}, nil
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
		save()
	}
	job.Progress(StepBuilding, "Building on Cloud Build")
	if _, err := s.WaitBuild(ctx, state.BuildID); err != nil {
		return "", err
	}
	return image, nil
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
	b := gcp.Build{Timeout: buildTimeout, Tags: []string{"loomplane"}}
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
	args := []string{"build", image, "--builder", googleBuilder, "--publish"}
	dir := root
	if cfg.Ecosystem == "go" {
		// The Go buildpack builds one main package of a module: name it.
		args = append(args, "--env", "GOOGLE_BUILDABLE=./"+cfg.Source)
	} else {
		dir = root + "/" + cfg.Source
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

// principal is who Ctrl Plane sees acting: the person who approved the
// plan, in its organization.
func principal(job *Job) security.Principal {
	return security.Principal{OrgID: job.Deployment.OrgID, UserID: job.Deployment.StartedBy}
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
	var instances []string
	for _, b := range job.Attach {
		switch {
		case b.Provider == "cloud-sql" && b.ExternalID != "":
			instances = append(instances, b.ExternalID)
		case b.Provider == "memorystore":
			// Memorystore has a private address on the default network.
			labels[cloudrun.LabelVPC] = "default"
		}
	}
	if len(instances) > 0 {
		slices.Sort(instances)
		labels[cloudrun.LabelCloudSQL] = strings.Join(instances, ",")
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
func (c *CloudRun) run(ctx context.Context, job *Job, image string) (externalID, url string, err error) {
	key := c.provider(job)
	gkey, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return "", "", err
	}
	s, err := c.Google.Session(ctx, gkey)
	if err != nil {
		return "", "", err
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
// An instance an action replaces, on another provider, region or
// connection, isn't this one: the runner removes it once this one is up.
// Until then it keeps its slug, so this one takes another name. Nothing
// here deletes an instance, so an old one never goes before what takes
// its place has started. One on another provider key that isn't what the
// action replaces is left alone, and the step fails saying so, since it
// can't be updated through this connection and removing it is a person's
// call.
func currentInstance(ctx context.Context, ctrl CtrlPlane, job *Job, key string) (inst *instance.Instance, name string, err error) {
	if job.Binding != nil && job.Binding.ExternalID != "" {
		iid, perr := id.Parse(job.Binding.ExternalID)
		if perr != nil {
			return nil, "", fmt.Errorf("the binding names no instance: %w", perr)
		}
		if inst, err = ctrl.Instances.Get(ctx, iid); err != nil && !errors.Is(err, ctrlplane.ErrNotFound) {
			return nil, "", err
		}
	}
	if name, err = newInstanceName(ctx, ctrl, job); err != nil {
		return nil, "", err
	}
	if inst == nil {
		found, ferr := ctrl.Instances.GetBySlug(ctx, strings.ToLower(name))
		if ferr != nil && !errors.Is(ferr, ctrlplane.ErrNotFound) {
			return nil, "", ferr
		}
		inst = found
	}
	switch {
	case inst == nil || replaced(job, inst):
		return nil, name, nil
	case inst.ProviderName != key:
		return nil, "", fmt.Errorf("%s already runs as Ctrl Plane instance %s (%s), on %s rather than %s. Loomplane won't remove it before a new one starts, so remove it, then deploy again",
			job.Action.Name, inst.Slug, inst.ID, inst.ProviderName, key)
	}
	return inst, name, nil
}

// replaced reports whether inst is the instance the action replaces.
func replaced(job *Job, inst *instance.Instance) bool {
	return job.Replaces != nil && job.Replaces.ExternalID == inst.ID.String()
}

// newInstanceName is what to call an instance the action creates: its
// name, or when the instance it replaces already has that slug, its name
// with "-2". A move back takes the first name again.
func newInstanceName(ctx context.Context, ctrl CtrlPlane, job *Job) (string, error) {
	name := instanceName(job)
	if job.Replaces == nil || job.Replaces.ExternalID == "" {
		return name, nil
	}
	iid, err := id.Parse(job.Replaces.ExternalID)
	if err != nil {
		// It isn't a Ctrl Plane instance, such as a Vercel project, so
		// it holds no slug.
		return name, nil
	}
	old, err := ctrl.Instances.Get(ctx, iid)
	switch {
	case errors.Is(err, ctrlplane.ErrNotFound):
		return name, nil
	case err != nil:
		return "", fmt.Errorf("read the instance it replaces: %w", err)
	case strings.EqualFold(old.Slug, name):
		return name + "-2", nil
	}
	return name, nil
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
