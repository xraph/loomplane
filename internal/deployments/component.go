package deployments

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"go.jetify.com/typeid/v2"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

var _ planning.TargetDialer = (*OwnedTarget)(nil)

// TargetDial opens connections from a target's network, for checking an
// external endpoint the way the target will reach it. A server dials over
// its SSH connection, opened and checked the way a deployment opens it, so
// the host key, Docker identity and reviewed settings must still match;
// closing the io.Closer ends that connection. A cluster's checks dial
// from Loomplane: nothing runs inside the cluster to dial from.
func (e *OwnedTarget) TargetDial(ctx context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
	if e.Credentials == nil {
		return nil, nil, ErrNoCredential
	}
	c, _, err := e.Credentials.Credential(ctx, orgID, connectionID)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.ID != connectionID || c.OrgID != orgID || c.Status != connections.StatusReady {
		return nil, nil, errors.New("the target connection isn't ready")
	}
	if c.Cloud != e.Cloud {
		return nil, nil, fmt.Errorf("%s is a %s connection, not a %s one", c.Name, c.Cloud, e.Cloud)
	}
	switch e.Cloud {
	case connections.Server:
		settings, _ := json.Marshal(planning.ReviewedConnectionSettings(c.Cloud, c.Details)) //nolint:errcheck // string maps always encode
		labels := map[string]string{owned.LabelOrg: orgID, owned.LabelConnection: connectionID, owned.LabelSettings: string(settings)}
		client, err := e.serverSession(orgID, connectionID)(ctx, labels)
		if err != nil {
			return nil, nil, err
		}
		return client.DialContext, client, nil
	case connections.Kubernetes:
		return (&net.Dialer{}).DialContext, noClose{}, nil
	}
	return nil, nil, fmt.Errorf("endpoints can't be checked from a %s target", e.Cloud)
}

// noClose is an io.Closer with nothing to close.
type noClose struct{}

func (noClose) Close() error { return nil }

// componentAddressField is where a process component keeps the address of
// each of its ports, as JSON: {"http": "http://lp-inst-...:8080"}.
const componentAddressField = "address"

// errRecipeChanged is a plan made from a recipe this Loomplane no longer
// has.
var errRecipeChanged = errors.New("the catalog recipe changed; create a new plan")

// kinds is the catalog composed resources are checked against.
func (e *OwnedTarget) kinds() *recipes.Registry {
	if e.Kinds != nil {
		return e.Kinds
	}
	return recipes.Default()
}

// hooks are a kind's hooks, or NoHooks.
func (e *OwnedTarget) hooks(kind string) CatalogHooks {
	if h := e.Hooks[kind]; h != nil {
		return h
	}
	return NoHooks{}
}

// componentService is the service a composed resource's process component
// runs as: private, health-checked on its readiness port, from its pinned
// image.
func componentService(cfg *planning.ResourceConfig) *planning.ServiceConfig {
	c := cfg.Component.Component
	svc := &planning.ServiceConfig{
		Provider: cfg.Provider, Target: cfg.Target, TargetSettings: cfg.TargetSettings,
		Workload: provider.HTTPService, Execution: provider.Container, Public: false,
		Component: cfg.Component, BuildTarget: &planning.BuildTarget{Image: c.Image},
	}
	if c.Readiness != nil {
		svc.HealthPath = c.Readiness.Path
		if port, ok := componentPort(c.Component, c.Readiness.Port); ok {
			svc.Port = port.Container
		}
	}
	return svc
}

// componentSpec runs a component as its recipe declares: its command, every
// port, its resources and its catalog labels. Only a component with a
// volume is stateful.
func componentSpec(sp *cpprovider.ServiceSpec, labels map[string]string, cfg *planning.ComponentConfig) {
	c := cfg.Component
	sp.Command, sp.Args = c.Command, c.Args
	sp.Ports = make([]cpprovider.PortSpec, 0, len(c.Ports))
	for _, port := range c.Ports {
		sp.Ports = append(sp.Ports, cpprovider.PortSpec{Container: port.Container, Protocol: "tcp"})
	}
	sp.Resources = cpprovider.ResourceSpec{CPUMillis: cmp.Or(c.Resources.CPUMillis, 500), MemoryMB: cmp.Or(c.Resources.MemoryMB, 256)}
	labels[owned.LabelCatalogKind] = cfg.Kind
	labels[owned.LabelCatalogResource] = cfg.Resource
	labels[owned.LabelCatalogComponent] = c.Name
	if c.Readiness != nil {
		if port, ok := componentPort(c.Component, c.Readiness.Port); ok {
			labels[owned.LabelHealthPort] = strconv.Itoa(port.Container)
		}
	}
	if c.Volume != nil {
		sp.Volumes = []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: c.Volume.MountPath, SizeMB: c.Volume.KubernetesSizeMB}}
		labels[owned.LabelStateful] = "true"
	}
}

func componentPort(c recipes.Component, name string) (recipes.Port, bool) {
	i := slices.IndexFunc(c.Ports, func(p recipes.Port) bool { return p.Name == name })
	if i < 0 {
		return recipes.Port{}, false
	}
	return c.Ports[i], true
}

// sameJSON reports whether two values are stored the same way. A plan's
// configuration comes back from JSON, where an empty list and none are
// one thing.
func sameJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// checkComponent resolves the component's kind again and refuses a
// configuration the catalog wouldn't produce now.
func (e *OwnedTarget) checkComponent(cfg *planning.ResourceConfig) (recipes.Kind, error) {
	if cfg == nil || cfg.Component == nil || !planning.OwnedCloud(cfg.Provider) || cfg.Target == "" {
		return recipes.Kind{}, errors.New("a catalog component needs a reviewed connected target")
	}
	c := cfg.Component
	kind, ok := e.kinds().Kind(c.Kind)
	if !ok || cfg.Kind != c.Kind {
		return recipes.Kind{}, errRecipeChanged
	}
	composition, err := e.kinds().Resolve(c.Kind, cfg.Version, cfg.TargetSettings["architecture"], cfg.Provider)
	if err != nil {
		return recipes.Kind{}, err
	}
	i := slices.IndexFunc(composition.Components, func(r recipes.ResolvedComponent) bool { return r.Name == c.Component.Name })
	if i < 0 {
		return recipes.Kind{}, errRecipeChanged
	}
	resolved := composition.Components[i]
	expected := planning.ComponentConfig{Kind: composition.Kind, Resource: c.Resource, Revision: composition.Revision, Component: resolved, Dependencies: c.Dependencies, Protection: planning.ComponentProtection(resolved.Component)}
	if !sameJSON(expected, *c) {
		return recipes.Kind{}, errRecipeChanged
	}
	return kind, nil
}

// applyComponent runs one component of a composed resource. A process runs
// through the service path, privately and without a database adapter;
// what it reads from its dependencies, siblings and generated secrets is
// resolved first, and an external endpoint is checked again from the
// target before anything changes.
func (e *OwnedTarget) applyComponent(ctx context.Context, job *Job) (*Outcome, error) {
	if !planning.OwnedCloud(e.Cloud) || job.Connection == nil || job.Deployment == nil {
		return nil, ErrNoExecutor
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	cfg := job.Action.Resource
	kind, err := e.checkComponent(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Provider != e.Cloud || job.Connection.Cloud != e.Cloud || cfg.Target != job.Connection.ID {
		return nil, errors.New("the component's target differs from the reviewed connection")
	}
	component := cfg.Component.Component
	if component.Role == recipes.RoleJob {
		return e.runComponentJob(ctx, job, kind)
	}
	if e.Secrets == nil || job.Keep == nil {
		return nil, ErrNoCredential
	}
	run, secret, err := e.componentEnv(ctx, job, kind)
	if err != nil {
		return nil, err
	}
	files, err := e.renderFiles(ctx, kind, run)
	if err != nil {
		return nil, err
	}
	copyJob := *job
	copyJob.Action.Resource = nil
	copyJob.Action.Service = componentService(cfg)
	copyJob.Env, copyJob.Secrets = maps.Clone(run.Env), secret
	copyJob.files = files
	out, err := e.Apply(ctx, &copyJob)
	if err != nil {
		return nil, err
	}
	iid, err := id.Parse(out.ExternalID)
	if err != nil {
		return nil, err
	}
	address, err := componentAddress(cfg, iid)
	if err != nil {
		return nil, err
	}
	if keepErr := job.Keep(ctx, componentAddressField, address); keepErr != nil {
		return nil, keepErr
	}
	if readyErr := e.hooks(kind.Name).Ready(ctx, run); readyErr != nil {
		return nil, fmt.Errorf("%s is ready, and setting it up failed: %w", job.Action.Name, readyErr)
	}
	return out, nil
}

// A job's checkpoint says whether its run is running or succeeded.
const (
	jobRunning   = "running"
	jobSucceeded = "succeeded"
)

type jobCheckpoint struct {
	Job   string `json:"job"`
	State string `json:"state"`
}

// defaultJobTimeout is how long a job runs when its recipe doesn't say.
const defaultJobTimeout = 10 * time.Minute

// componentJobName names a job's run for one deployment's action, so a
// retry of the step finds it again.
func componentJobName(deploymentID, actionID string) string {
	sum := sha256.Sum256([]byte(deploymentID + "/" + actionID))
	return "lp-job-" + hex.EncodeToString(sum[:])[:12]
}

// runComponentJob runs a job component to completion on the target,
// without Ctrl Plane: nothing keeps running once it's done. It gets what a
// process would, its secrets through runtime values kept only while it
// runs. A job that exits with anything but 0 fails the step, so the
// components after it don't start. A retry of a step whose job succeeded
// doesn't run it again.
func (e *OwnedTarget) runComponentJob(ctx context.Context, job *Job, kind recipes.Kind) (out *Outcome, err error) {
	cfg := job.Action.Resource
	component := cfg.Component.Component
	name := componentJobName(job.Deployment.ID, job.Action.ID)
	done := &Outcome{ExternalID: "job/" + name, Revision: component.Image}
	var resumed jobCheckpoint
	if len(job.Resume) > 0 && json.Unmarshal(job.Resume, &resumed) == nil && resumed.Job == name && resumed.State == jobSucceeded {
		return done, nil
	}
	backend, err := e.registeredBackend(e.Register(job.Deployment.OrgID, job.Connection.ID))
	if err != nil {
		return nil, err
	}
	jobs, ok := backend.(owned.JobBackend)
	if !ok {
		return nil, fmt.Errorf("%s is a job, and this %s target can't run catalog jobs", job.Action.Name, e.Cloud)
	}
	if e.Secrets == nil || job.Keep == nil || job.Checkpoint == nil {
		return nil, ErrNoCredential
	}
	run, secret, err := e.componentEnv(ctx, job, kind)
	if err != nil {
		return nil, err
	}
	files, err := e.renderFiles(ctx, kind, run)
	if err != nil {
		return nil, err
	}
	ref, err := typeid.Generate("runtime")
	if err != nil {
		return nil, err
	}
	state := ownedState{RuntimeRef: resourceKey(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, ref.String())}
	values := map[string]string{}
	for key := range secret {
		values[key] = run.Env[key]
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	if putErr := e.Secrets.Put(ctx, state.RuntimeRef, raw); putErr != nil {
		return nil, putErr
	}
	copyJob := *job
	copyJob.Action.Resource = nil
	copyJob.Action.Service = componentService(cfg)
	copyJob.Env, copyJob.Secrets = maps.Clone(run.Env), secret
	copyJob.files = files
	sp, labels := ownedSpec(&copyJob, component.Image, state)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = errors.Join(err, e.deleteRuntimeFiles(cleanup, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, labels))
		err = errors.Join(err, e.deleteRuntime(cleanup, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, state.RuntimeRef))
		if err != nil {
			out = nil
		}
	}()
	if putErr := e.keepRuntimeFiles(ctx, state.RuntimeRef, files); putErr != nil {
		return nil, putErr
	}
	// The recipe pinned the image for the target's architecture.
	labels[owned.LabelArchitecture] = cfg.TargetSettings["architecture"]
	save := func(s string) error {
		raw, _ := json.Marshal(jobCheckpoint{Job: name, State: s}) //nolint:errcheck // a plain struct always encodes
		return job.Checkpoint(raw)
	}
	if saveErr := save(jobRunning); saveErr != nil {
		return nil, saveErr
	}
	job.Progress(StepDeploying, "Running the "+component.Name+" job")
	result, err := jobs.RunJob(ctx, name, sp, labels, cmp.Or(component.Timeout, defaultJobTimeout))
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		// The target redacted the output; this catches anything it missed.
		if output := owned.JobOutput([]byte(result.Output), values); output != "" {
			return nil, fmt.Errorf("%s exited with code %d: %s", component.Name, result.ExitCode, output)
		}
		return nil, fmt.Errorf("%s exited with code %d", component.Name, result.ExitCode)
	}
	if saveErr := save(jobSucceeded); saveErr != nil {
		return nil, saveErr
	}
	return done, nil
}

// runtimeFilesSuffix is where a component's rendered files are kept in
// Vault: under its runtime values' reference, with "/files" after it.
const runtimeFilesSuffix = "/files"

// runtimeFile is one rendered file as it's kept in Vault.
type runtimeFile struct {
	Path string      `json:"path"`
	Mode fs.FileMode `json:"mode"`
	Data []byte      `json:"data"`
}

// renderFiles asks a component's kind for the files it declares, on each
// apply, before it starts. A declared file the hooks leave out, or one
// they add, stops it. What comes back is in path order.
func (e *OwnedTarget) renderFiles(ctx context.Context, kind recipes.Kind, run ComponentRun) ([]runtimeFile, error) {
	declared := run.Component.Files
	if len(declared) == 0 {
		return nil, nil
	}
	rendered, err := e.hooks(kind.Name).Files(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("%s couldn't render %s's files: %w", kind.Name, run.Component.Name, err)
	}
	files := make([]runtimeFile, 0, len(declared))
	for _, f := range declared {
		data, ok := rendered[f.Path]
		if !ok {
			return nil, fmt.Errorf("%s did not render %s", kind.Name, f.Path)
		}
		files = append(files, runtimeFile{Path: f.Path, Mode: cmp.Or(f.Mode.Perm(), owned.DefaultFileMode), Data: data})
	}
	for _, p := range slices.Sorted(maps.Keys(rendered)) {
		if !slices.ContainsFunc(declared, func(f recipes.File) bool { return f.Path == p }) {
			return nil, fmt.Errorf("%s rendered %s, which %s doesn't declare", kind.Name, p, run.Component.Name)
		}
	}
	slices.SortFunc(files, func(a, b runtimeFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// filesRevision is a digest of rendered files: each one's path, mode and
// bytes, in path order.
func filesRevision(files []runtimeFile) string {
	sum := sha256.New()
	for _, f := range files {
		// Writing to a hash never fails.
		_, _ = fmt.Fprintf(sum, "%s\x00%o\x00%d\x00", f.Path, f.Mode, len(f.Data))
		_, _ = sum.Write(f.Data)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// keepRuntimeFiles keeps a component's rendered files beside its runtime
// values. A component without files keeps nothing.
func (e *OwnedTarget) keepRuntimeFiles(ctx context.Context, ref string, files []runtimeFile) error {
	if len(files) == 0 {
		return nil
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	return e.Secrets.Put(ctx, ref+runtimeFilesSuffix, raw)
}

// runtimeFiles reads the rendered files kept beside the runtime values at
// ref into values.
func (e *OwnedTarget) runtimeFiles(ctx context.Context, ref string, values *owned.RuntimeValues) error {
	raw, err := e.Secrets.Get(ctx, ref+runtimeFilesSuffix)
	if err != nil {
		return fmt.Errorf("read the rendered files: %w", err)
	}
	var files []runtimeFile
	if decodeErr := json.Unmarshal(raw, &files); decodeErr != nil {
		return errors.New("the kept rendered files can't be read")
	}
	values.Files, values.FileModes = map[string][]byte{}, map[string]fs.FileMode{}
	for _, f := range files {
		values.Files[f.Path], values.FileModes[f.Path] = f.Data, f.Mode
	}
	return nil
}

// deleteRuntimeFiles deletes the rendered files kept beside the runtime
// values labels name, when they have any.
func (e *OwnedTarget) deleteRuntimeFiles(ctx context.Context, org, environment, service string, labels map[string]string) error {
	ref := labels[owned.LabelRuntime]
	if ref == "" || labels[owned.LabelRuntimeFiles] != "true" {
		return nil
	}
	if err := validateRuntimeRef(org, environment, service, ref); err != nil {
		return err
	}
	if e.Secrets == nil {
		return ErrNoCredential
	}
	return e.Secrets.Delete(ctx, ref+runtimeFilesSuffix)
}

// declaresFiles reports whether a binding is a catalog component with
// rendered files.
func declaresFiles(b *planning.Binding) bool {
	resource := boundResource(b)
	return resource != nil && resource.Component != nil && len(resource.Component.Component.Files) > 0
}

// componentEnv resolves what a component gets: every derived or generated
// value is secret, so only plain values and sibling addresses reach Ctrl
// Plane. External endpoints are checked before anything is generated.
func (e *OwnedTarget) componentEnv(ctx context.Context, job *Job, kind recipes.Kind) (ComponentRun, map[string]bool, error) {
	cfg := job.Action.Resource
	d := job.Deployment
	run := ComponentRun{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Environment: d.Environment, PlanID: d.PlanID, DeploymentID: d.ID,
		Resource: cfg.Component.Resource, Kind: kind.Name, Provider: e.Cloud, Component: cfg.Component.Component,
		Env: map[string]string{}, Secrets: map[string][]byte{},
	}
	secret := map[string]bool{}
	external := map[string]string{}
	for _, v := range run.Component.Env {
		if v.From == nil || v.From.Dependency == "" || external[v.From.Dependency] != "" {
			continue
		}
		choice := cfg.Component.Dependencies[v.From.Dependency]
		if choice.Mode != planning.DependencyExternal {
			continue
		}
		dsn, err := e.recheckEndpoint(ctx, job, kind, v.From.Dependency, choice.External)
		if err != nil {
			return run, nil, err
		}
		external[v.From.Dependency] = dsn
	}
	// The component's hooks may need any of the composition's secrets, not
	// only the ones its variables read.
	for _, s := range kind.Secrets {
		value, err := e.generatedSecret(ctx, job, s)
		if err != nil {
			return run, nil, err
		}
		run.Secrets[s.Name] = value
	}
	for _, v := range run.Component.Env {
		switch {
		case v.Secret != "":
			value, ok := run.Secrets[v.Secret]
			if !ok {
				return run, nil, fmt.Errorf("%s has no generated secret %s", kind.Title, v.Secret)
			}
			run.Env[v.Name], secret[v.Name] = string(value), true
		case v.From != nil && v.From.Dependency != "":
			address, on, err := e.dependencyURL(ctx, job, v.From.Dependency, external)
			if err != nil {
				return run, nil, err
			}
			if !on {
				continue
			}
			value, err := urlField(address, v.From.Field)
			if err != nil {
				return run, nil, fmt.Errorf("%s's %s dependency: %w", run.Resource, v.From.Dependency, err)
			}
			run.Env[v.Name], secret[v.Name] = value, true
		case v.From != nil:
			value, err := e.siblingAddress(ctx, job, *v.From)
			if err != nil {
				return run, nil, err
			}
			run.Env[v.Name] = value
		default:
			run.Env[v.Name] = v.Value
		}
	}
	return run, secret, nil
}

// recheckEndpoint checks an external endpoint from the component's target
// again, as the plan did, and returns its DSN. What fails names the
// endpoint and never shows the DSN.
func (e *OwnedTarget) recheckEndpoint(ctx context.Context, job *Job, kind recipes.Kind, dependency, endpointID string) (string, error) {
	i := slices.IndexFunc(kind.Dependencies, func(d recipes.Dependency) bool { return d.Name == dependency })
	if i < 0 || kind.Dependencies[i].External == nil {
		return "", fmt.Errorf("%s can't connect %s to an external endpoint", kind.Title, dependency)
	}
	if e.Endpoints == nil || e.Checker == nil {
		return "", errors.New("this Loomplane can't check external endpoints")
	}
	org := job.Deployment.OrgID
	endpoint, dsn, err := e.Endpoints.Resolve(ctx, org, endpointID)
	if errors.Is(err, catalogconnect.ErrNotFound) {
		return "", fmt.Errorf("endpoint %s isn't in this organization any more; connect another one and plan again", endpointID)
	}
	if err != nil {
		return "", err
	}
	dialFrom := e.endpointDial
	if dialFrom == nil {
		dialFrom = e.TargetDial
	}
	dial, closer, err := dialFrom(ctx, org, job.Connection.ID)
	if err != nil {
		return "", fmt.Errorf("endpoint %s couldn't be checked: the target connection failed: %w", endpoint.Name, err)
	}
	report, err := e.Checker.Check(ctx, org, endpointID, *kind.Dependencies[i].External, dial)
	if closer != nil {
		if closeErr := closer.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return "", fmt.Errorf("endpoint %s couldn't be checked: %s", endpoint.Name, withoutDSN(err.Error(), dsn))
	}
	if problem := endpointProblem(endpoint, report); problem != "" {
		return "", errors.New(withoutDSN(problem, dsn))
	}
	return dsn, nil
}

// endpointProblem words what a check found wrong with an endpoint, naming
// it by its name, or returns "".
func endpointProblem(endpoint *catalogconnect.Endpoint, report *catalogconnect.Report) string {
	if report == nil {
		return fmt.Sprintf("endpoint %s couldn't be checked from the target", endpoint.Name)
	}
	address := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)) + " "
	var problems []string
	for _, p := range report.Problems {
		if rest, ok := strings.CutPrefix(p, address); ok {
			problems = append(problems, fmt.Sprintf("endpoint %s %s", endpoint.Name, rest))
		} else {
			problems = append(problems, fmt.Sprintf("endpoint %s can't be used: %s", endpoint.Name, p))
		}
	}
	for _, extension := range report.Missing {
		problems = append(problems, fmt.Sprintf("endpoint %s lacks the %s extension", endpoint.Name, extension))
	}
	if len(problems) == 0 && report.Version == "" {
		problems = append(problems, fmt.Sprintf("endpoint %s didn't say which version it runs", endpoint.Name))
	}
	return strings.Join(problems, "; ")
}

// withoutDSN removes a DSN from a message, in case anything quoted it.
func withoutDSN(message, dsn string) string {
	if dsn == "" {
		return message
	}
	return strings.ReplaceAll(message, dsn, "[redacted]")
}

// generatedSecret is one of the composition's generated secrets, kept once
// for the whole composed resource. It's generated the first time a
// component needs it, which for a deployed component can be a later
// revision adding it. One the deployed component already had and that's
// gone fails rather than get a new one its data doesn't know.
func (e *OwnedTarget) generatedSecret(ctx context.Context, job *Job, s recipes.GeneratedSecret) ([]byte, error) {
	name := s.Name
	key := resourceKey(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Resource.Component.Resource, "secret:"+name)
	kept, err := e.Secrets.Get(ctx, key)
	if err == nil && len(kept) > 0 {
		return kept, nil
	}
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return nil, err
	}
	if hadSecret(job, name) {
		return nil, fmt.Errorf("the generated secret %s is missing; recover it before changing the service", name)
	}
	value, err := generateSecret(s)
	if err != nil {
		return nil, err
	}
	if putErr := e.Secrets.Put(ctx, key, value); putErr != nil {
		return nil, putErr
	}
	// Steps apply one action at a time, so nothing else keeps one in
	// between; reading it back still hands every component what's kept.
	return e.Secrets.Get(ctx, key)
}

// hadSecret reports whether the deployed component already had a
// generated secret: its variables read it, or it was deployed from this
// same revision, which declared it, so it was kept then.
func hadSecret(job *Job, name string) bool {
	was := boundResource(job.Binding)
	if was == nil || was.Component == nil {
		return false
	}
	if was.Component.Kind == job.Action.Resource.Component.Kind && was.Component.Revision == job.Action.Resource.Component.Revision {
		return true
	}
	return slices.ContainsFunc(was.Component.Component.Env, func(v recipes.EnvVar) bool { return v.Secret == name })
}

func generateSecret(s recipes.GeneratedSecret) ([]byte, error) {
	raw := make([]byte, cmp.Or(s.Bytes, 32))
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	switch s.Encoding {
	case "", "base64url":
		return []byte(base64.RawURLEncoding.EncodeToString(raw)), nil
	case "hex":
		return []byte(hex.EncodeToString(raw)), nil
	}
	return nil, fmt.Errorf("the generated secret %s has an unknown encoding %q", s.Name, s.Encoding)
}

// dependencyURL is the address of what a dependency connects to, with its
// credentials: a provisioned or connected resource's kept address, or an
// external endpoint's DSN. on is false for an optional dependency that's
// off.
func (e *OwnedTarget) dependencyURL(ctx context.Context, job *Job, dependency string, external map[string]string) (address string, on bool, err error) {
	cfg := job.Action.Resource.Component
	choice, ok := cfg.Dependencies[dependency]
	if !ok {
		return "", false, fmt.Errorf("the plan doesn't say where %s gets %s; plan again", cfg.Resource, dependency)
	}
	switch choice.Mode {
	case planning.DependencyOff:
		return "", false, nil
	case planning.DependencyExternal:
		return external[dependency], true, nil
	case planning.DependencyNew, planning.DependencyResource:
		raw, getErr := e.Secrets.Get(ctx, resourceKey(job.Deployment.OrgID, job.Deployment.EnvironmentID, choice.Resource, ResourceURL))
		if errors.Is(getErr, ErrNoCredential) {
			return "", false, fmt.Errorf("%s needs %s, which hasn't been provisioned", job.Action.Name, choice.Resource)
		}
		if getErr != nil {
			return "", false, fmt.Errorf("%s needs %s: %w", job.Action.Name, choice.Resource, getErr)
		}
		return string(raw), true, nil
	}
	return "", false, fmt.Errorf("%s gets %s in a way this Loomplane doesn't know: %q", cfg.Resource, dependency, choice.Mode)
}

// defaultPorts are the ports a dependency's address may leave out.
var defaultPorts = map[string]string{"postgres": "5432", "postgresql": "5432", "mysql": "3306", "redis": "6379", "rediss": "6379"}

// urlField reads one field of a dependency's address. Nothing it returns
// quotes the address, which holds a password.
func urlField(address, field string) (string, error) {
	if field == recipes.FieldURL {
		return address, nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "", errors.New("its address isn't a URL")
	}
	port := u.Port()
	if port == "" {
		port = defaultPorts[u.Scheme]
	}
	switch field {
	case recipes.FieldHost:
		return u.Hostname(), nil
	case recipes.FieldPort, recipes.FieldHostPort:
		if port == "" {
			return "", errors.New("its address has no port")
		}
		if field == recipes.FieldPort {
			return port, nil
		}
		return net.JoinHostPort(u.Hostname(), port), nil
	case recipes.FieldUsername:
		return u.User.Username(), nil
	case recipes.FieldPassword:
		password, _ := u.User.Password()
		return password, nil
	case recipes.FieldDatabase:
		return strings.TrimPrefix(u.Path, "/"), nil
	}
	return "", fmt.Errorf("a dependency has no field %q", field)
}

// siblingAddress reads a port of a component that started before this
// one: "url:<port>" is its URL, "address:<port>" its host and port.
func (e *OwnedTarget) siblingAddress(ctx context.Context, job *Job, ref recipes.Ref) (string, error) {
	resource := job.Action.Resource.Component.Resource
	name := planning.ComponentActionName(resource, ref.Component)
	addresses, err := e.componentAddresses(ctx, job.Deployment.OrgID, job.Deployment.EnvironmentID, name)
	if err != nil {
		return "", fmt.Errorf("%s needs %s: %w", job.Action.Name, name, err)
	}
	return addressField(addresses, name, ref.Field)
}

func (e *OwnedTarget) componentAddresses(ctx context.Context, org, environment, name string) (map[string]string, error) {
	raw, err := e.Secrets.Get(ctx, resourceKey(org, environment, name, componentAddressField))
	if errors.Is(err, ErrNoCredential) {
		return nil, errors.New("it hasn't started yet")
	}
	if err != nil {
		return nil, err
	}
	var addresses map[string]string
	if decodeErr := json.Unmarshal(raw, &addresses); decodeErr != nil {
		return nil, fmt.Errorf("read its address: %w", decodeErr)
	}
	return addresses, nil
}

func addressField(addresses map[string]string, name, field string) (string, error) {
	kind, port, _ := strings.Cut(field, ":")
	value, ok := addresses[port]
	if !ok {
		return "", fmt.Errorf("%s has no %s port", name, port)
	}
	switch kind {
	case "url":
		return value, nil
	case "address":
		u, err := url.Parse(value)
		if err != nil {
			return "", fmt.Errorf("%s's %s address isn't a URL", name, port)
		}
		return u.Host, nil
	}
	return "", fmt.Errorf("a component has no field %q", field)
}

// componentAddress is where the composition's other components reach each
// port of a process: its container name on a server's network, its
// Service in the namespace on a cluster.
func componentAddress(cfg *planning.ResourceConfig, iid id.ID) ([]byte, error) {
	host := server.ContainerName(iid)
	switch cfg.Provider {
	case connections.Server:
	case connections.Kubernetes:
		namespace := cfg.TargetSettings["namespace"]
		if namespace == "" || len(validation.IsDNS1123Label(namespace)) > 0 {
			return nil, errors.New("check the component's namespace")
		}
		host = kubernetes.WorkloadName(iid) + "." + namespace + ".svc"
	default:
		return nil, errors.New("the component has no qualified target")
	}
	addresses := map[string]string{}
	for _, port := range cfg.Component.Component.Ports {
		addresses[port.Name] = cmp.Or(port.Protocol, "tcp") + "://" + net.JoinHostPort(host, strconv.Itoa(port.Container))
	}
	return json.Marshal(addresses)
}

// checkComposition refuses a composed resource whose release, outputs or
// secrets the catalog wouldn't plan now.
func (e *OwnedTarget) checkComposition(cfg *planning.ResourceConfig) (recipes.Kind, error) {
	if cfg == nil || cfg.Composition == nil || !planning.OwnedCloud(cfg.Provider) || cfg.Target == "" {
		return recipes.Kind{}, errors.New("a composed catalog resource needs a reviewed connected target")
	}
	c := cfg.Composition
	kind, ok := e.kinds().Kind(c.Kind)
	if !ok || cfg.Kind != c.Kind {
		return recipes.Kind{}, errRecipeChanged
	}
	composition, err := e.kinds().Resolve(c.Kind, c.Version, cfg.TargetSettings["architecture"], cfg.Provider)
	if err != nil {
		return recipes.Kind{}, err
	}
	if composition.Revision != c.Revision || !sameJSON(kind.Outputs, c.Outputs) || !sameJSON(kind.Secrets, c.Secrets) {
		return recipes.Kind{}, errRecipeChanged
	}
	return kind, nil
}

// applyComposition publishes a composed resource's outputs, once its
// components run: the primary under "url", which is what a service that
// needs it gets, and the others as "output:<name>". Outputs issued per
// consuming service aren't kept here.
func (e *OwnedTarget) applyComposition(ctx context.Context, job *Job) (*Outcome, error) {
	if !planning.OwnedCloud(e.Cloud) || job.Connection == nil || job.Deployment == nil {
		return nil, ErrNoExecutor
	}
	cfg := job.Action.Resource
	if _, err := e.checkComposition(cfg); err != nil {
		return nil, err
	}
	if cfg.Provider != e.Cloud || job.Connection.Cloud != e.Cloud || cfg.Target != job.Connection.ID {
		return nil, errors.New("the composed resource's target differs from the reviewed connection")
	}
	if e.Secrets == nil || job.Keep == nil {
		return nil, ErrNoCredential
	}
	for i, output := range cfg.Composition.Outputs {
		if output.PerConsumer || output.From == nil {
			continue
		}
		if output.From.Component == "" {
			return nil, fmt.Errorf("%s's %s output comes from a dependency, which this Loomplane can't publish yet", job.Action.Name, output.Name)
		}
		name := planning.ComponentActionName(job.Action.Name, output.From.Component)
		addresses, err := e.componentAddresses(ctx, job.Deployment.OrgID, job.Deployment.EnvironmentID, name)
		if err != nil {
			return nil, fmt.Errorf("%s's %s output needs %s: %w", job.Action.Name, output.Name, name, err)
		}
		value, err := addressField(addresses, name, output.From.Field)
		if err != nil {
			return nil, err
		}
		field := ResourceURL
		if i > 0 {
			field = "output:" + output.Name
		}
		if keepErr := job.Keep(ctx, field, []byte(value)); keepErr != nil {
			return nil, keepErr
		}
	}
	return &Outcome{ExternalID: "catalog/" + job.Action.Name}, nil
}

// removeComposition forgets a composed resource once its components are
// gone: its kind's hooks first, then its outputs and generated secrets.
func (e *OwnedTarget) removeComposition(ctx context.Context, job *Job, b *planning.Binding, c *planning.CompositionConfig) error {
	if job.Deployment == nil || job.Deployment.OrgID != b.OrgID {
		return ctrlplane.ErrForbidden
	}
	if e.Secrets == nil {
		return ErrNoCredential
	}
	if err := e.hooks(c.Kind).Removed(ctx, ComposedResource{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Resource: b.Name, Kind: c.Kind}); err != nil {
		return err
	}
	fields := []string{ResourceURL}
	for _, output := range c.Outputs {
		fields = append(fields, "output:"+output.Name)
	}
	for _, s := range c.Secrets {
		fields = append(fields, "secret:"+s.Name)
	}
	for _, field := range fields {
		if err := e.Secrets.Delete(ctx, resourceKey(b.OrgID, b.EnvironmentID, b.Name, field)); err != nil {
			return err
		}
	}
	return nil
}

// boundResource is a resource binding's configuration, or nil for anything
// else.
func boundResource(b *planning.Binding) *planning.ResourceConfig {
	if b == nil || b.Subject != planning.ResourceSubject {
		return nil
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil {
		return nil
	}
	return &cfg
}
