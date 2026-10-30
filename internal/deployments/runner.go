package deployments

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/telemetry"
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
	// Observe reads what's live for a binding, without changing anything.
	Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error)
}

// retryCheckpointExecutor opts into keeping native operation state when an
// explicit deployment retry starts a new workflow. Its checkpoints must be
// scoped to the original deployment and safe to resume after failure.
type retryCheckpointExecutor interface {
	RetainCheckpointOnRetry() bool
}

// Observation is what's live for a binding.
type Observation struct {
	// Exists is false when it's been deleted.
	Exists bool
	// Revision is what's live: an image, or a Vercel deployment. Empty
	// when the provider can't tell.
	Revision string
}

// Job is one action to carry out, with everything its executor needs.
type Job struct {
	cleanupCheck     func(context.Context) error
	pendingIdentity  *PendingCloudIdentity
	cleanupSyncID    string
	BuildCredentials Credentials
	Logs             LogSink
	Attempt          string
	Deployment       *Deployment
	Plan             *planning.Plan
	Action           planning.Action
	Connection       *connections.Connection
	// Credential is the connection's secret. It's only ever passed to the
	// cloud's client, in memory.
	Credential []byte
	// Env are the variables the service gets: its resolved variables and
	// the addresses of what it needs.
	Env map[string]string
	// Secrets are the keys of Env that are secret. An executor keeps them
	// in the cloud's secret store where it has one, and never passes them
	// through Ctrl Plane, which stores what it's given.
	Secrets map[string]bool
	// Source opens the tarball of the repository the service is built
	// from, at the commit its plan pins for that repository, as GitHub
	// serves it: gzip, with one top-level directory. A gateway's is the
	// program Loomplane generates for it.
	Source func(ctx context.Context) (io.ReadCloser, int64, error)
	// CommitSHA is the commit of the repository the service is built
	// from. It's empty for a resource or a gateway.
	CommitSHA string
	// SourceID names what Source opens, at least 12 characters: the
	// commit, or for a gateway, its generated source. Builds name their
	// uploads and tag their images with it.
	SourceID string
	// Binding is what runs the action now, or nil.
	Binding *planning.Binding
	// Replaces is what runs the action on another provider, in another
	// region or through another connection, which the runner removes once
	// this one is up and bound, through the executor that deployed it.
	// Binding is nil when it's set, since
	// nothing runs the action here yet. An executor whose names could
	// collide with the old one's reads it to choose another.
	Replaces *planning.Binding
	// Resume is what Checkpoint saved in an earlier attempt, such as a
	// build's operation name, or nil.
	Resume []byte
	// Checkpoint saves progress that a retry resumes from, and reports
	// that the activity is alive.
	Checkpoint func(data []byte) error
	// Progress shows what the step is doing.
	Progress func(status, message string)
	// Keep and Recall store and read a resource's credentials, such as a
	// database password, in Vault, scoped to this resource in this
	// environment. A retried step recalls what it kept.
	Keep   func(ctx context.Context, field string, value []byte) error
	Recall func(ctx context.Context, field string) ([]byte, error)
	// Forget deletes everything kept for the resource, once it's gone.
	Forget func(ctx context.Context) error
	// Attach are the resources a service needs, which some providers have
	// to wire to it: a Cloud SQL instance Cloud Run mounts.
	Attach []*planning.Binding

	// files are a catalog component's rendered files, which an owned
	// target keeps in Vault beside its runtime values.
	files []runtimeFile
	// filesRevision is files' digest, keyed by the installation.
	filesRevision string
	// replacing is a Remove of what an action ran as before it moved.
	// What's kept under the action's name is the replacement's then.
	replacing bool
	// warn adds a warning to the step, or is nil.
	warn func(error)
}

// Warn adds a warning to the step an executor runs in, for something that
// went wrong without failing it. A step that can't take warnings drops
// it.
func (j *Job) Warn(err error) {
	if j.warn != nil && err != nil {
		j.warn(err)
	}
}

// ResourceURL is the field a resource executor keeps its address under,
// which the services that need it get.
const ResourceURL = "url"

// linkField is the field of a resource a service's variable reads: its
// address, or one of a composed resource's outputs, which it keeps as
// "output:<name>". A "consumer:<name>" output isn't kept: it's issued to
// the service as its variables are captured.
func linkField(link planning.EnvLink) (string, error) {
	switch {
	case link.Field == "":
		return ResourceURL, nil
	case link.Field == ResourceURL, strings.HasPrefix(link.Field, "output:") && len(link.Field) > len("output:"):
		return link.Field, nil
	}
	return "", fmt.Errorf("%s reads %s of %s, which this Loomplane can't give it yet", link.Key, link.Field, link.From)
}

// Outcome is what an executor did.
type Outcome struct {
	// ExternalID is what the provider calls it: a Ctrl Plane instance or a
	// Vercel project.
	ExternalID     string
	PhysicalID     string
	OwnedResources string
	URL            string
	// Image is the container image a build produced.
	Image string
	// Revision is what went live, which a drift check compares with:
	// the image, or a Vercel deployment.
	Revision string
}

// Plans reads plans and bindings, and records what's deployed.
type Plans interface {
	GetPlan(ctx context.Context, orgID, id string) (*planning.Plan, error)
	ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	PutBinding(ctx context.Context, b *planning.Binding) error
	DeleteBinding(ctx context.Context, orgID, id string) error
	// SetReplaced records what a binding's action ran as before it moved
	// and still needs removing.
	SetReplaced(ctx context.Context, orgID, id string, replaced []planning.Binding) error
	// RecordDeployed records the commit of one repository now deployed in
	// an environment, which the next plan holds the repository at.
	RecordDeployed(ctx context.Context, orgID string, d *planning.DeployedSource) error
	// RecordRelease records, in one transaction, the manifest revision a
	// succeeded deployment's plan was made from and the commit of every
	// repository it moved.
	RecordRelease(ctx context.Context, orgID string, r *planning.DeployedRelease) error
}

// Credentials reveals a connection's credential.
type Credentials interface {
	Credential(ctx context.Context, orgID, id string) (*connections.Connection, []byte, error)
}

// Variables resolves what a service gets in an environment.
type Variables interface {
	Values(ctx context.Context, orgID, projectID, environment, service string) (values map[string]string, secret map[string]bool, err error)
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
	capacity CapacityResolver
	routes   ReleaseRoutes

	cloudSyncAuth  CloudSyncAuthorizer
	cloudProfiles  cloudprofiles.Resolver
	store          Store
	plans          Plans
	credentials    Credentials
	variables      Variables
	sources        Sources
	providers      *provider.Registry
	executors      map[string]Executor
	catalogHooks   CatalogHookMap
	observers      []ReleaseObserver
	authz          Authorizer
	backupPolicies BackupPolicyInstaller
	resources      ResourceSecrets
	health         releasehealth.Reader
	watches        WatchHook
	warn           func(msg string)
	history        LatestRelease
	dbGateways     DatabaseGatewayStore
	dbTrust        func() ([]plane.TrustKey, error)
	publicURL      string
	telemetry      *telemetry.Recorder
	logs           LogSink
	http           *http.Client
	now            func() time.Time
	// verifyWait is how long a deployed service has to answer.
	verifyWait time.Duration
}

// RunnerDeps are what a Runner needs.
type RunnerDeps struct {
	// Authz and BackupPolicies recheck and install reviewed backup defaults.
	Authz          Authorizer
	BackupPolicies BackupPolicyInstaller
	Capacity       CapacityResolver
	Routes         ReleaseRoutes

	CloudSyncAuth CloudSyncAuthorizer
	CloudProfiles cloudprofiles.Resolver
	Logs          LogSink
	Store         Store
	Plans         Plans
	Credentials   Credentials
	Variables     Variables
	Sources       Sources
	Providers     *provider.Registry
	// Executors carry out each provider's actions, by provider name.
	Executors map[string]Executor
	// Resources keeps provisioned resources' credentials. It may be nil
	// when nothing is provisioned.
	Resources ResourceSecrets
	// DatabaseGateways records each environment's database gateway and its
	// routes. It may be nil when no plan deploys one.
	DatabaseGateways DatabaseGatewayStore
	// DatabaseGatewayTrust returns the keys a database gateway trusts the
	// control plane's snapshots with. None means no signing key is
	// configured, and an error that the service holding them isn't
	// available; either way a database gateway can't deploy.
	DatabaseGatewayTrust func() ([]plane.TrustKey, error)
	// PublicURL is Loomplane's own address, which a database gateway polls.
	PublicURL string
	// Telemetry records how deployments go. It may be nil.
	Telemetry *telemetry.Recorder
	// CatalogHooks are composed catalog kinds' hooks, by kind: the same
	// ones the owned targets run.
	CatalogHooks CatalogHookMap
	// Observers hear of each service binding a deployment writes or
	// deletes. What they fail at only warns.
	Observers []ReleaseObserver
	// Health reads a service's health from its cloud's own monitoring,
	// which a canary is judged on. It may be nil when nothing ramps.
	Health releasehealth.Reader
	// Watches arms a post-deploy watch for each deployment that succeeds.
	// It may be nil: then nothing is watched.
	Watches WatchHook
	// Warn logs what the runner carries on past, such as a watch it
	// couldn't prepare. It may be nil.
	Warn func(msg string)
	// History tells a rollback whether the release it undoes is still the
	// environment's newest when it starts. It may be nil: then a rollback
	// starts unchecked.
	History LatestRelease
}

// LatestRelease reads an environment's newest deployment, leaving out
// Cloud sync and catalog plans. *History is the real one.
type LatestRelease interface {
	Latest(ctx context.Context, orgID, environmentID string) (*planning.ReleaseRecord, error)
}

// Release is a service binding a deployment step wrote, or deleted
// (Removed), as a ReleaseObserver hears of it.
type Release struct {
	OrgID, ProjectID, EnvironmentID string
	Binding                         *planning.Binding
	Removed                         bool
}

// ReleaseObserver hears of each service binding a deployment step writes
// or deletes, once that's done. Each call has 10 seconds; an error never
// fails the step, and lands on it as a warning naming the service.
// Resource bindings aren't observed, and neither are writes that only
// record metadata about what's already deployed: adopting a Cloud
// placement, or recording that another connection deployed it (see
// Runner.deployedElsewhere). Each observer gets its own copy of the
// binding.
type ReleaseObserver interface {
	Released(ctx context.Context, r Release) error
}

// NewRunner returns a Runner.
func NewRunner(d RunnerDeps) *Runner {
	return &Runner{
		authz: d.Authz, backupPolicies: d.BackupPolicies,
		capacity: d.Capacity, cloudSyncAuth: d.CloudSyncAuth, routes: d.Routes,
		cloudProfiles: d.CloudProfiles, store: d.Store, plans: d.Plans, credentials: d.Credentials, variables: d.Variables,
		sources: d.Sources, providers: d.Providers, executors: d.Executors, catalogHooks: d.CatalogHooks, observers: d.Observers, resources: d.Resources, health: d.Health, watches: d.Watches, warn: d.Warn, history: d.History, telemetry: d.Telemetry, logs: d.Logs,
		dbGateways: d.DatabaseGateways, dbTrust: d.DatabaseGatewayTrust, publicURL: d.PublicURL,
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
	if guardErr := legacyCapacityGuard(ctx, plan); guardErr != nil {
		return "", nil, guardErr
	}
	if plan.Status != planning.StatusApproved || !plan.Executable {
		return "", nil, nil
	}
	if checkErr := r.checkBackupPolicies(ctx, plan, plan.Actions); checkErr != nil {
		return "", nil, checkErr
	}
	if available, e := r.ReleaseAvailable(ctx, orgID, planID); e != nil {
		return "", nil, e
	} else if !available {
		return "", nil, cloudprofiles.ErrBusy
	}
	if authErr := r.authorizeSync(ctx, plan); authErr != nil {
		return "", nil, authErr
	}
	for _, action := range plan.Actions {
		if action.Service != nil && action.Service.CapacityIntent != nil {
			continue
		}
		if validationErr := r.validateCloudPlacement(ctx, plan, action, nil); validationErr != nil {
			return "", nil, validationErr
		}
		if action.Subject != planning.ServiceSubject && action.Subject != planning.ResourceSubject {
			return "", nil, fmt.Errorf("%w: action subject %s is unavailable", ErrNoExecutor, action.Subject)
		}
	}
	stale, err := r.staleRollback(ctx, plan)
	if err != nil {
		return "", nil, err
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
		if a.Op == planning.Keep && !a.AdoptCloudPlacement {
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
	if stale {
		// Recorded failed, so people and the watch see why it didn't
		// deploy; the workflow gets no deployment to carry out.
		now := r.now().UTC()
		d.Status, d.Error, d.FinishedAt = StatusFailed, StaleRollback, &now
		for i := range d.Steps {
			d.Steps[i].Status, d.Steps[i].Message = StepSkipped, StaleRollback
		}
		if _, err = r.store.CreateDeployment(ctx, d); err != nil {
			return "", nil, err
		}
		return "", nil, nil
	}
	if d.Canary, err = r.newCanary(ctx, plan, d); err != nil {
		return "", nil, err
	}
	if d, err = r.store.CreateDeployment(ctx, d); err != nil {
		return "", nil, err
	}
	// An earlier start refused it; that refusal stands.
	if d.Status == StatusFailed && d.Error == StaleRollback {
		return "", nil, nil
	}
	return d.ID, order, nil
}

// StaleRollback is why a rollback deploys nothing: a newer release
// deployed after it was approved, and it would undo that one too.
const StaleRollback = planning.StaleRollback

// staleRollback reports whether plan is a rollback whose target isn't the
// environment's newest release any more. Approved rollbacks can queue
// behind a Cloud sync or a catalog deployment while another release goes
// first. Its own deployment, from an earlier start, counts as current.
func (r *Runner) staleRollback(ctx context.Context, plan *planning.Plan) (bool, error) {
	if r.history == nil || plan.Kind != planning.KindRollback || plan.Rollback == nil {
		return false, nil
	}
	latest, err := r.history.Latest(ctx, plan.OrgID, plan.EnvironmentID)
	if errors.Is(err, planning.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the environment's newest release: %w", err)
	}
	return latest.DeploymentID != plan.Rollback.TargetDeploymentID && latest.PlanID != plan.ID, nil
}

// startGrace is what a step gets on top of its component's start timeout:
// time to pull the image and apply before the runtime starts waiting.
const startGrace = 15 * time.Minute

// ActionTimeouts are how long each of the deployment's actions that waits
// for a catalog component's start timeout may run: that timeout plus
// startGrace. The workflow gives every other action its usual time, and
// never less than that to one listed here.
func (r *Runner) ActionTimeouts(ctx context.Context, orgID, deploymentID string) (map[string]time.Duration, error) {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return nil, err
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return nil, err
	}
	return actionTimeouts(plan.Actions), nil
}

func actionTimeouts(actions []planning.Action) map[string]time.Duration {
	var out map[string]time.Duration
	for _, a := range actions {
		if a.Resource == nil || a.Resource.Component == nil || a.Resource.Component.Component.StartTimeout <= 0 {
			continue
		}
		if out == nil {
			out = map[string]time.Duration{}
		}
		out[a.ID] = a.Resource.Component.Component.StartTimeout + startGrace
	}
	return out
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
	if d.Status == StatusPaused {
		return ErrCapacityPaused
	}
	if d.Status == StatusCancelled {
		return ErrCapacityCancelled
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return err
	}
	if guardErr := legacyCapacityGuard(ctx, plan); guardErr != nil {
		return guardErr
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

// commitFor is the commit an action builds from: the one its plan pinned
// for the service's repository. A service from a plan that names no
// repository takes the commit of the deployment's one repository, and has
// none when the deployment moves several, since it can't be told which. A
// resource builds nothing, and a gateway or the database gateway is built
// from what Loomplane generates, so neither has one.
func commitFor(d *Deployment, a planning.Action) string {
	switch {
	case a.Service == nil || a.Service.Generated():
		return ""
	case a.Service.CommitSHA != "":
		return a.Service.CommitSHA
	case len(d.Sources) == 1:
		return d.Sources[0].CommitSHA
	case len(d.Sources) > 1:
		// d.CommitSHA names every repository, as "api@aaaaaaa
		// web@bbbbbbb", which isn't a commit.
		return ""
	}
	return d.CommitSHA
}

func (r *Runner) retainsCheckpoint(providerName string) bool {
	executor, ok := r.executors[providerName].(retryCheckpointExecutor)
	return ok && executor.RetainCheckpointOnRetry()
}

// Run carries out one action of a deployment. resume is the last
// checkpoint an earlier attempt saved, or nil; heartbeat saves one and
// tells Dispatch the activity is alive.
func (r *Runner) Run(ctx context.Context, orgID, deploymentID, actionID string, resume []byte, heartbeat func([]byte) error) (runErr error) {
	// A guard can reject work after the initial deployment read. Report the
	// durable wait state to workflow callers without converting it to Retry.
	defer func() {
		if runErr == nil {
			return
		}
		if signal := capacityEffectSignal(runErr); signal != nil {
			runErr = signal
			return
		}
		current, readErr := r.store.GetDeployment(context.WithoutCancel(ctx), orgID, deploymentID)
		if readErr != nil {
			runErr = errors.Join(runErr, readErr)
			return
		}
		if current.Status == StatusPaused {
			runErr = errors.Join(ErrCapacityPaused, runErr)
		}
		if current.Status == StatusCancelled {
			runErr = errors.Join(ErrCapacityCancelled, runErr)
		}
	}()
	ctx, heartbeat, stopHeartbeat := keepHeartbeat(ctx, resume, heartbeat, 30*time.Second)
	defer stopHeartbeat()
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	if d.Status == StatusCancelled {
		return ErrCapacityCancelled
	}
	if d.Status == StatusPaused {
		return ErrCapacityPaused
	}
	step := d.Step(actionID)
	if step == nil {
		return fmt.Errorf("%w: deployment %s has no step %s", ErrNotFound, deploymentID, actionID)
	}
	// A ramping step's candidate is up: the ramp settles the step.
	if step.Status == StepSucceeded || step.Status == StepRamping {
		return nil
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return err
	}
	if deferredPlan(plan) {
		if checkErr := legacyCapacityGuard(ctx, plan); checkErr != nil {
			return checkErr
		}
		wait, checkErr := r.AssessCapacity(ctx, orgID, deploymentID)
		if checkErr != nil {
			return checkErr
		}
		if wait != nil {
			return ErrCapacityPaused
		}
	}
	execution, err := r.executionPlan(ctx, plan)
	if err != nil {
		return err
	}
	var action planning.Action
	for _, a := range execution.Actions {
		if a.ID == actionID {
			action = a
		}
	}
	if action.Provider == "cloud-sql" && ctx.Value(sqlActionLockKey{}) == nil {
		if _, installed := r.store.(*PgStore); installed {
			return r.withSQLActionLock(ctx, d, action, func(work context.Context) error {
				return r.Run(work, orgID, deploymentID, actionID, resume, func(raw []byte) error {
					if lockErr := checkSQLActionLock(work); lockErr != nil {
						return lockErr
					}
					return heartbeat(raw)
				})
			})
		}
	}
	if action.Compensation != nil {
		if checkErr := r.validateCompensationCapability(ctx, d, plan, action); checkErr != nil {
			return checkErr
		}
	}
	if action.Compensation != nil && action.Provider == "server" && action.Compensation.ServerAttempt && ctx.Value(compensationRunKey{}) == nil {
		return r.runCloudCompensation(ctx, d, plan, action, step, heartbeat)
	}
	if checkErr := r.checkBackupPolicies(ctx, plan, []planning.Action{action}); checkErr != nil {
		return checkErr
	}
	if action.BackupEveryHours != 0 && d.StartedBy != plan.DecidedBy {
		return errors.New("default backup schedule approver changed")
	}
	poolLock, err := r.beginPoolExecution(ctx, plan, action)
	if err != nil {
		return err
	}
	if poolLock != nil {
		ctx = poolEffectContext(poolLock.Context(ctx))
		defer func() { runErr = errors.Join(runErr, poolLock.Finish(context.WithoutCancel(ctx))) }()
	}
	ctx, finishAttempt, err := r.beginWorkloadAttempt(ctx, d, action, false)
	if err != nil {
		return err
	}
	defer func() {
		// A failure the provider reported as final left nothing in flight,
		// so the next release needn't recover this one first. Any other
		// failure after the first effect keeps the service fenced.
		settled := runErr == nil && len(action.Routes) == 0 && plan.CloudSyncID == "" || finishedByProvider(runErr)
		runErr = errors.Join(runErr, finishAttempt(settled))
	}()
	admission, daemon, err := r.directServerAdmission(ctx, plan, action)
	if err != nil {
		return err
	}
	if admission != nil {
		defer func() { _ = admission.Rollback() }() //nolint:errcheck // releasing a read-only admission lock
		ctx = context.WithValue(ctx, admittedDaemonKey{}, daemon)
	}
	if checkErr := r.authorizeSync(ctx, plan); checkErr != nil {
		return checkErr
	}
	retainCheckpoint := poolAction(action) || plan.CloudSyncID != "" || r.retainsCheckpoint(action.Provider) || (step.Replaced != nil && r.retainsCheckpoint(step.Replaced.Provider))
	// The first move has not populated Step.Replaced yet. Its original provider
	// can still require durable cleanup even when the destination does not.
	if !retainCheckpoint && (action.Op == planning.Replace || action.Op == planning.Create) {
		bindings, readErr := r.plans.ListBindings(ctx, orgID, d.EnvironmentID)
		if readErr != nil {
			return readErr
		}
		for _, binding := range bindings {
			if string(binding.Subject)+":"+binding.Name == actionID && r.retainsCheckpoint(binding.Provider) {
				retainCheckpoint = true
				break
			}
		}
	}
	if retainCheckpoint {
		if len(step.Checkpoint) > 0 {
			resume = step.Checkpoint
		}
		durableHeartbeat := heartbeat
		heartbeat = func(raw []byte) error {
			step.Checkpoint = append([]byte(nil), raw...)
			if e := r.store.UpdateStep(ctx, orgID, d.ID, *step); e != nil {
				return e
			}
			return durableHeartbeat(raw)
		}
	}
	now := r.now().UTC()
	step.StartedAt, step.FinishedAt, step.Warnings = &now, nil, nil
	progress := func(status, message string) {
		step.Status, step.Message = status, message
		_ = r.store.UpdateStep(context.WithoutCancel(ctx), orgID, d.ID, *step) //nolint:errcheck // progress is best effort; the step's end is recorded
	}
	err = r.run(ctx, d, plan, action, step, resume, heartbeat, progress, poolLock)
	if err == nil {
		err = r.installBackupPolicy(ctx, d, action)
	}
	if err != nil {
		if signal := capacityEffectSignal(err); signal != nil {
			return signal
		}
		current, readErr := r.store.GetDeployment(context.WithoutCancel(ctx), orgID, d.ID)
		if readErr != nil {
			return errors.Join(err, readErr)
		}
		if current.Status == StatusCancelled {
			return errors.Join(ErrCapacityCancelled, err)
		}
		if blocks, blockErr := capacityBlocks(err); blockErr == nil && len(blocks) > 0 {
			st, ok := r.store.(CapacityStore)
			if !ok {
				return errors.New("capacity wait store is unavailable")
			}
			if current.CapacityWait != nil {
				blocks = append(blocks, current.CapacityWait.Blocks...)
			}
			if _, waitErr := st.SetCapacityWait(ctx, orgID, d.ID, current.CapacityRevision, blocks, r.now().UTC()); waitErr != nil {
				return waitErr
			}
			return ErrCapacityPaused
		}
		if current.Status == StatusPaused {
			return errors.Join(ErrCapacityPaused, err)
		}
		finished := r.now().UTC()
		r.telemetry.StepFinished(action.Provider, string(action.Op), StepFailed, finished.Sub(now))
		step.Status, step.Message, step.FinishedAt = StepFailed, err.Error(), &finished
		if uerr := r.store.UpdateStep(context.WithoutCancel(ctx), orgID, d.ID, *step); uerr != nil {
			err = errors.Join(err, uerr)
		}
		return err
	}
	if step.Status == StepRamping {
		return r.store.UpdateStep(ctx, orgID, d.ID, *step)
	}
	finished := r.now().UTC()
	r.telemetry.StepFinished(action.Provider, string(action.Op), StepSucceeded, finished.Sub(now))
	step.Status, step.FinishedAt = StepSucceeded, &finished
	step.Message = doneMessage(step)
	return r.store.UpdateStep(ctx, orgID, d.ID, *step)
}

// Preserve the boundary's rejection before consulting mutable deployment
// state. A newer resume remains running; the workflow can reassess and retry
// this activity without turning the rejected effect into a failed step.
func capacityEffectSignal(err error) error {
	if errors.Is(err, cloudinfra.ErrWorkloadCancelled) {
		return errors.Join(ErrCapacityCancelled, err)
	}
	if errors.Is(err, cloudinfra.ErrWorkloadPaused) {
		return errors.Join(ErrCapacityPaused, err)
	}
	return nil
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
	poolLock *cloudinfra.PoolExecutionLock,
) error {
	if plan.CloudSyncID != "" {
		ctx = planning.WithCloudSyncMutation(ctx, plan.CloudSyncID)
	}
	if a.Subject != planning.ServiceSubject && a.Subject != planning.ResourceSubject {
		return fmt.Errorf("%w: action subject %s is unavailable", ErrNoExecutor, a.Subject)
	}
	if a.Op == planning.Keep && a.AdoptCloudPlacement {
		return r.adoptCloudPlacement(ctx, d, plan, a, step, heartbeat)
	}
	exec, ok := r.executors[a.Provider]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoExecutor, a.Provider)
	}
	prov, err := r.providers.Get(a.Provider)
	if err != nil {
		return err
	}
	around, err := r.environmentBindings(ctx, d, a.ID)
	if err != nil {
		return err
	}
	current, resources := around.current, around.resources
	deleting := a.Op == planning.Delete
	if deleting && current == nil {
		return nil
	}
	connID := planConnection(plan, a, prov.Cloud)
	if deleting && current.ConnectionID != "" {
		// What's deleted goes through the connection that deployed it,
		// which needn't be the one the environment deploys with now. Only
		// a binding from before bindings recorded their connection is
		// taken to be on the plan's.
		connID = current.ConnectionID
	}
	if connID == "" {
		if deleting {
			return fmt.Errorf("%s was deployed before Loomplane recorded which connection deployed it, and the plan names no %s connection to delete it through. Choose one for %s and plan again",
				a.Name, prov.Cloud, d.Environment)
		}
		return fmt.Errorf("the plan names no %s connection", prov.Cloud)
	}
	conn, cred, err := r.credentials.Credential(ctx, d.OrgID, connID)
	// A delete whose connection is gone can't reach what it deletes. That
	// fails nothing: deleteBound stops tracking it and says so.
	unreachable := deleting && errors.Is(err, connections.ErrNotFound)
	if err != nil && !unreachable {
		return err
	}
	if checkErr := checkAdmittedDaemon(ctx, a, conn); checkErr != nil {
		return checkErr
	}
	if !deleting && a.ConnectionID != "" {
		if conn.Cloud != prov.Cloud || conn.Status != connections.StatusReady {
			return fmt.Errorf("the reviewed target for %s is not a ready %s connection; check it and plan again", a.Name, prov.Cloud)
		}
		if a.Service != nil && !reflect.DeepEqual(a.Service.TargetSettings, planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)) {
			return fmt.Errorf("the target settings for %s changed since this plan was reviewed; plan again", a.Name)
		}
	}
	if validationErr := r.validateCloudPlacement(ctx, plan, a, conn); validationErr != nil {
		return validationErr
	}
	if captureErr := r.captureAppliedVariables(ctx, d, &a, around.services, resources); captureErr != nil {
		return captureErr
	}
	pinAppliedSource(d, plan, &a)
	job, err := r.newJob(ctx, d, plan, a, conn, cred, current)
	if err != nil {
		return err
	}
	job.Resume, job.Checkpoint, job.Progress = resume, heartbeat, progress
	// Applying and removing what an action replaced save their progress
	// apart, so a retry hands each its own.
	rc := readReplaceCheckpoint(resume)
	job.Resume = rc.Apply
	if deleting {
		if derr := r.deleteBound(ctx, job, exec, current, unreachable, step, rc, heartbeat); derr != nil {
			return derr
		}
		// What the service was issued goes with it, once it's gone.
		step.Warnings = append(step.Warnings, r.releaseConsumers(ctx, d, a.Name, current, nil, resources)...)
		step.Warnings = append(step.Warnings, r.released(ctx, d, current, true)...)
		return nil
	}
	// A failure removing what an earlier move left only warns: the plan
	// keeps warning about it, and a permanent one, such as a revoked
	// permission, would otherwise fail every deployment of the action.
	warn := func(err error) { step.Warnings = append(step.Warnings, err.Error()) }
	// A move starts the new one with nothing bound to it, binds it once
	// it's up, with the old one recorded as still to remove, and only then
	// removes the old one, through the executor that deployed it. The step
	// records the old one too, before anything starts, so a retry of this
	// step knows its new one is up once the binding names something else.
	replacementDifferent := false
	if step.Replaced != nil && current != nil && !moves(current, a, connID) {
		same, identityErr := r.sameBindingResource(ctx, d.OrgID, step.Replaced, current)
		if identityErr != nil {
			return identityErr
		}
		replacementDifferent = !same
	}
	if replacementDifferent {
		// The new one is up and bound already, so removing what's left is
		// all there is to do. Starting the new one again would only
		// redeploy it. A step that replaced what's bound now, as a retry
		// after later deployments moved the action back can find, isn't
		// one of these.
		if plan.CloudSyncID != "" || len(a.Routes) > 0 {
			return nil
		}
		if rerr := r.removeReplaced(ctx, job, current, step.Replaced, warn, rc, heartbeat); rerr != nil {
			return rerr
		}
		step.Replaced = nil
		return nil
	}
	if routeErr := r.validateReleaseRoutes(ctx, job, current); routeErr != nil {
		return routeErr
	}
	moving := moves(current, a, connID) || len(a.Routes) > 0
	if moving && a.Op != planning.Replace && (a.Op != planning.Create || current.Drift != planning.DriftMissing) && current.Provider == a.Provider && current.Region == a.Region {
		// Only the planner moves something to another connection, since
		// only it deploys again what uses it, or refuses when something
		// held does. A plan made before the binding said where it runs,
		// as a retry after an executor recorded that would carry out,
		// didn't plan the move, so it doesn't make it. A Create over a
		// binding is planned: what it replaced was deleted outside
		// Loomplane, and the planner readdresses what uses it.
		return fmt.Errorf("%s runs through connection %s, not %s, which this plan deploys with, and this plan doesn't replace it, so nothing was deployed. Plan again, and the new plan replaces %s through this connection and deploys again what uses it",
			a.Name, current.ConnectionID, connID, a.Name)
	}
	if a.Subject == planning.ResourceSubject && current != nil && !moving && current.Drift != planning.DriftMissing &&
		current.ConnectionID != "" && current.ConnectionID != connID {
		// The planner refuses this. Applying the binding through another
		// connection would provision an empty resource under its name,
		// in what may be another account, and leave the one holding its
		// data behind. One deleted outside Loomplane has no data left to
		// leave, so, as the planner does, it's created again here.
		return fmt.Errorf("%s was provisioned through connection %s, not %s, which this plan deploys with. Loomplane doesn't move data between connections, so it won't provision %s through this one. Plan again to see what to do",
			a.Name, current.ConnectionID, connID, a.Name)
	}
	// A service the canary ramps starts its candidate beside what runs,
	// which a move has nothing to start beside. The planner never ramps
	// one.
	ramps := d.Canary.service(a.ID) != nil
	if ramps && moving {
		return fmt.Errorf("%s moves, so it can't ramp as a canary. Plan again", a.Name)
	}
	// The database gateway's record, keys and routes follow each release
	// in one go, so it never ramps. This refuses before deployInputs
	// records anything for it.
	if ramps && a.Service != nil && a.Service.DatabaseGateway != nil {
		return errCanaryDatabaseGateway(a.Name)
	}
	job.Checkpoint = func(data []byte) error {
		rc.Apply = data
		return heartbeat(rc.encode())
	}
	if moving {
		job.Binding, job.Replaces = nil, current
		step.Replaced = current
		if uerr := r.store.UpdateStep(ctx, d.OrgID, d.ID, *step); uerr != nil {
			return fmt.Errorf("record what %s replaces: %w", a.Name, uerr)
		}
	}
	if inputErr := r.deployInputs(ctx, job, around); inputErr != nil {
		return inputErr
	}
	if authErr := r.authorizeSync(ctx, plan); authErr != nil {
		return authErr
	}
	if effectErr := r.markWorkloadEffects(ctx); effectErr != nil {
		return effectErr
	}
	if _, fenced := server.EffectFenceFrom(ctx); fenced {
		if fenceErr := preparePoolEffects(ctx, exec, job); fenceErr != nil {
			return fenceErr
		}
		if poolLock != nil {
			if fenceErr := poolLock.Attempt(ctx); fenceErr != nil {
				return fenceErr
			}
		}
	}
	// What an executor warns about while it deploys reaches the step once
	// it has deployed, since its progress may update the step meanwhile.
	var applyMu sync.Mutex
	var applyWarnings []string
	job.warn = func(err error) {
		applyMu.Lock()
		defer applyMu.Unlock()
		applyWarnings = append(applyWarnings, err.Error())
	}
	if ramps {
		candidateErr := r.deployCandidate(ctx, d, exec, job, step)
		job.warn = nil
		if candidateErr == nil {
			step.Warnings = append(step.Warnings, applyWarnings...)
		}
		return candidateErr
	}
	out, err := exec.Apply(ctx, job)
	job.warn = nil
	if elsewhere := (*ElsewhereError)(nil); errors.As(err, &elsewhere) {
		return r.deployedElsewhere(ctx, current, elsewhere)
	}
	if err != nil {
		return err
	}
	step.URL, step.Image, step.Revision = out.URL, out.Image, out.Revision
	step.Warnings = append(step.Warnings, applyWarnings...)
	if out.URL != "" && a.Service != nil && a.Service.Public {
		progress(StepVerifying, "Checking "+out.URL+" answers")
		if verr := r.verifyService(ctx, out.URL, a); verr != nil {
			return verr
		}
	}
	// The database gateway is up, so its record can follow the plan.
	if a.Service != nil && a.Service.DatabaseGateway != nil {
		if serr := r.settleDatabaseGateway(ctx, d, &a); serr != nil {
			return serr
		}
	}
	old := job.Replaces
	if old != nil {
		destination := &planning.Binding{Provider: a.Provider, Region: a.Region, ConnectionID: connID, ExternalID: out.ExternalID, PhysicalID: out.PhysicalID}
		if out.ExternalID != "" && out.ExternalID == old.ExternalID && old.Provider == a.Provider {
			sameAccount, identityErr := r.sameBindingAccount(ctx, d.OrgID, old, destination)
			if identityErr != nil {
				return identityErr
			}
			if sameAccount && old.Region != a.Region && (old.PhysicalID == "" || out.PhysicalID == "") {
				return fmt.Errorf("%s can't move %s to %s: it kept the one it was to replace (%s), so nothing was removed", a.Provider, a.Name, placement(a.Provider, a.Region), old.ExternalID)
			}
		}
		same, identityErr := r.sameBindingResource(ctx, d.OrgID, old, destination)
		if identityErr != nil {
			return identityErr
		}
		if same {
			old = nil
		}
	}
	bound, err := r.bind(ctx, d, a, current, out, connID, old)
	if err != nil {
		return err
	}
	// A service that no longer needs a per-consumer output gives it back.
	if a.Service != nil {
		step.Warnings = append(step.Warnings, r.releaseConsumers(ctx, d, a.Name, current, a.Service, resources)...)
	}
	step.Warnings = append(step.Warnings, r.released(ctx, d, bound, false)...)
	if plan.CloudSyncID != "" || len(a.Routes) > 0 {
		return nil
	}
	if rerr := r.removeReplaced(ctx, job, bound, step.Replaced, warn, rc, heartbeat); rerr != nil {
		return rerr
	}
	step.Replaced = nil
	return nil
}

// environmentBindings are an environment's bindings as an action's job
// reads them: what runs the action now, if anything, and the services and
// resources by name.
type environmentBindings struct {
	current   *planning.Binding
	services  map[string]*planning.Binding
	resources map[string]*planning.Binding
}

func (r *Runner) environmentBindings(ctx context.Context, d *Deployment, actionID string) (*environmentBindings, error) {
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return nil, err
	}
	out := &environmentBindings{services: map[string]*planning.Binding{}, resources: map[string]*planning.Binding{}}
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name == actionID {
			out.current = b
		}
		switch b.Subject {
		case planning.ServiceSubject:
			out.services[b.Name] = b
		case planning.ResourceSubject:
			out.resources[b.Name] = b
		}
	}
	return out, nil
}

// planConnection is the connection an action deploys through: the one
// reviewed for it, or the plan's for its cloud. "" when there's neither.
func planConnection(plan *planning.Plan, a planning.Action, cloud string) string {
	if a.ConnectionID != "" {
		return a.ConnectionID
	}
	return plan.Connections[cloud]
}

// newJob is the job an action's executor gets, before what only deploying
// a service reads (deployInputs). Its Resume, Checkpoint and Progress are
// the caller's to set.
func (r *Runner) newJob(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action,
	conn *connections.Connection, cred []byte, current *planning.Binding,
) (*Job, error) {
	job := &Job{
		BuildCredentials: r.credentials, Logs: r.logs, Attempt: logAttempt(ctx, d.Retries),
		Deployment: d, Plan: plan, Action: a, Connection: conn, Credential: cred, Binding: current,
		Keep: func(ctx context.Context, field string, value []byte) error {
			if r.resources == nil {
				return errors.New("no resource credential store")
			}
			return r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field), value)
		},
		Recall: func(ctx context.Context, field string) ([]byte, error) {
			if r.resources == nil {
				return nil, ErrNoCredential
			}
			return r.resources.Get(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field))
		},
		Forget: func(ctx context.Context) error {
			if r.resources == nil {
				return nil
			}
			for _, field := range resourceCredentialFields {
				if derr := r.resources.Delete(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field)); derr != nil {
					return derr
				}
			}
			return nil
		},
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return nil, 0, errors.New("only a service is built from a repository")
		},
		CommitSHA: commitFor(d, a), SourceID: commitFor(d, a),
	}
	// A gateway is built from the source Loomplane generates for its
	// routes, and the database gateway from the program Loomplane ships,
	// not from the repository.
	if a.Service != nil && a.Service.Generated() {
		var data []byte
		var id string
		var gerr error
		if a.Service.DatabaseGateway != nil {
			data, id, gerr = dbgateway.Source()
		} else {
			data, id, gerr = gateway.Source(a.Service.Gateway)
		}
		if gerr != nil {
			return nil, fmt.Errorf("generate the gateway: %w", gerr)
		}
		job.SourceID = id
		job.Source = func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
		}
	}
	return job, nil
}

// deployInputs gives a service's job what deploying it reads: its source,
// its variables, and the resources its provider attaches to it.
func (r *Runner) deployInputs(ctx context.Context, job *Job, around *environmentBindings) error {
	d, a := job.Deployment, job.Action
	if a.Service == nil {
		return nil
	}
	if !a.Service.Generated() {
		job.Source = func(ctx context.Context) (io.ReadCloser, int64, error) {
			return r.appliedSource(ctx, d, a)
		}
	}
	var err error
	if a.Service.DatabaseGateway != nil {
		job.Env, job.Secrets, err = r.ensureDatabaseGateway(ctx, d, &job.Action)
	} else {
		job.Env, job.Secrets, err = r.env(ctx, d, a, around.services, around.resources)
	}
	if err != nil {
		return err
	}
	for _, link := range a.Service.Env {
		if b := around.resources[link.From]; link.Subject == planning.ResourceSubject && b != nil {
			job.Attach = append(job.Attach, b)
		}
	}
	return nil
}

// deployedElsewhere records the connection and region that reach what an
// action is bound to, when an executor finds another connection deployed
// it and the binding doesn't say so, as one from before bindings recorded
// their connection doesn't. Nothing was deployed: moving it here would give it a
// new address that what the plan keeps or holds wouldn't get. The next
// plan sees the move, and replaces it with everything that implies.
func (r *Runner) deployedElsewhere(ctx context.Context, current *planning.Binding, e *ElsewhereError) error {
	switch {
	case current == nil || e.ConnectionID == "":
		return e
	case current.ConnectionID == e.ConnectionID:
		// The binding has it right, and the key's region is what differs:
		// the connection's region changed since it was deployed. Planning
		// again wouldn't change anything.
		e.Region = cmp.Or(e.Region, current.Region)
		e.SameConnection = true
		return e
	}
	b := *current
	b.ConnectionID = e.ConnectionID
	if b.Region == "" {
		b.Region = e.Region
	}
	if err := r.plans.PutBinding(ctx, &b); err != nil {
		return errors.Join(e, fmt.Errorf("record where %s runs: %w", b.Name, err))
	}
	e.Recorded = true
	return e
}

// moves reports whether an action runs somewhere other than its binding:
// on another provider, or in another region, which the planner replaces.
// A service deployed through another connection, such as another
// account's, moves too, as the planner plans it, since what runs it there
// can't be reached from this one. A resource on another connection
// doesn't, since its data would stay behind: the planner refuses that,
// and run does too.
func moves(b *planning.Binding, a planning.Action, connID string) bool {
	switch {
	case b == nil:
		return false
	case b.Provider != a.Provider || b.Region != a.Region:
		return true
	}
	return a.Subject == planning.ServiceSubject && b.ConnectionID != "" && b.ConnectionID != connID
}

// replaceCheckpoint is what an action saves: its executor's progress and
// the progress removing what it replaced, apart, so a retry hands each
// its own. Removing is the external ID of what Remove's progress is for,
// since an action can have more than one old one to remove.
type replaceCheckpoint struct {
	PendingIdentity *PendingCloudIdentity `json:"pending_identity,omitempty"`
	PendingResource string                `json:"pending_resource,omitempty"`
	Replace         bool                  `json:"replace"`
	Apply           []byte                `json:"apply,omitempty"`
	Remove          []byte                `json:"remove,omitempty"`
	Removing        string                `json:"removing,omitempty"`
	RemovingScope   string                `json:"removing_scope,omitempty"`
}

// readReplaceCheckpoint reads what an earlier attempt at an action saved.
// Anything else, such as a checkpoint from before the runner split them,
// is its executor's.
func readReplaceCheckpoint(data []byte) *replaceCheckpoint {
	var rc replaceCheckpoint
	if len(data) > 0 && json.Unmarshal(data, &rc) == nil && rc.Replace {
		return &rc
	}
	return &replaceCheckpoint{Replace: true, Apply: data}
}

func (rc *replaceCheckpoint) encode() []byte {
	data, _ := json.Marshal(rc) //nolint:errcheck // a plain struct always encodes
	return data
}

// deleteBound carries out a Delete. What a move left running goes first,
// each through the connection that deployed it, then what's bound, through
// its own, and then the binding. Deleting the binding stops tracking what
// it lists, so one that can't be removed only warns, as it does on a later
// deployment: a permanent failure, such as a revoked permission, would
// otherwise fail every deployment that deletes the action. The warnings
// land on the step only once the binding is gone, since until then it
// still tracks them, and a retry tries them again. unreachable is a
// binding whose connection has been removed, which nothing can reach.
func (r *Runner) deleteBound(ctx context.Context, job *Job, exec Executor, bound *planning.Binding, unreachable bool,
	step *Step, rc *replaceCheckpoint, heartbeat func([]byte) error,
) error {
	if r.routes != nil {
		for _, retained := range append([]planning.Binding{*bound}, bound.Replaced...) {
			if routeErr := r.routes.ValidateRuntimeRemoval(ctx, bound.OrgID, bound.ProjectID, retained.ExternalID); routeErr != nil {
				return routeErr
			}
		}
	}
	var warnings []string
	warn := func(err error) { warnings = append(warnings, err.Error()) }
	if err := r.removeReplaced(ctx, job, bound, nil, warn, rc, heartbeat); err != nil {
		return err
	}
	a := job.Action
	if unreachable {
		if poolBinding(bound) {
			return errors.New("pooled server cleanup is blocked: restore the recorded connection before releasing capacity")
		}
		warn(fmt.Errorf("the connection that deployed %s has been removed, so Loomplane can't reach it to delete it, and stops tracking it. If %s still runs, delete it yourself, in the account that connection reached",
			a.Name, locate(exec, bound)))
		// What Loomplane itself keeps open on it still ends.
		if remover, ok := exec.(unreachableRemover); ok {
			job.warn = warn
			err := remover.removedUnreachable(ctx, job, bound)
			job.warn = nil
			if err != nil {
				return err
			}
		}
		if err := job.Forget(ctx); err != nil {
			return err
		}
	} else {
		job.Progress(StepDeploying, "Removing "+a.Name)
		if effectErr := r.markWorkloadEffects(ctx); effectErr != nil {
			return effectErr
		}
		removalCtx, fenceErr := preparePoolRemoval(ctx, exec, job, bound)
		if fenceErr != nil {
			return fenceErr
		}
		if err := validateFrontendCleanup(exec, job, bound); err != nil {
			return err
		}
		job.warn = warn
		err := exec.Remove(removalCtx, job, bound)
		job.warn = nil
		if err != nil {
			return err
		}
	}
	if !unreachable {
		if err := r.confirmFrontendRemoval(ctx, exec, job, bound); err != nil {
			return err
		}
	}
	// The database gateway's cloud service is gone, or out of reach for
	// good, so its record and keys go too, while the binding still says
	// what to delete if this fails.
	if err := r.removeDatabaseGateway(ctx, job.Deployment, bound); err != nil {
		return err
	}
	if err := r.plans.DeleteBinding(ctx, job.Deployment.OrgID, bound.ID); err != nil {
		return err
	}
	step.Warnings = append(step.Warnings, warnings...)
	return nil
}

// unreachableRemover is an executor with something of its own to end when
// what it deployed is deleted through a connection that's gone.
type unreachableRemover interface {
	removedUnreachable(ctx context.Context, job *Job, b *planning.Binding) error
}

// removeReplaced removes what an action ran as before it moved, which its
// binding records, through the executor that deployed each, and stops
// recording each one as it goes. One that's what's bound now, as after a
// move back onto it, is live, so it's only dropped from the record.
// Something already gone isn't an error, so a retry that gets here again
// finishes the job. Each one that can't be removed is kept, unless nothing
// here could ever reach it. Failing to remove own, what this step itself
// replaced, fails the step. Failing to remove what an earlier move left
// goes to warn instead, when there's one, so it doesn't fail a deployment
// that has nothing to do with it.
func (r *Runner) removeReplaced(ctx context.Context, job *Job, bound, own *planning.Binding, warn func(error),
	rc *replaceCheckpoint, heartbeat func([]byte) error,
) error {
	left := slices.Clone(bound.Replaced)
	var errs []error
	for _, old := range bound.Replaced {
		if c := job.Action.Compensation; c != nil && c.Retained && (old.PlanID != c.Candidate.PlanID || bindingIdentity(&old) != bindingIdentity(&c.Candidate)) {
			continue
		}
		done := true
		same, identityErr := r.sameBindingResource(ctx, job.Deployment.OrgID, &old, bound)
		if identityErr != nil {
			return identityErr
		}
		if !same {
			var err error
			done, err = r.removeOne(ctx, job, &old, rc, heartbeat)
			mine := own != nil && bindingIdentity(own) == bindingIdentity(&old)
			switch {
			case err == nil:
			case mine || warn == nil:
				errs = append(errs, err)
			default:
				warn(err)
			}
		}
		if !done {
			continue
		}
		left = slices.DeleteFunc(left, func(b planning.Binding) bool {
			return bindingIdentity(&b) == bindingIdentity(&old)
		})
		if err := r.plans.SetReplaced(ctx, job.Deployment.OrgID, bound.ID, left); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// removeOne removes one thing an action ran as before it moved: through
// the old provider's executor, with the connection that deployed it.
// done reports whether to stop recording it: it's gone, or nothing here
// could ever reach it.
func (r *Runner) removeOne(ctx context.Context, job *Job, old *planning.Binding, rc *replaceCheckpoint, heartbeat func([]byte) error) (done bool, err error) {
	a := job.Action
	exec, ok := r.executors[old.Provider]
	// Deleting the action deletes the binding that tracks the old one, so
	// a failure says it's left to you.
	deleting := a.Op == planning.Delete
	untracked := func(why error) error {
		return fmt.Errorf("%w. Deleting %s stops Loomplane tracking the old one, so if %s still runs, delete it yourself",
			why, a.Name, locate(exec, old))
	}
	// notYet is a failure that a change to Loomplane's setup fixes, so the
	// record stays, and a later attempt removes it.
	notYet := func(why error) error {
		if deleting {
			return untracked(why)
		}
		return fmt.Errorf("%w. %s now runs on %s, but the old one on %s (%s) can't be removed until that's fixed. Loomplane keeps track of it, and retrying this deployment, or the next one that deploys %s, removes it then",
			why, a.Name, placement(a.Provider, a.Region), placement(old.Provider, old.Region), old.ExternalID, a.Name)
	}
	if !ok {
		return false, notYet(fmt.Errorf("%w: %s, which ran %s before it moved", ErrNoExecutor, old.Provider, a.Name))
	}
	oldConnID := old.ConnectionID
	if oldConnID == "" {
		// A binding from before bindings recorded their connection was
		// deployed with the plan's connection to its cloud.
		prov, perr := r.providers.Get(old.Provider)
		if perr != nil {
			return false, notYet(fmt.Errorf("%w: %s, which ran %s before it moved, isn't a provider Loomplane knows", ErrNoExecutor, old.Provider, a.Name))
		}
		if oldConnID = job.Plan.Connections[prov.Cloud]; oldConnID == "" {
			return false, notYet(fmt.Errorf("the plan names no %s connection, which %s ran on before it moved", prov.Cloud, a.Name))
		}
	}
	conn, cred, err := r.credentials.Credential(ctx, job.Deployment.OrgID, oldConnID)
	if errors.Is(err, connections.ErrNotFound) && (job.Plan.CloudSyncID != "" || poolBinding(old)) {
		return false, err
	}
	if errors.Is(err, connections.ErrNotFound) {
		// Nothing can reach it again, so keeping the record would fail
		// every deployment of the action from now on.
		now := ""
		if !deleting {
			now = fmt.Sprintf(" %s now runs on %s.", a.Name, placement(a.Provider, a.Region))
		}
		return true, fmt.Errorf("the connection that deployed the old %s has been removed, so Loomplane can't reach it, and stops tracking it.%s If %s still runs, delete it yourself, in the account that connection reached",
			a.Name, now, locate(exec, old))
	}
	if err != nil {
		return false, err
	}
	if job.Plan.CloudSyncID != "" {
		var cfg planning.ServiceConfig
		if err := json.Unmarshal(old.Config, &cfg); err != nil {
			return false, err
		}
		if conn == nil || conn.ID != old.ConnectionID || conn.Status != connections.StatusReady {
			return false, errors.New("original cleanup connection is unavailable")
		}
		if cfg.CloudPlacement != nil && (cfg.CloudPlacement.AccountIdentity == "" || connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details) != cfg.CloudPlacement.AccountIdentity) {
			return false, errors.New("original cleanup account identity changed")
		}
	}
	gone := *job
	gone.Action.Provider, gone.Action.Region = old.Provider, old.Region
	gone.Connection, gone.Credential = conn, cred
	gone.Binding, gone.Replaces, gone.Resume = old, nil, nil
	if rc.Removing == "" || (rc.Removing == old.ExternalID && (rc.RemovingScope == "" || rc.RemovingScope == bindingIdentity(old))) {
		gone.Resume = rc.Remove
	}
	gone.Checkpoint = func(data []byte) error {
		rc.Remove, rc.Removing, rc.RemovingScope = data, old.ExternalID, bindingIdentity(old)
		return heartbeat(rc.encode())
	}
	gone.Keep = func(context.Context, string, []byte) error {
		return errors.New("removing what an action replaces keeps nothing")
	}
	// What's kept under the action's name, such as a database's address,
	// is the replacement's now.
	gone.Forget = func(context.Context) error { return nil }
	gone.replacing = true
	job.Progress(StepDeploying, "Removing the one it replaces on "+placement(old.Provider, old.Region))
	if authErr := r.authorizeCloudRemoval(ctx, job); authErr != nil {
		return false, authErr
	}
	if effectErr := r.markWorkloadEffects(ctx); effectErr != nil {
		return false, effectErr
	}
	removalCtx, fenceErr := preparePoolRemoval(ctx, exec, &gone, old)
	if fenceErr != nil {
		return false, fenceErr
	}
	if err := validateFrontendCleanup(exec, &gone, old); err != nil {
		return false, err
	}
	if err := removePendingOrBound(removalCtx, exec, &gone, old); err != nil {
		if deleting {
			return false, untracked(fmt.Errorf("remove the old %s on %s: %w", a.Name, placement(old.Provider, old.Region), err))
		}
		return false, fmt.Errorf("remove the %s it replaces on %s: %w", a.Name, placement(old.Provider, old.Region), err)
	}
	if err := r.confirmFrontendRemoval(ctx, exec, &gone, old); err != nil {
		return false, err
	}
	return true, nil
}

// Locator is an Executor that can say where what a binding names runs, in
// the provider's own terms, for a person who has to find it.
type Locator interface {
	Locate(b *planning.Binding) string
}

// locate says where what b names runs: as its executor puts it, or by
// provider, region and external ID.
func locate(exec Executor, b *planning.Binding) string {
	if l, ok := exec.(Locator); ok {
		if where := l.Locate(b); where != "" {
			return where
		}
	}
	return b.ExternalID + " on " + placement(b.Provider, b.Region)
}

// placement names a provider and, when it has one, a region:
// "cloud-run in europe-west1".
func placement(prov, region string) string {
	if region == "" {
		return prov
	}
	return prov + " in " + region
}

// env is what a service gets: its resolved variables, then the address of
// each thing it needs. A variable someone set wins over an address
// Loomplane would set, so you can point a service elsewhere.
func (r *Runner) env(ctx context.Context, d *Deployment, a planning.Action, services, resources map[string]*planning.Binding) (env map[string]string, secret map[string]bool, err error) {
	resourceValues, gatewayValues, gatewaySecrets := map[string]string{}, map[string]string{}, map[string]bool{}
	if a.Service.Generated() {
		// A gateway gets the addresses it routes to and nothing else: none
		// of the project's variables, and so none of its secrets.
		env, secret = map[string]string{}, map[string]bool{}
	} else if a.Service.AppliedInputs != nil && a.Service.AppliedInputs.Variables != "" {
		if r.resources == nil {
			return nil, nil, errors.New("applied variable snapshot store is unavailable")
		}
		raw, snapshotErr := r.resources.Get(ctx, a.Service.AppliedInputs.Variables)
		if snapshotErr != nil {
			return nil, nil, fmt.Errorf("read applied variables: %w", snapshotErr)
		}
		var snapshot appliedVariables
		if snapshotErr := json.Unmarshal(raw, &snapshot); snapshotErr != nil {
			return nil, nil, snapshotErr
		}
		env, secret = snapshot.Values, snapshot.Secrets
		resourceValues, gatewayValues, gatewaySecrets = snapshot.ResourceValues, snapshot.GatewayValues, snapshot.GatewaySecrets
		if env == nil {
			env = map[string]string{}
		}
		if secret == nil {
			secret = map[string]bool{}
		}
	} else if env, secret, err = r.variables.Values(ctx, d.OrgID, d.ProjectID, d.Environment, a.Service.Service); err != nil {
		return nil, nil, fmt.Errorf("resolve variables: %w", err)
	}
	for _, link := range a.Service.Env {
		if _, set := env[link.Key]; set {
			continue
		}
		if link.Subject == planning.ResourceSubject {
			if pinned, ok := resourceValues[link.Key]; ok {
				env[link.Key], secret[link.Key] = pinned, true
				pinnedExtras := map[string]linkExtra{}
				for _, k := range []string{link.Key + GatewaySuffix, link.Key + DirectSuffix} {
					if v, ok := gatewayValues[k]; ok {
						pinnedExtras[k] = linkExtra{Value: v, Secret: gatewaySecrets[k]}
					}
				}
				setExtras(env, secret, pinnedExtras)
				continue
			}
			if a.Service.AppliedInputs != nil && a.Service.AppliedInputs.Variables != "" {
				return nil, nil, errors.New("applied resource credential snapshot is missing")
			}
			// A resource's address holds its credentials, so it's secret.
			if resources[link.From] == nil || r.resources == nil {
				return nil, nil, fmt.Errorf("%s needs %s, which hasn't been provisioned", a.Name, link.From)
			}
			if _, consumer := consumerOutput(link); consumer {
				if err := consumerThroughGateway(a.Name, link); err != nil {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("%s's %s for %s is issued when its variables are captured, and none was", link.From, link.Field, a.Name)
			}
			addr, extra, err := r.resourceAddress(ctx, d, a.Name, services, link)
			if err != nil {
				return nil, nil, err
			}
			env[link.Key], secret[link.Key] = addr, true
			setExtras(env, secret, extra)
			continue
		}
		if link.Address != "" {
			env[link.Key] = link.Address
			continue
		}
		dep := services[link.From]
		if dep == nil || dep.URL == "" {
			return nil, nil, fmt.Errorf("%s needs %s, which has no address yet", a.Name, link.From)
		}
		env[link.Key] = dep.URL
	}
	return env, secret, nil
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

// releaseObserverTimeout is how long each release observer has.
const releaseObserverTimeout = 10 * time.Second

// released tells the release observers of a service binding a step wrote,
// or deleted (removed). It returns what they failed at as warnings naming
// the service: the binding has changed already, so failing the step
// wouldn't undo it. A resource binding isn't observed.
func (r *Runner) released(ctx context.Context, d *Deployment, b *planning.Binding, removed bool) []string {
	if b == nil || b.Subject != planning.ServiceSubject {
		return nil
	}
	var warnings []string
	for _, observer := range r.observers {
		observed, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseObserverTimeout)
		err := observer.Released(observed, Release{OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Binding: cloneBinding(b), Removed: removed})
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", b.Name, err))
		}
	}
	return warnings
}

// cloneBinding is a copy of b that shares nothing with it, so an observer
// can't change what the step holds.
func cloneBinding(b *planning.Binding) *planning.Binding {
	c := *b
	c.Config = slices.Clone(b.Config)
	if b.OwnedResources != nil {
		c.OwnedResources = make(map[string]json.RawMessage, len(b.OwnedResources))
		for key, value := range b.OwnedResources {
			c.OwnedResources[key] = slices.Clone(value)
		}
	}
	if b.ObservedAt != nil {
		at := *b.ObservedAt
		c.ObservedAt = &at
	}
	c.Replaced = nil
	for _, old := range b.Replaced {
		c.Replaced = append(c.Replaced, *cloneBinding(&old))
	}
	return &c
}

// bind records what runs an action now, with what it ran as before it
// moved and still needs removing: what the binding recorded already, and
// replaced, when it moves now. Something it runs as again, after a move
// back, isn't left to remove.
func (r *Runner) bind(ctx context.Context, d *Deployment, a planning.Action, current *planning.Binding, out *Outcome, connID string, replaced *planning.Binding) (*planning.Binding, error) {
	if current != nil && current.PhysicalID != "" && out.PhysicalID == "" && current.Provider == a.Provider && current.ExternalID == out.ExternalID && current.ConnectionID == connID {
		return nil, errors.New("the provider omitted the known physical resource identity; binding update is blocked")
	}
	var cfg any = a.Service
	if a.Subject == planning.ResourceSubject {
		cfg = a.Resource
	} else if a.Service != nil && a.Service.Artifact != nil {
		// An image reused from another environment belongs to the plan
		// that chose it, never to the binding: an action rebuilt from the
		// binding later, by a Cloud sync move or a pool relocation, builds
		// its commit for wherever it goes.
		svc := *a.Service
		svc.Artifact = nil
		cfg = &svc
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode binding: %w", err)
	}
	now := r.now().UTC()
	b := &planning.Binding{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Subject: a.Subject, Name: a.Name,
		Provider: a.Provider, Region: a.Region, Engine: a.Engine, ExternalID: out.ExternalID, PhysicalID: out.PhysicalID, Config: raw, URL: out.URL,
		ConnectionID: connID, Revision: out.Revision, CommitSHA: commitFor(d, a),
		PlanID: d.PlanID, CreatedAt: now, UpdatedAt: now,
	}
	if out.OwnedResources != "" {
		if err := json.Unmarshal([]byte(out.OwnedResources), &b.OwnedResources); err != nil || b.OwnedResources == nil {
			return nil, errors.New("provider ownership must be a JSON object")
		}
	}
	if _, ownershipErr := b.OwnsResources(); ownershipErr != nil {
		return nil, ownershipErr
	}
	var left []planning.Binding
	if current != nil {
		b.ID, b.CreatedAt = current.ID, current.CreatedAt
		left = slices.Clone(current.Replaced)
		owned, ownershipErr := current.OwnsResources()
		if ownershipErr != nil {
			return nil, ownershipErr
		}
		if owned {
			old := *current
			old.Replaced = nil
			left = append(left, old)
		}
	} else {
		id, err := typeid.Generate("bind")
		if err != nil {
			return nil, fmt.Errorf("generate binding id: %w", err)
		}
		b.ID = id.String()
	}
	if replaced != nil {
		old := *replaced
		old.Replaced = nil
		left = append(left, old)
	}
	for _, old := range left {
		same, identityErr := r.sameBindingResource(ctx, d.OrgID, &old, b)
		if identityErr != nil {
			return nil, identityErr
		}
		duplicate := slices.ContainsFunc(b.Replaced, func(o planning.Binding) bool { return bindingIdentity(&o) == bindingIdentity(&old) })
		if same && !b.RetainsOwnership(&old) {
			return nil, errors.New("the provider discarded existing resource ownership; binding update is blocked")
		}
		if same || duplicate {
			continue
		}
		b.Replaced = append(b.Replaced, old)
	}
	if err := r.plans.PutBinding(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Finish settles a deployment: failed with reason, or succeeded. Steps
// still pending after a failure are marked skipped, and steps still
// ramping failed. A canary still in flight after a failure settles (see
// settleCanary). A success records the commit of each repository it moved
// as what the environment runs; a repository the plan held keeps what it
// had.
func (r *Runner) Finish(ctx context.Context, orgID, deploymentID, reason string) error {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return err
	}
	if done, replayErr := r.replayFinalization(ctx, d, reason); done || replayErr != nil {
		return replayErr
	}
	if d.Status == StatusPaused || d.Status == StatusCancelled {
		return nil
	}
	var watch WatchDecision
	if reason == "" && d.Status != StatusFailed {
		watch = r.prepareWatch(ctx, d)
		if cleanupErr := r.finishRoutedRelease(ctx, d); cleanupErr != nil {
			return cleanupErr
		}
	}
	status := StatusSucceeded
	if reason != "" {
		status = StatusFailed
		// A durable finish settles the canary and marks the steps in the
		// store's fenced transaction, so a late finish from an earlier
		// retry can't settle the canary a retry reopened. Without one, the
		// canary settles here first, from its services, so it never reads
		// steps this marks.
		if orchestration.FinalizationKey(ctx) == "" {
			if err := r.settleCanary(ctx, d, reason, false); err != nil {
				return err
			}
			for _, i := range failFinishedSteps(d.Steps, reason, r.now().UTC()) {
				if err := r.store.UpdateStep(ctx, orgID, d.ID, d.Steps[i]); err != nil {
					return err
				}
			}
		}
	}
	now := r.now().UTC()
	if err := r.store.Finish(ctx, orgID, d.ID, status, reason, now); err != nil {
		return err
	}
	r.telemetry.DeploymentFinished(status, now.Sub(d.StartedAt))
	// A deployment that already failed stays failed, so a late success
	// for it records nothing.
	if reason != "" || d.Status == StatusFailed {
		return nil
	}
	settled, readErr := r.store.GetDeployment(ctx, orgID, d.ID)
	if readErr != nil {
		return readErr
	}
	if settled.Status != StatusSucceeded {
		return nil
	}
	if recordErr := r.recordDeployed(ctx, d, now); recordErr != nil {
		return recordErr
	}
	return r.armWatch(ctx, settled, watch)
}

// recordDeployed records the commit of each repository a deployment's plan
// moved, and the manifest revision the plan was made from, as what its
// environment runs, in one write. A rollback records like any release, so
// the restored manifest becomes the environment's. A Cloud sync and a
// catalog plan record nothing: neither releases the environment's
// repositories or manifest.
func (r *Runner) recordDeployed(ctx context.Context, d *Deployment, at time.Time) error {
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return err
	}
	if plan.CloudSyncID != "" || plan.Catalog {
		return nil
	}
	release := &planning.DeployedRelease{
		EnvironmentID: d.EnvironmentID, PlanID: d.PlanID, ManifestID: plan.ManifestID, ManifestRevision: plan.ManifestRevision,
		DeployedAt: at, Sources: make([]*planning.DeployedSource, 0, len(plan.Sources)),
	}
	for _, ps := range plan.Sources {
		if ps.InScope {
			release.Sources = append(release.Sources, &planning.DeployedSource{
				EnvironmentID: d.EnvironmentID, SourceID: ps.SourceID, AnalysisID: ps.AnalysisID, CommitSHA: ps.CommitSHA,
				PlanID: d.PlanID, DeployedAt: at,
			})
		}
	}
	if err := r.plans.RecordRelease(ctx, d.OrgID, release); err != nil {
		return fmt.Errorf("record what's deployed: %w", err)
	}
	return nil
}

func (r *Runner) verifyService(ctx context.Context, address string, a planning.Action) error {
	if a.Service == nil || !planning.OwnedCloud(a.Provider) {
		return r.verify(ctx, address)
	}
	health := a.Service.HealthPath
	if health == "" {
		return r.verify(ctx, address)
	}
	target, err := neturl.Parse(address)
	if err != nil {
		return err
	}
	target.Path, target.RawPath, target.RawQuery, target.Fragment = health, "", "", ""
	deadline := r.now().Add(r.verifyWait)
	client := *r.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
		if requestErr != nil {
			return requestErr
		}
		res, requestErr := client.Do(req)
		if requestErr == nil {
			_ = res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return nil
			}
			requestErr = fmt.Errorf("health endpoint returned HTTP %d", res.StatusCode)
		}
		if r.now().After(deadline) {
			return fmt.Errorf("the public service did not pass its health check: %w", requestErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
