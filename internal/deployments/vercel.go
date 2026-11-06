package deployments

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // G505: Vercel names file content by its SHA-1
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	// uploads is how many files go to Vercel at once.
	uploads = 8
	// maxUploadFile is the largest file uploaded; bigger ones are left
	// out of the deployment, since a web app's build doesn't read them.
	maxUploadFile = 32 << 20
)

// vercelFrameworks maps the frameworks analysis names to Vercel's
// presets. Anything else lets Vercel detect it.
var vercelFrameworks = map[string]string{
	"next": "nextjs", "nuxt": "nuxtjs", "remix": "remix", "sveltekit": "sveltekit-1", "astro": "astro",
	"vite": "vite", "vitepress": "vitepress", "gatsby": "gatsby",
}

// Vercel deploys web apps and static sites as Vercel deployments: the
// commit's files uploaded, built by Vercel, and served from its network.
// It's a Loomplane capability adapter, not a Ctrl Plane provider: a
// Vercel deployment is an immutable build behind a URL, not an instance
// that starts and stops.
//
// With Environments set, each service is one Vercel project and each
// environment deploys into a Vercel environment of it: production,
// Preview or a custom environment, with its variables scoped to that one
// environment. An environment Vercel won't give a native environment
// falls back to a project of its own, deployed to its production target,
// which is how every environment deploys when Environments is nil.
type Vercel struct {
	Client       *vercel.Client
	Environments *NativeEnvironments
}

var _ Executor = (*Vercel)(nil)

// RetainCheckpointOnRetry keeps native resource create intents and identities.
func (*Vercel) RetainCheckpointOnRetry() bool { return true }

type vercelState struct {
	ProjectID    string `json:"project_id,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	LogAttempt   string `json:"log_attempt,omitempty"`
	// Mode is ModeNative or ModeFallback once the job knows which; empty
	// when Environments is nil.
	Mode string `json:"mode,omitempty"`
	// Target is the native environment: "production", "preview" or the
	// custom environment's id.
	Target string `json:"target,omitempty"`
	// CreatedProject and CreatedEnvironment (a custom environment id) are
	// what this job made on Vercel for a native deploy. A fallback removes
	// those and nothing else.
	CreatedProject     bool   `json:"created_project,omitempty"`
	CreatedEnvironment string `json:"created_environment,omitempty"`
}

func token(job *Job) vercel.Token {
	tok := vercel.Token{Value: strings.TrimSpace(string(job.Credential))}
	if job.Connection != nil {
		tok.TeamID = job.Connection.Details["team_id"]
	}
	return tok
}

// ProjectName is the Vercel project a service runs as in an environment
// of its own: the fallback, and every environment when native
// environments are off.
func ProjectName(job *Job) string {
	return strings.ToLower(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
}

// ServiceProjectName is the Vercel project a service's native
// environments share.
func ServiceProjectName(job *Job) string {
	return strings.ToLower(job.Action.Name + "-" + idSuffix(job.Deployment.ProjectID))
}

// targetRef is how a target is written in the checkpoint, the binding ref
// and the record: the built-in name or the custom environment id.
func targetRef(t vercel.Target) string {
	if t.CustomID != "" {
		return t.CustomID
	}
	return t.Builtin
}

// refTarget reads a target written by targetRef.
func refTarget(ref string) vercel.Target {
	switch ref {
	case vercel.Production.Builtin, vercel.Preview.Builtin:
		return vercel.Target{Builtin: ref}
	}
	return vercel.Target{CustomID: ref}
}

// nativeRefusalError is Vercel declining a native environment for this
// project. The environment falls back instead of failing.
type nativeRefusalError struct {
	reason    string
	permanent bool
	err       error
}

func (r *nativeRefusalError) Error() string { return r.err.Error() }
func (r *nativeRefusalError) Unwrap() error { return r.err }

// refusedNative marks err as a refusal when Vercel's answer means it won't
// give the project a native environment. Anything else, transient errors
// included, is returned as it is and fails the deploy.
func refusedNative(err error) error {
	if reason, permanent, ok := vercel.FallbackReason(err); ok {
		return &nativeRefusalError{reason: reason, permanent: permanent, err: err}
	}
	return err
}

// Apply implements Executor.
func (v *Vercel) Apply(ctx context.Context, job *Job) (_ *Outcome, err error) {
	if job.Action.Resource != nil {
		return v.applyResource(ctx, job)
	}
	if job.Secrets == nil {
		job.Secrets = map[string]bool{}
	}
	if job.Action.Service != nil {
		for _, link := range job.Action.Service.Env {
			if link.Subject == planning.ResourceSubject {
				job.Secrets[link.Key] = true
			}
		}
	}
	defer nativeDeliveryBoundary(job)(&err)
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: vercel runs web apps and static sites, not %s", ErrNoExecutor, job.Action.Subject)
	}
	var state vercelState
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
	d := &vercelDeploy{v: v, job: job, tok: token(job), state: &state, save: save}
	if f, ok := vercelFrameworks[cfg.Framework]; ok {
		d.settings.Framework = &f
	}
	if root := strings.Trim(cfg.Source, "/"); root != "" && root != "." {
		d.settings.RootDirectory = &root
	}
	if v.Environments == nil || job.Connection == nil {
		return d.run(ctx, false)
	}
	native, err := d.native(ctx)
	if err != nil {
		return nil, err
	}
	if native {
		out, err := d.run(ctx, true)
		var refusal *nativeRefusalError
		if !errors.As(err, &refusal) {
			return out, err
		}
		if state.DeploymentID != "" {
			// The environment already has a deployment of this job in it;
			// falling back would remove the environment under it.
			return nil, refusal.err
		}
		if d.alreadyNative() {
			return nil, fmt.Errorf("%s already deploys into its %s Vercel environment, so it doesn't fall back to a separate project: %w",
				job.Action.Name, job.Deployment.Environment, refusal.err)
		}
		if err := d.undoNative(ctx); err != nil {
			return nil, err
		}
		if err := v.Environments.FallBack(ctx, job, "vercel", job.Action.Name, refusal.reason, refusal.permanent); err != nil {
			return nil, err
		}
		state = vercelState{Mode: ModeFallback}
		save()
	}
	return d.run(ctx, false)
}

// vercelDeploy is one service deploy in progress.
type vercelDeploy struct {
	v        *Vercel
	job      *Job
	tok      vercel.Token
	settings vercel.ProjectSettings
	state    *vercelState
	save     func()
	// record is the environment's native record when it had one.
	record *ProviderEnvironment
	// recordedNative is set when the record, as the deploy found it, named
	// a native environment and not just a claim on the project.
	recordedNative bool
}

// native says whether the deploy goes into a native environment. Not when
// this job or an earlier deploy of the environment fell back: a fallback
// is sticky.
func (d *vercelDeploy) native(ctx context.Context) (bool, error) {
	if d.state.Mode == ModeFallback {
		return false, nil
	}
	rec, err := d.v.Environments.Record(ctx, d.job, "vercel", d.job.Action.Name)
	if err != nil {
		return false, err
	}
	if rec == nil || rec.Mode != ModeFallback {
		if err := d.refuseOrphaning(); err != nil {
			return false, err
		}
		d.record = rec
		d.recordedNative = rec != nil && rec.EnvironmentRef != ""
		return true, nil
	}
	// Anything checkpointed belongs to the shared project, not the
	// environment's own.
	*d.state = vercelState{Mode: ModeFallback}
	d.save()
	return false, nil
}

// refuseOrphaning stops a native deploy over a service that runs on a
// Vercel project of its own when the plan doesn't move it. Such a plan was
// made while a fallback record kept the service there, and "try again"
// cleared the record since: deploying into the shared project would rebind
// the service and leave its own project serving, with nothing tracking it.
// A first deploy, a move (the runner binds nothing to either), a binding
// that's native already, and one whose project was deleted outside
// Loomplane have nothing to leave behind.
func (d *vercelDeploy) refuseOrphaning() error {
	job, b := d.job, d.job.Binding
	if b == nil || job.Action.NativeMove || b.Drift == planning.DriftMissing {
		return nil
	}
	projectID, _, native := planning.ParseVercelRef(b.ExternalID)
	if native {
		return nil
	}
	return fmt.Errorf("%s runs on a Vercel project of its own (%s), and this plan doesn't move it into its shared project's %s environment, so nothing was deployed. Plan again, and the new plan moves %s there and removes the old project once it's up",
		job.Action.Name, projectID, job.Deployment.Environment, job.Action.Name)
}

// alreadyNative says whether the service runs in a native environment
// already: its record names one, or its binding does. A refusal then fails
// the deploy, since falling back would rebind the service to a separate
// project and leave the native environment serving, untracked. Only a
// first native deploy, or a move from a project of its own, falls back.
func (d *vercelDeploy) alreadyNative() bool {
	if d.recordedNative {
		return true
	}
	if b := d.job.Binding; b != nil {
		_, _, native := planning.ParseVercelRef(b.ExternalID)
		return native
	}
	return false
}

// run deploys the service: into its native environment of the shared
// project, or to the production target of the environment's own project.
// In a native run, Vercel refusing the environment comes back as a
// *nativeRefusalError.
func (d *vercelDeploy) run(ctx context.Context, native bool) (*Outcome, error) {
	v, job, tok, state := d.v, d.job, d.tok, d.state
	name := ProjectName(job)
	if native {
		name = ServiceProjectName(job)
		state.Mode = ModeNative
	}
	job.Progress(StepDeploying, "Setting up the Vercel project")
	project, created, err := v.Client.EnsureProjectCreated(ctx, tok, name, d.settings)
	if err != nil {
		return nil, err
	}
	if state.ProjectID != "" && state.ProjectID != project.ID {
		return nil, errors.New("the Vercel project identity changed since this deployment started")
	}
	state.ProjectID = project.ID
	if native && created {
		state.CreatedProject = true
		d.save()
	}
	if native && (d.record == nil || d.record.ProjectRef != project.ID) {
		// Claim the project before writing to it, so another
		// environment's fallback never removes it while this deploys.
		if claimErr := v.Environments.Native(ctx, job, "vercel", job.Action.Name, project.ID, ""); claimErr != nil {
			return nil, fmt.Errorf("claim the vercel project: %w", claimErr)
		}
		d.record = &ProviderEnvironment{Mode: ModeNative, ProjectRef: project.ID}
	}
	if checkpointErr := checkpointCloudIdentity(job, *state); checkpointErr != nil {
		return nil, checkpointErr
	}
	if checkErr := vercelCredentialBridgeCollision(job, project.ID); checkErr != nil {
		return nil, checkErr
	}
	target, refuse := vercel.Production, func(err error) error { return err }
	if native {
		if target, err = d.nativeTarget(ctx, project); err != nil {
			return nil, err
		}
		// A retry deploys to the same environment.
		d.save()
		refuse = refusedNative
	}
	var resourceKeys []string
	for _, link := range job.Action.Service.Env {
		if link.Subject == planning.ResourceSubject {
			resourceKeys = append(resourceKeys, link.Key)
		}
	}
	if len(resourceKeys) > 0 {
		owner := tok.TeamID
		if owner == "" && job.Connection != nil {
			owner = job.Connection.Details["user_id"]
		}
		if checkErr := v.Client.VerifyResourceProject(ctx, tok, project.ID, owner); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := v.Client.VerifyRuntimeResourceIsolation(ctx, tok, project.ID, target, resourceKeys); checkErr != nil {
			return nil, checkErr
		}
	}
	_, buildEnv, buildSecrets := vercelDeploymentEnvironments(job)
	if serr := v.Client.SetEnv(ctx, tok, project.ID, target, buildEnv, buildSecrets); serr != nil {
		// Vercel refusing a sensitive variable here falls back; a secret
		// is never written as merely encrypted to stay native.
		return nil, refuse(serr)
	}
	if state.DeploymentID == "" {
		id, derr := v.deploy(ctx, tok, job, project, target, refuse)
		if derr != nil {
			return nil, derr
		}
		state.DeploymentID = id
		d.save()
	}
	job.Progress(StepBuilding, "Building on Vercel")
	if state.LogAttempt == "" {
		state.LogAttempt = job.LogAttempt()
		d.save()
	}
	output := startManagedOutput(ctx, job, state.DeploymentID, state.LogAttempt, func(ctx context.Context, cursor string, follow bool, emit buildlogs.Emit) (string, bool, error) {
		return v.Client.BuildLogs(ctx, tok, state.DeploymentID, cursor, follow, emit)
	})
	dep, err := v.Client.WaitDeployment(ctx, tok, state.DeploymentID, target)
	output.finish(ctx, dep != nil)
	if err != nil {
		// Vercel returns the deployment with an error only once it ended
		// in ERROR or CANCELED.
		if dep != nil {
			return nil, &finishedError{err: err}
		}
		return nil, err
	}
	if !native {
		return &Outcome{ExternalID: project.ID, URL: dep.Address(), Revision: dep.ID}, nil
	}
	if err := v.Environments.Native(ctx, job, "vercel", job.Action.Name, project.ID, state.Target); err != nil {
		return nil, fmt.Errorf("record the native vercel environment: %w", err)
	}
	return &Outcome{ExternalID: planning.VercelRef(project.ID, state.Target), URL: dep.Address(), Revision: dep.ID}, nil
}

// nativeTarget is the Vercel environment the job's environment maps to:
// the one this job checkpointed, the one it already deploys to on the
// project, or on a first deploy production, Preview or a custom
// environment named after it, created when it's missing.
func (d *vercelDeploy) nativeTarget(ctx context.Context, project *vercel.Project) (vercel.Target, error) {
	if d.state.Target != "" {
		return refTarget(d.state.Target), nil
	}
	if ref := d.savedTarget(project.ID); ref != "" {
		d.state.Target = ref
		return refTarget(ref), nil
	}
	et, err := d.v.Environments.Target(ctx, d.job)
	if err != nil {
		return vercel.Target{}, err
	}
	t := vercel.Target{Builtin: et.Builtin}
	if et.Builtin == "" {
		env, created, err := d.v.Client.EnsureCustomEnvironmentCreated(ctx, d.tok, project.ID, strings.ToLower(et.Name))
		if err != nil {
			return vercel.Target{}, refusedNative(err)
		}
		t = vercel.Target{CustomID: env.ID}
		if created {
			d.state.CreatedEnvironment = env.ID
		}
	}
	d.state.Target = targetRef(t)
	return t, nil
}

// savedTarget is the Vercel environment the job's environment already
// deploys to on the project: the record's, or the one its binding, or
// what the job replaces, names there. It's "" on a first deploy. Once an
// environment is native its target stays put, even when deleting another
// environment makes it the oldest of its kind: moving it would hand it
// the other environment's leftover variables and orphan its own.
func (d *vercelDeploy) savedTarget(projectID string) string {
	if r := d.record; r != nil && r.ProjectRef == projectID && r.EnvironmentRef != "" {
		return r.EnvironmentRef
	}
	for _, b := range []*planning.Binding{d.job.Binding, d.job.Replaces} {
		if b == nil {
			continue
		}
		if p, target, native := planning.ParseVercelRef(b.ExternalID); native && p == projectID && target != "" {
			return target
		}
	}
	return ""
}

// undoNative removes what this job made on Vercel for the native deploy
// Vercel then refused: the custom environment with its variables, and the
// shared project when no other environment records or claims a native
// deploy on it.
// Nothing that existed before the job is touched. Each step is
// checkpointed, so a retry picks up where this left off.
func (d *vercelDeploy) undoNative(ctx context.Context) error {
	v, tok, s := d.v, d.tok, d.state
	if s.CreatedEnvironment != "" {
		t := vercel.Target{CustomID: s.CreatedEnvironment}
		if err := v.Client.DeleteEnv(ctx, tok, s.ProjectID, t); err != nil {
			return fmt.Errorf("remove the refused vercel environment's variables: %w", err)
		}
		if err := v.Client.DeleteCustomEnvironment(ctx, tok, s.ProjectID, s.CreatedEnvironment); err != nil {
			return fmt.Errorf("remove the refused vercel custom environment: %w", err)
		}
		s.CreatedEnvironment, s.Target = "", ""
		d.save()
	}
	if s.CreatedProject {
		job := d.job
		others, err := otherNativeUsers(ctx, v.Environments.Store, "vercel", job.Deployment.OrgID, s.ProjectID, job.Deployment.EnvironmentID, job.Action.Name)
		if err != nil {
			return fmt.Errorf("check who uses the vercel project: %w", err)
		}
		if !others {
			if err := v.Client.DeleteProject(ctx, tok, s.ProjectID); err != nil {
				return fmt.Errorf("remove the unused vercel project: %w", err)
			}
		}
		s.CreatedProject, s.ProjectID = false, ""
		d.save()
	}
	return nil
}

// deploy uploads the commit and starts a deployment of it to target.
// refuse classifies Vercel's answer to the deployment itself.
func (v *Vercel) deploy(ctx context.Context, tok vercel.Token, job *Job, project *vercel.Project, target vercel.Target, refuse func(error) error) (string, error) {
	job.Progress(StepDeploying, "Uploading the source to Vercel")
	files, err := v.upload(ctx, tok, job)
	if err != nil {
		return "", err
	}
	runtimeEnv, buildEnv, _ := vercelDeploymentEnvironments(job)
	d, err := v.Client.CreateDeploymentWithRuntimeEnv(ctx, tok, project, target, files, map[string]string{
		"githubCommitSha": job.CommitSHA, "loomplanePlan": job.Plan.ID,
	}, runtimeEnv, buildEnv)
	if err != nil {
		return "", refuse(fmt.Errorf("create the vercel deployment: %w", err))
	}
	return d.ID, nil
}

// upload sends every file of the commit to Vercel, a few at a time, and
// lists them, relative to the repository's root. The whole repository
// goes, not just the app's directory, so a workspace's shared packages
// are there when Vercel builds.
func (v *Vercel) upload(ctx context.Context, tok vercel.Token, job *Job) ([]File, error) {
	src, _, err := job.Source(ctx)
	if err != nil {
		return nil, err
	}
	defer src.Close() // read-only
	zr, err := gzip.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("read the source: %w", err)
	}
	defer zr.Close() // read-only
	tr := tar.NewReader(zr)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type upload struct {
		sha  string
		data []byte
	}
	work := make(chan upload)
	var wg sync.WaitGroup
	var once sync.Once
	var failed error
	for range uploads {
		wg.Go(func() {
			for u := range work {
				if err := v.Client.UploadFile(ctx, tok, u.sha, u.data); err != nil {
					once.Do(func() { failed = err; cancel() })
				}
			}
		})
	}
	var files []File
	seen := map[string]bool{}
	var readErr error
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = fmt.Errorf("read the source: %w", err)
			break
		}
		if h.Typeflag != tar.TypeReg || h.Size > maxUploadFile {
			continue
		}
		_, rel, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || rel == "" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			readErr = fmt.Errorf("read the source: %w", err)
			break
		}
		sum := sha1.Sum(data) //nolint:gosec // G401: Vercel names file content by its SHA-1
		sha := hex.EncodeToString(sum[:])
		files = append(files, File{File: rel, SHA: sha, Size: int64(len(data))})
		if seen[sha] {
			continue
		}
		seen[sha] = true
		select {
		case work <- upload{sha: sha, data: data}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	switch {
	case failed != nil:
		return nil, fmt.Errorf("upload to vercel: %w", failed)
	case readErr != nil:
		return nil, readErr
	}
	return files, nil
}

// File is one file of a deployment.
type File = vercel.File

// removePasses bounds how often Remove lists a target's deployments and
// deletes them, since a deployment can be started while it works.
const removePasses = 5

// Remove implements Executor: it deletes the Vercel project, and with it
// its deployments. A native service removes only its own environment:
// the deployments, variables and custom environment of its target, and
// the project too once no other environment uses it.
func (v *Vercel) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.Subject == planning.ResourceSubject || job.Action.Resource != nil {
		return v.removeResource(ctx, job, b)
	}
	if b.ExternalID == "" {
		return nil
	}
	projectID, ref, native := planning.ParseVercelRef(b.ExternalID)
	if !native {
		return v.Client.DeleteProject(ctx, token(job), b.ExternalID)
	}
	return v.removeNative(ctx, job, b, projectID, refTarget(ref))
}

func (v *Vercel) removeNative(ctx context.Context, job *Job, b *planning.Binding, projectID string, target vercel.Target) error {
	tok := token(job)
	drained := false
	for range removePasses {
		deployments, err := v.Client.TargetDeployments(ctx, tok, projectID, target)
		if vercel.IsNotFound(err) {
			return v.forgetNative(ctx, job, b, projectID, false)
		}
		if err != nil {
			return err
		}
		if len(deployments) == 0 {
			drained = true
			break
		}
		for _, d := range deployments {
			if err := v.Client.DeleteDeployment(ctx, tok, d.ID); err != nil {
				return fmt.Errorf("delete the vercel deployment %s: %w", d.ID, err)
			}
		}
	}
	if !drained {
		left, err := v.Client.TargetDeployments(ctx, tok, projectID, target)
		if err != nil {
			return err
		}
		if len(left) > 0 {
			return fmt.Errorf("the vercel environment still has %d deployments after %d passes", len(left), removePasses)
		}
	}
	if err := v.Client.DeleteEnv(ctx, tok, projectID, target); err != nil {
		return fmt.Errorf("remove the vercel environment's variables: %w", err)
	}
	if target.CustomID != "" {
		if err := v.Client.DeleteCustomEnvironment(ctx, tok, projectID, target.CustomID); err != nil {
			return fmt.Errorf("remove the vercel custom environment: %w", err)
		}
	}
	return v.forgetNative(ctx, job, b, projectID, true)
}

// forgetNative deletes the binding's record and, when no other environment
// uses the project, the project. Without Environments nothing says who
// shares the project, so it stays.
func (v *Vercel) forgetNative(ctx context.Context, job *Job, b *planning.Binding, projectID string, projectExists bool) error {
	if v.Environments == nil {
		return nil
	}
	envID := b.EnvironmentID
	if envID == "" {
		envID = job.Deployment.EnvironmentID
	}
	connID := b.ConnectionID
	if connID == "" && job.Connection != nil {
		connID = job.Connection.ID
	}
	others, err := otherNativeUsers(ctx, v.Environments.Store, "vercel", b.OrgID, projectID, envID, b.Name)
	if err != nil {
		return fmt.Errorf("check who uses the vercel project: %w", err)
	}
	row := &ProviderEnvironment{OrgID: b.OrgID, EnvironmentID: envID, Provider: "vercel", ConnectionID: connID, Scope: b.Name}
	if err := v.Environments.Store.DeleteProviderEnvironment(ctx, row); err != nil {
		return fmt.Errorf("forget the native vercel environment: %w", err)
	}
	if others || !projectExists {
		return nil
	}
	return v.Client.DeleteProject(ctx, token(job), projectID)
}

// Observe implements Executor: the project, and which deployment its
// production domain serves.
func (v *Vercel) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	if b.Subject == planning.ResourceSubject {
		return v.observeResource(ctx, conn, credential, b)
	}
	tok := vercel.Token{Value: strings.TrimSpace(string(credential))}
	if conn != nil {
		tok.TeamID = conn.Details["team_id"]
	}
	if projectID, ref, native := planning.ParseVercelRef(b.ExternalID); native {
		return v.observeNative(ctx, tok, projectID, refTarget(ref))
	}
	p, err := v.Client.GetProject(ctx, tok, b.ExternalID)
	if vercel.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: p.Production()}, nil
}

// observeNative reads the project's one environment: the newest deployment
// of its target, not the project's production.
func (v *Vercel) observeNative(ctx context.Context, tok vercel.Token, projectID string, target vercel.Target) (*Observation, error) {
	if _, err := v.Client.GetProject(ctx, tok, projectID); err != nil {
		if vercel.IsNotFound(err) {
			return &Observation{}, nil
		}
		return nil, err
	}
	if target.CustomID != "" {
		if _, err := v.Client.GetCustomEnvironment(ctx, tok, projectID, target.CustomID); err != nil {
			if vercel.IsNotFound(err) {
				return &Observation{}, nil
			}
			return nil, err
		}
	}
	deployments, err := v.Client.TargetDeployments(ctx, tok, projectID, target)
	if err != nil {
		return nil, err
	}
	obs := &Observation{Exists: true}
	if len(deployments) > 0 {
		obs.Revision = deployments[0].ID
	}
	return obs, nil
}
