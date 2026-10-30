package deployments

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// DigitalOcean runs services on DigitalOcean App Platform (services,
// workers and static sites) and provisions Managed Databases for them:
// PostgreSQL, MySQL and Valkey, which speaks the Redis protocol.
//
// Each service and environment is an App Platform app of its own, named
// the way a Render service is, built by App Platform from the repository
// through DigitalOcean's GitHub connection, with deploy on push off.
//
// App Platform builds the newest commit of a branch and can't be told
// which one to build. So Loomplane points it at the branch the project
// reads, starts the deployment itself, and checks the commit App Platform
// reports for every component against the one the plan approved. A
// different commit is cancelled before it goes live. If it somehow went
// live, the app is rolled back to what ran before, or deleted when
// nothing approved ever ran there.
type DigitalOcean struct {
	Client *digitalocean.Client
	// Resolve looks a host up in public DNS, past this machine's own
	// cache. Nil asks Cloudflare's and Google's resolvers.
	Resolve func(ctx context.Context, host string) error
	// DNSWait is how long a new app's address may take to appear in DNS,
	// and DNSPoll how often it's looked up. Zero means the defaults.
	DNSWait, DNSPoll time.Duration
}

var _ Executor = (*DigitalOcean)(nil)

// doSizes are App Platform's instance sizes, smallest first, with the
// CPUs and MiB of memory each gives. Shared CPUs first, then dedicated
// ones for what doesn't fit.
var doSizes = []struct {
	slug   string
	cpu    float64
	memory int
}{
	{"apps-s-1vcpu-0.5gb", 1, 512}, {"apps-s-1vcpu-1gb", 1, 1024}, {"apps-s-1vcpu-2gb", 1, 2048},
	{"apps-s-2vcpu-4gb", 2, 4096}, {"apps-d-2vcpu-8gb", 2, 8192}, {"apps-d-4vcpu-8gb", 4, 8192},
	{"apps-d-4vcpu-16gb", 4, 16384}, {"apps-d-8vcpu-32gb", 8, 32768},
}

// doDatacenters are where a database goes for each App Platform region
// (internal/providers lists them): Managed Databases live in datacenters.
var doDatacenters = map[string]string{
	"nyc": "nyc3", "sfo": "sfo3", "tor": "tor1", "atl": "atl1", "ams": "ams3",
	"fra": "fra1", "lon": "lon1", "blr": "blr1", "sgp": "sgp1", "syd": "syd1",
}

const (
	doRegion       = "nyc"
	doBranch       = "main"
	doDatabaseSize = "db-s-1vcpu-1gb"
	// doNameMax is App Platform's limit on app and component names.
	doNameMax = 32
	// doDatabasePrefix marks a binding's ExternalID as a database.
	doDatabasePrefix = "db/"
)

type doState struct {
	AppID        string `json:"app_id,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	// Previous is the deployment that was live before this one, to roll
	// back to.
	Previous string `json:"previous,omitempty"`
}

// DigitalOceanName is the App Platform app or Managed Database an action
// runs as in an environment, cut to App Platform's 32 characters with a
// hash of the whole name when it's longer, so two long names don't meet.
func DigitalOceanName(job *Job) string {
	return doName(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
}

// doName makes s a name App Platform accepts: lower case letters, digits
// and hyphens, starting with a letter and ending with a letter or digit.
func doName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "lp-" + name
	}
	if len(name) > doNameMax {
		sum := sha256.Sum256([]byte(s))
		tail := "-" + hex.EncodeToString(sum[:])[:6]
		name = strings.TrimRight(name[:doNameMax-len(tail)], "-") + tail
	}
	return strings.TrimRight(name, "-")
}

// doBranchFor is the branch the project reads the service's repository
// from. A source that leaves it to the repository's default branch is
// built from main; if the default is another branch, the commit check
// catches it and says to set the source's branch.
func doBranchFor(job *Job) string {
	alias := ""
	if job.Action.Service != nil {
		alias = job.Action.Service.Repo
	}
	if job.Plan != nil {
		for _, s := range job.Plan.Sources {
			if (s.Alias == alias || (len(job.Plan.Sources) == 1 && alias == "")) && strings.TrimSpace(s.Ref) != "" {
				return strings.TrimSpace(s.Ref)
			}
		}
	}
	return doBranch
}

// Apply implements Executor.
func (d *DigitalOcean) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Resource != nil {
		return d.applyDatabase(ctx, job)
	}
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: digitalocean runs services and provisions databases, not %s", ErrNoExecutor, job.Action.Subject)
	}
	token := strings.TrimSpace(string(job.Credential))
	var state doState
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
		return nil, errors.New("app platform deploys a commit, and the deployment has none for this service")
	}
	if cfg.Artifact != nil && cfg.Artifact.Revision != "" && job.Binding != nil && job.Binding.ExternalID != "" {
		return d.rollBack(ctx, token, job, cfg, &state, save)
	}
	branch := doBranchFor(job)
	spec := doAppSpec(job, cfg, repo, branch)

	if state.DeploymentID == "" {
		job.Progress(StepDeploying, "Setting up the App Platform app")
		aerr := d.ensureApp(ctx, token, spec, func(ctx context.Context, app *digitalocean.App) error {
			state.AppID = app.ID
			if checkpointErr := checkpointCloudIdentity(job, state); checkpointErr != nil {
				return checkpointErr
			}
			if app.ActiveDeployment != nil {
				state.Previous = app.ActiveDeployment.ID
			}
			dep, derr := d.Client.CreateDeployment(ctx, token, app.ID)
			if derr != nil {
				return fmt.Errorf("deploy %s on app platform: %w", spec.Name, derr)
			}
			state.DeploymentID = dep.ID
			save()
			return nil
		})
		if aerr != nil {
			return nil, aerr
		}
	}
	job.Progress(StepBuilding, "Building "+branch+" on App Platform")
	job.Collector(state.DeploymentID, job.LogAttempt()).RecordState(ctx, "", "unavailable", "App Platform's build logs are on the app's Activity tab in DigitalOcean. Progress remains available.")
	dep, err := d.Client.WaitDeployment(ctx, token, state.AppID, state.DeploymentID, job.CommitSHA)
	var mm *digitalocean.CommitMismatchError
	if errors.As(err, &mm) {
		refused := d.refuse(ctx, token, job, state, mm, branch)
		// A retry starts a deployment of its own and checks that one, so
		// it never waits on the one refused here.
		state.DeploymentID = ""
		save()
		return nil, refused
	}
	if err != nil {
		return nil, err
	}
	app, err := d.Client.GetApp(ctx, token, state.AppID)
	if err != nil {
		return nil, err
	}
	url := doAddress(cfg, app)
	if err := d.waitForDNS(ctx, job, url); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: app.ID, URL: url, Revision: dep.ID}, nil
}

// rollBack puts the app back on the deployment a rollback plan restores,
// which App Platform still holds, so nothing builds. The rollback is a
// deployment of its own, waited on and checked against the restored
// commit like any other.
func (d *DigitalOcean) rollBack(ctx context.Context, token string, job *Job, cfg *planning.ServiceConfig, state *doState, save func()) (*Outcome, error) {
	app := job.Binding.ExternalID
	if state.AppID != app || state.DeploymentID == "" {
		job.Progress(StepDeploying, "Rolling App Platform back to deployment "+cfg.Artifact.Revision)
		dep, err := d.Client.Rollback(ctx, token, app, cfg.Artifact.Revision)
		if err != nil {
			return nil, fmt.Errorf("roll %s back on app platform: %w", job.Action.Name, err)
		}
		*state = doState{AppID: app, DeploymentID: dep.ID}
		save()
	}
	dep, err := d.Client.WaitDeployment(ctx, token, app, state.DeploymentID, job.CommitSHA)
	if err != nil {
		return nil, fmt.Errorf("roll %s back on app platform: %w", job.Action.Name, err)
	}
	live, err := d.Client.GetApp(ctx, token, app)
	if err != nil {
		return nil, err
	}
	url := doAddress(cfg, live)
	if err := d.waitForDNS(ctx, job, url); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: live.ID, URL: url, Revision: dep.ID}, nil
}

// doPublicResolvers are asked directly, so a name looked up too early
// isn't remembered as missing by this machine's resolver.
var doPublicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}

const (
	doDNSWait = 10 * time.Minute
	doDNSPoll = 5 * time.Second
)

// waitForDNS waits until a new app's address is in public DNS. App
// Platform reports a deployment live a few minutes before its
// ondigitalocean.app name resolves, and looking it up in that gap makes
// this machine cache "no such host", so the runner's check that the
// service answers fails while the app is up. Only a name that public DNS
// says doesn't exist yet is waited for: when public DNS can't be reached,
// the runner's own check decides.
func (d *DigitalOcean) waitForDNS(ctx context.Context, job *Job, rawURL string) error {
	u, err := neturl.Parse(rawURL)
	if rawURL == "" || err != nil || u.Hostname() == "" || u.Hostname() == "localhost" || net.ParseIP(u.Hostname()) != nil {
		return nil
	}
	host := u.Hostname()
	resolve, wait, poll := d.Resolve, cmp.Or(d.DNSWait, doDNSWait), cmp.Or(d.DNSPoll, doDNSPoll)
	if resolve == nil {
		resolve = resolvePublicly
	}
	deadline := time.Now().Add(wait)
	for {
		err := resolve(ctx, host)
		var dnsErr *net.DNSError
		if err == nil || !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the app is deployed, but %s isn't in DNS after %s. Retry this step once it resolves", host, wait)
		}
		job.Progress(StepDeploying, "Waiting for DigitalOcean to publish "+host)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// resolvePublicly looks host up through the first public resolver that
// answers.
func resolvePublicly(ctx context.Context, host string) error {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		var last error
		for _, server := range doPublicResolvers {
			conn, err := dialer.DialContext(ctx, network, server)
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := r.LookupHost(ctx, host)
	return err
}

// ensureApp creates the app, or updates the one already called spec.Name
// to match spec, then calls deploy to start the deployment. Either can
// start a deployment of App Platform's own, which is cancelled: the only
// one that runs is the one deploy starts and Apply checks. An existing app
// is held from reading its domains until deploy returns, so a hostname
// change can't write the spec in between and lose either change.
func (d *DigitalOcean) ensureApp(ctx context.Context, token string, spec digitalocean.AppSpec, deploy func(context.Context, *digitalocean.App) error) error {
	found, err := d.Client.FindApp(ctx, token, spec.Name)
	if err != nil {
		return err
	}
	if found == nil {
		// No route has a hostname on an app that didn't exist.
		app, createErr := d.Client.CreateApp(ctx, token, spec)
		if createErr != nil {
			return createErr
		}
		return d.startDeployment(ctx, token, spec.Name, app, deploy)
	}
	return d.Client.WithApp(ctx, found.ID, func(ctx context.Context) error {
		// Domains belong to the hostnames routes attach, not to the
		// deploy, so an update carries over whatever the app has. They're
		// read from the app itself: the list FindApp reads needn't carry
		// them.
		current, getErr := d.Client.GetApp(ctx, token, found.ID)
		if getErr != nil {
			return getErr
		}
		spec.Domains = current.Spec.Domains
		app, updateErr := d.Client.UpdateApp(ctx, token, found.ID, spec)
		if updateErr != nil {
			return updateErr
		}
		return d.startDeployment(ctx, token, spec.Name, app, deploy)
	})
}

// startDeployment cancels the deployments App Platform started of its own
// for a create or update, then calls deploy.
func (d *DigitalOcean) startDeployment(ctx context.Context, token, name string, app *digitalocean.App, deploy func(context.Context, *digitalocean.App) error) error {
	for _, dep := range []*digitalocean.Deployment{app.PendingDeployment, app.InProgressDeployment} {
		if dep != nil && dep.ID != "" {
			if err := d.Client.CancelDeployment(ctx, token, app.ID, dep.ID); err != nil {
				return fmt.Errorf("cancel app platform's own deployment of %s: %w", name, err)
			}
		}
	}
	return deploy(ctx, app)
}

// refuse stops a deployment built from the wrong commit: cancelled before
// it's live, or, if it got there, rolled back to what ran before, or
// deleted when it's an app Loomplane only just made.
func (d *DigitalOcean) refuse(ctx context.Context, token string, job *Job, state doState, mm *digitalocean.CommitMismatchError, branch string) error {
	hint := fmt.Sprintf("App Platform builds the newest commit of %s. Push nothing to it while a plan for it is approved, or set the repository's branch in Loomplane if the commit is on another one.", branch)
	if mm.Deployment == nil || mm.Deployment.Phase != digitalocean.PhaseActive {
		if err := d.Client.CancelDeployment(ctx, token, state.AppID, state.DeploymentID); err != nil {
			return fmt.Errorf("%w. Cancelling it failed too: %w", mm, err)
		}
		return fmt.Errorf("%w. Loomplane cancelled it before it went live. %s", mm, hint)
	}
	switch {
	case state.Previous != "":
		if _, err := d.Client.Rollback(ctx, token, state.AppID, state.Previous); err != nil {
			return fmt.Errorf("%w. It went live, and rolling back failed: %w. Roll the app back in DigitalOcean", mm, err)
		}
		return fmt.Errorf("%w. It went live, so Loomplane rolled the app back to what ran before. %s", mm, hint)
	case job.Binding == nil:
		if err := d.Client.DeleteApp(ctx, token, state.AppID); err != nil {
			return fmt.Errorf("%w. It went live on a new app, and deleting it failed: %w. Delete the app in DigitalOcean", mm, err)
		}
		return fmt.Errorf("%w. It went live on a new app, so Loomplane deleted it. %s", mm, hint)
	}
	return fmt.Errorf("%w. It went live and there's nothing to roll back to; redeploy an approved plan. %s", mm, hint)
}

// doAddress is where a service answers: its app's public URL. A private
// service is reachable only inside its own app, so from nowhere Loomplane
// deploys, and a worker answers nowhere.
func doAddress(cfg *planning.ServiceConfig, app *digitalocean.App) string {
	if cfg.Workload == provider.Worker || (!cfg.Public && cfg.Execution != provider.Static) {
		return ""
	}
	return app.LiveURL
}

// doAppSpec is the App Platform app a planned service runs as.
func doAppSpec(job *Job, cfg *planning.ServiceConfig, repo, branch string) digitalocean.AppSpec {
	region := job.Action.Region
	if region == "" {
		region = doRegion
	}
	spec := digitalocean.AppSpec{Name: DigitalOceanName(job), Region: region}
	comp := digitalocean.Component{
		Name:   doName(job.Action.Name),
		GitHub: &digitalocean.GitHubSource{Repo: repo, Branch: branch},
	}
	root := strings.Trim(cfg.Source, "/")
	if root == "." {
		root = ""
	}
	env := map[string]string{}
	for k, v := range job.Env {
		env[k] = v
	}
	if cfg.Execution == provider.Static {
		comp.SourceDir = "/" + root
		comp.OutputDir = renderPublish[cfg.Framework]
		if comp.OutputDir == "" {
			comp.OutputDir = "dist"
		}
		comp.Envs = doEnvs(env, job.Secrets)
		spec.StaticSites = []digitalocean.Component{comp}
		return spec
	}
	comp.InstanceSizeSlug = doSize(cfg.Size)
	comp.InstanceCount = 1
	if cfg.Scale != nil {
		comp.InstanceCount = max(1, cfg.Scale.Min)
	}
	if cfg.Image != "" {
		comp.DockerfilePath, comp.SourceDir = cfg.Image, path.Clean("/"+cfg.ContextDir())
	} else {
		// App Platform's buildpacks build Node, Go, Python, Ruby, PHP and
		// more from the source directory; a start command overrides theirs.
		comp.SourceDir, comp.RunCommand = "/"+root, cfg.Start
	}
	if cfg.Workload == provider.Worker {
		comp.Envs = doEnvs(env, job.Secrets)
		spec.Workers = []digitalocean.Component{comp}
		return spec
	}
	if cfg.Port > 0 {
		if _, set := env["PORT"]; !set {
			env["PORT"] = strconv.Itoa(cfg.Port)
		}
		if cfg.Public {
			comp.HTTPPort = cfg.Port
		} else {
			comp.InternalPorts = []int{cfg.Port}
		}
	}
	comp.Envs = doEnvs(env, job.Secrets)
	spec.Services = []digitalocean.Component{comp}
	return spec
}

// doEnvs are a component's variables, sorted so the spec reads the same on
// every deploy. Secret ones are App Platform secrets, encrypted at rest
// and hidden in its control panel.
func doEnvs(env map[string]string, secret map[string]bool) []digitalocean.Env {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]digitalocean.Env, 0, len(keys))
	for _, k := range keys {
		e := digitalocean.Env{Key: k, Value: env[k], Scope: "RUN_AND_BUILD_TIME", Type: "GENERAL"}
		if secret[k] {
			e.Type = "SECRET"
		}
		out = append(out, e)
	}
	return out
}

// doSize is the smallest instance size that gives what size asks for.
func doSize(size *manifest.Size) string {
	if size == nil {
		return doSizes[0].slug
	}
	for _, s := range doSizes {
		if s.cpu >= size.CPU && s.memory >= size.Memory {
			return s.slug
		}
	}
	return doSizes[len(doSizes)-1].slug
}

func (d *DigitalOcean) applyDatabase(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	spec := digitalocean.DatabaseSpec{Name: DigitalOceanName(job), Size: doDatabaseSize, NumNodes: 1, Version: cfg.Version}
	switch cfg.Kind {
	case "postgres":
		spec.Engine = digitalocean.Postgres
	case "mysql":
		spec.Engine = digitalocean.MySQL
	case "redis":
		// DigitalOcean's Redis-compatible engine is Valkey; its version
		// numbers aren't Redis's.
		spec.Engine, spec.Version = digitalocean.Valkey, ""
	default:
		return nil, fmt.Errorf("%w: digitalocean provisions postgres, mysql and redis, not %s", ErrNoExecutor, cfg.Kind)
	}
	spec.Region = doDatacenter(cfg.Region)
	token := strings.TrimSpace(string(job.Credential))
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
	job.Progress(StepDeploying, "Making "+spec.Name+" on DigitalOcean")
	db, err := d.Client.EnsureDatabase(ctx, token, spec)
	if err != nil {
		return nil, err
	}
	if db.Connection == nil || db.Connection.URI == "" {
		return nil, fmt.Errorf("digitalocean has no address for %s yet", spec.Name)
	}
	if err := job.Keep(ctx, ResourceURL, []byte(db.Connection.URI)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: doDatabasePrefix + db.ID, Revision: spec.Engine}, nil
}

// doDatacenter is where a database for region goes: the datacenter of an
// App Platform region, or a datacenter named outright, such as nyc1.
func doDatacenter(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		region = doRegion
	}
	if dc, ok := doDatacenters[region]; ok {
		return dc
	}
	return region
}

// Remove implements Executor. A database goes with its data.
func (d *DigitalOcean) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	token := strings.TrimSpace(string(job.Credential))
	if id, ok := strings.CutPrefix(b.ExternalID, doDatabasePrefix); ok {
		if err := d.Client.DeleteDatabase(ctx, token, id); err != nil {
			return err
		}
		return job.Forget(ctx)
	}
	if b.ExternalID == "" {
		return nil
	}
	return d.Client.DeleteApp(ctx, token, b.ExternalID)
}

// Observe implements Executor. An app is changed when its live deployment
// isn't the one Loomplane made, as when someone redeploys it in
// DigitalOcean's control panel.
func (d *DigitalOcean) Observe(ctx context.Context, _ *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	token := strings.TrimSpace(string(cred))
	if id, ok := strings.CutPrefix(b.ExternalID, doDatabasePrefix); ok {
		db, err := d.Client.GetDatabase(ctx, token, id)
		if err != nil {
			if digitalocean.IsNotFound(err) {
				return &Observation{}, nil
			}
			return nil, err
		}
		return &Observation{Exists: true, Revision: db.Engine}, nil
	}
	app, err := d.Client.GetApp(ctx, token, b.ExternalID)
	if err != nil {
		if digitalocean.IsNotFound(err) {
			return &Observation{}, nil
		}
		return nil, err
	}
	o := &Observation{Exists: true}
	if app.ActiveDeployment != nil {
		o.Revision = app.ActiveDeployment.ID
	}
	return o, nil
}
