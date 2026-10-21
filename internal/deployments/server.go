package deployments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	cpauth "github.com/xraph/ctrlplane/auth"
	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

const labelRegistry = "loomplane.registry"
const labelRegistrySettings = "loomplane.registry.settings"
const labelImage = owned.LabelImage

// OwnedTarget keeps the existing Ctrl Plane lifecycle and stores runtime
// credentials in Vault. Its adapters read the selected connection on each call.
type OwnedTarget struct {
	Cloud       string
	Ctrl        CtrlPlane
	Credentials Credentials
	Secrets     ResourceSecrets
	Builder     *ImageBuilder
	Ports       owned.PortReservations
	Controllers ControllerReceipts
	ServerDial  func(context.Context, *server.Config) (*server.Client, error)
	Backend     func(string, string) owned.Backend
}

var _ Executor = (*OwnedTarget)(nil)
var _ Locator = (*OwnedTarget)(nil)

func (e *OwnedTarget) connection(ctx context.Context, org, connection string, labels map[string]string) (*connections.Connection, []byte, error) {
	if labels[owned.LabelOrg] != org || labels[owned.LabelConnection] != connection {
		return nil, nil, errors.New("the service does not belong to this target connection")
	}
	if claims := cpauth.ClaimsFrom(ctx); claims != nil && claims.TenantID != org {
		return nil, nil, ctrlplane.ErrForbidden
	}
	if e.Credentials == nil {
		return nil, nil, ErrNoCredential
	}
	c, raw, err := e.Credentials.Credential(ctx, org, connection)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.ID != connection || c.OrgID != org || c.Cloud != e.Cloud || c.Status != connections.StatusReady {
		return nil, nil, errors.New("the recorded target connection is not ready")
	}
	var reviewed map[string]string
	if checkErr := json.Unmarshal([]byte(labels[owned.LabelSettings]), &reviewed); checkErr != nil {
		return nil, nil, errors.New("the target has no reviewed placement")
	}
	if !reflect.DeepEqual(reviewed, planning.ReviewedConnectionSettings(c.Cloud, c.Details)) {
		return nil, nil, errors.New("target settings changed since review; plan again")
	}
	return c, raw, nil
}

func (e *OwnedTarget) serverSession(org, connection string) server.Session {
	return func(ctx context.Context, labels map[string]string) (*server.Client, error) {
		c, raw, err := e.connection(ctx, org, connection, labels)
		if err != nil {
			return nil, err
		}
		cfg, err := server.ParseConfig(raw, c.Details)
		if err != nil {
			return nil, err
		}
		dial := e.ServerDial
		if dial == nil {
			dial = server.Dial
		}
		client, err := dial(ctx, cfg)
		if err != nil {
			return nil, err
		}
		details, missing, err := client.Check(ctx)
		if err != nil || len(missing) > 0 || !reflect.DeepEqual(planning.ReviewedConnectionSettings(e.Cloud, details), planning.ReviewedConnectionSettings(e.Cloud, c.Details)) {
			_ = client.Close()
			if err != nil {
				return nil, err
			}
			return nil, errors.New("the SSH server or Docker identity changed; check the connection and plan again")
		}
		client.RequireFence = labels["loomplane.pool"] != ""
		client.Config.Details = details
		return client, nil
	}
}

func (e *OwnedTarget) values(ctx context.Context, labels map[string]string) (owned.RuntimeValues, error) {
	values := owned.RuntimeValues{Env: map[string]string{}}
	if ref := labels[owned.LabelRuntime]; ref != "" {
		if validateRuntimeRef(labels[owned.LabelOrg], labels[owned.LabelEnvironment], labels[owned.LabelService], ref) != nil || e.Secrets == nil {
			return values, errors.New("runtime credential is outside this environment")
		}
		raw, err := e.Secrets.Get(ctx, ref)
		if err != nil {
			return values, err
		}
		if checkErr := json.Unmarshal(raw, &values.Env); checkErr != nil {
			return values, checkErr
		}
	}
	connection := labels[labelRegistry]
	if connection == "" {
		return values, nil
	}
	if e.Credentials == nil {
		return values, ErrNoCredential
	}
	c, raw, err := e.Credentials.Credential(ctx, labels[owned.LabelOrg], connection)
	if err != nil {
		return values, err
	}
	if c == nil || c.OrgID != labels[owned.LabelOrg] || c.ID != connection || c.Cloud != connections.Registry || (c.Status != connections.StatusReady && !connections.RegistryPullReady(c)) {
		return values, errors.New("the registry is not ready for image pulls")
	}
	var reviewed map[string]string
	if checkErr := json.Unmarshal([]byte(labels[labelRegistrySettings]), &reviewed); checkErr != nil {
		return values, checkErr
	}
	cfg, err := registry.ParseConfig(raw, c.Details)
	if err != nil {
		return values, err
	}
	if !reflect.DeepEqual(reviewed, planning.ReviewedConnectionSettings(c.Cloud, cfg.Details)) {
		return values, errors.New("registry trust settings changed since review; plan again")
	}
	if e.Cloud == connections.Kubernetes {
		var target map[string]string
		if decodeErr := json.Unmarshal([]byte(labels[owned.LabelSettings]), &target); decodeErr != nil {
			return values, decodeErr
		}
		if (cfg.Username != "" || cfg.Token != "") && target["image_pull_secret"] == "" {
			return values, errors.New("private Kubernetes images need an existing namespace image_pull_secret in the target connection; registry credentials remain with the builder")
		}
		return values, nil
	}
	image := labels[labelImage]
	if image == "" {
		return values, nil
	} // The source build has not produced its image yet.
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return values, err
	}
	repository, _, _ := strings.Cut(strings.TrimPrefix(image, endpoint.Host+"/"), "@")
	auth, err := registry.New(cfg).DockerAuth(ctx, repository, "pull")
	if err != nil {
		return values, err
	}
	values.PullAuth = base64.URLEncoding.EncodeToString(auth)
	return values, nil
}

// Register restores a connection-specific provider without opening credentials.
func (e *OwnedTarget) Register(org, connection string) string {
	key := e.Cloud + "." + connection
	if _, err := e.Ctrl.Providers.Get(key); err == nil {
		return key
	}
	var backend owned.Backend
	switch {
	case e.Backend != nil:
		backend = e.Backend(org, connection)
	case e.Cloud == connections.Server:
		backend = &server.Runtime{Session: e.serverSession(org, connection), Values: e.values, Ports: e.Ports}
	default:
		backend = e.kubernetesBackend(org, connection)
	}
	e.Ctrl.Providers.Register(key, &owned.Provider{Key: key, Instances: e.Ctrl.Instances, Backend: backend, AutoScale: e.Cloud == connections.Kubernetes})
	return key
}

type ownedState struct {
	Image         ImageArtifact            `json:"image"`
	RuntimeRef    string                   `json:"runtime_ref,omitempty"`
	PriorInstance string                   `json:"prior_instance,omitempty"`
	PriorLabels   map[string]string        `json:"prior_labels,omitempty"`
	PriorServices []cpprovider.ServiceSpec `json:"prior_services,omitempty"`
	PriorStopped  bool                     `json:"prior_stopped,omitempty"`
	Committed     bool                     `json:"committed,omitempty"`
	Removals      map[string]string        `json:"removals,omitempty"`
}

func (e *OwnedTarget) Apply(ctx context.Context, job *Job) (out *Outcome, err error) {
	if job.Action.Resource != nil {
		return e.applyDatabase(ctx, job)
	}
	if job.Action.Service == nil || job.Connection == nil || job.Deployment == nil {
		return nil, ErrNoExecutor
	}
	cfg := job.Action.Service
	if cfg.Target != job.Connection.ID || job.Action.ConnectionID != job.Connection.ID {
		return nil, errors.New("the action target differs from its reviewed connection")
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	key := e.Register(job.Deployment.OrgID, job.Connection.ID)
	inst, name, err := currentInstance(ctx, e.Ctrl, job, key)
	if err != nil {
		return nil, err
	}
	if inst != nil {
		if inst.TenantID != job.Deployment.OrgID || inst.Labels[owned.LabelProject] != job.Deployment.ProjectID || inst.Labels[owned.LabelEnvironment] != job.Deployment.EnvironmentID || len(inst.Services) != 1 {
			return nil, errors.New("the instance belongs to another project or environment")
		}
		if refErr := validateRuntimeRef(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, inst.Labels[owned.LabelRuntime]); refErr != nil {
			return nil, refErr
		}
	}
	var state ownedState
	if len(job.Resume) > 0 {
		if checkErr := json.Unmarshal(job.Resume, &state); checkErr != nil {
			return nil, fmt.Errorf("read owned deployment checkpoint: %w", checkErr)
		}
	}
	save := func() error {
		raw, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr
		}
		if job.Checkpoint == nil {
			return errors.New("owned deployment has no checkpoint")
		}
		if checkpointErr := job.Checkpoint(raw); checkpointErr != nil {
			return checkpointErr
		}
		job.Resume = raw
		return nil
	}
	for _, ref := range []string{state.RuntimeRef, state.PriorLabels[owned.LabelRuntime]} {
		if refErr := validateRuntimeRef(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, ref); refErr != nil {
			return nil, refErr
		}
	}
	if state.Committed && inst != nil && inst.Labels[owned.LabelRuntime] == state.RuntimeRef && len(inst.Services) == 1 && inst.Services[0].Image == state.Image.Reference {
		backend, backendErr := e.registeredBackend(key)
		if backendErr != nil {
			return nil, backendErr
		}
		status, statusErr := backend.Status(ctx, inst.ID, inst.Labels)
		if statusErr != nil {
			return nil, statusErr
		}
		if !status.Ready || status.Metadata["revision"] != state.Image.Reference {
			return nil, errors.New("the committed workload is no longer ready at its recorded image")
		}

		if state.PriorLabels != nil && state.PriorLabels[owned.LabelRuntime] != "" && state.PriorLabels[owned.LabelRuntime] != state.RuntimeRef {
			if deleteErr := e.deleteRuntime(ctx, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, state.PriorLabels[owned.LabelRuntime]); deleteErr != nil {
				return nil, deleteErr
			}
		}
		return &Outcome{ExternalID: inst.ID.String(), URL: endpoint(inst.Endpoints), Image: state.Image.Reference, Revision: state.Image.Reference}, nil
	}
	_, preflightLabels := ownedSpec(job, "", state)
	// A failed attempt may have removed its temporary runtime values. They are
	// recreated below; preflight checks the reviewed target and registry only.
	delete(preflightLabels, owned.LabelRuntime)
	if _, checkErr := e.values(ctx, preflightLabels); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := e.validateTarget(ctx, preflightLabels); checkErr != nil {
		return nil, checkErr
	}
	if inst != nil && state.PriorInstance == "" {
		state.PriorInstance = inst.ID.String()
		state.PriorLabels = maps.Clone(inst.Labels)
		state.PriorServices = inst.Services
		state.PriorStopped = inst.State == cpprovider.StateStopped
		if checkErr := save(); checkErr != nil {
			return nil, checkErr
		}
	}
	builder := e.Builder
	if builder == nil {
		builder = &ImageBuilder{Dial: e.ServerDial}
	}
	image, err := builder.Build(ctx, job, &state.Image, save)
	if err != nil {
		return nil, err
	}
	if state.RuntimeRef == "" {
		ref, refErr := typeid.Generate("runtime")
		if refErr != nil {
			return nil, refErr
		}
		state.RuntimeRef = resourceKey(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, ref.String())
		if checkErr := save(); checkErr != nil {
			return nil, checkErr
		}
	}
	if e.Secrets == nil {
		return nil, ErrNoCredential
	}
	runtimeValues := map[string]string{}
	for key, value := range job.Env {
		if job.Secrets[key] {
			runtimeValues[key] = value
		}
	}
	raw, err := json.Marshal(runtimeValues)
	if err != nil {
		return nil, err
	}
	if checkErr := e.Secrets.Put(ctx, state.RuntimeRef, raw); checkErr != nil {
		return nil, checkErr
	}
	sp, labels := ownedSpec(job, image, state)
	// Resolve and validate credentials before persisting any changed instance spec.
	if _, err = e.values(ctx, labels); err != nil {
		return nil, err
	}
	if checkErr := e.validateTarget(ctx, labels); checkErr != nil {
		return nil, checkErr
	}
	var previousServices []cpprovider.ServiceSpec
	var previousLabels map[string]string
	if inst != nil {
		previousServices = inst.Services
		previousLabels = maps.Clone(inst.Labels)
		if state.PriorInstance == inst.ID.String() && state.PriorLabels != nil {
			previousServices = state.PriorServices
			previousLabels = maps.Clone(state.PriorLabels)
		}
		if inst.TenantID != job.Deployment.OrgID || inst.Labels[owned.LabelProject] != job.Deployment.ProjectID || inst.Labels[owned.LabelEnvironment] != job.Deployment.EnvironmentID {
			return nil, errors.New("the instance belongs to another project or environment")
		}
	}
	job.Progress(StepDeploying, "Deploying to the selected "+e.Cloud+" target")
	if inst == nil {
		inst, err = e.Ctrl.Instances.Create(ctx, instance.CreateRequest{Name: name, ProviderName: key, Services: []cpprovider.ServiceSpec{sp}, Labels: labels})
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			failed, findErr := e.Ctrl.Instances.GetBySlug(cleanup, strings.ToLower(name))
			if findErr == nil && failed.Labels[owned.LabelRuntime] == state.RuntimeRef {
				binding := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, ConnectionID: job.Connection.ID, Name: job.Action.Name, ExternalID: failed.ID.String()}
				err = errors.Join(err, e.Remove(cleanup, job, binding))
			}
			return nil, err
		}
	} else {
		if _, err = e.Ctrl.Instances.Update(ctx, inst.ID, instance.UpdateRequest{Services: []cpprovider.ServiceSpec{sp}, Labels: labels}); err != nil {
			return nil, err
		}
		_, err = e.Ctrl.Deploys.Deploy(ctx, deploy.DeployRequest{InstanceID: inst.ID, Services: []cpprovider.ServiceDeploySpec{{Name: mainService, Image: image, Env: sp.Env}}, CommitSHA: job.CommitSHA, Notes: "Plan " + job.Plan.ID})
		if err != nil {
			restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			backend, backendErr := e.registeredBackend(key)
			if backendErr == nil && len(previousServices) == 1 {
				_, backendErr = backend.Apply(restore, inst.ID, previousServices[0], previousLabels)
				if backendErr == nil && state.PriorStopped {
					backendErr = backend.SetRunning(restore, inst.ID, previousLabels, false)
				}
			}
			_, restoreErr := e.Ctrl.Instances.Update(restore, inst.ID, instance.UpdateRequest{Services: previousServices, Labels: previousLabels})
			if backendErr == nil && restoreErr == nil && state.RuntimeRef != previousLabels[owned.LabelRuntime] {
				cleanupErr := e.deleteRuntime(restore, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, state.RuntimeRef)
				return nil, errors.Join(err, cleanupErr)
			}
			return nil, errors.Join(err, backendErr, restoreErr)
		}
	}
	inst, err = e.Ctrl.Instances.Get(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	state.Committed = true
	if saveErr := save(); saveErr != nil {
		return nil, saveErr
	}
	if previousLabels != nil && previousLabels[owned.LabelRuntime] != "" && previousLabels[owned.LabelRuntime] != state.RuntimeRef {
		if checkErr := e.deleteRuntime(ctx, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, previousLabels[owned.LabelRuntime]); checkErr != nil {
			return nil, checkErr
		}
	}
	return &Outcome{ExternalID: inst.ID.String(), URL: endpoint(inst.Endpoints), Image: image, Revision: image}, nil
}

func ownedSpec(job *Job, image string, state ownedState) (sp cpprovider.ServiceSpec, labels map[string]string) {
	cfg := job.Action.Service
	sp, _ = spec(job, image, nil)
	labels = map[string]string{owned.LabelOrg: job.Deployment.OrgID, owned.LabelProject: job.Deployment.ProjectID, owned.LabelEnvironment: job.Deployment.EnvironmentID, owned.LabelAction: job.Action.ID, owned.LabelService: job.Action.Name, owned.LabelConnection: job.Connection.ID, owned.LabelRuntime: state.RuntimeRef, owned.LabelPublic: strconv.FormatBool(cfg.Public), owned.LabelHealth: cfg.HealthPath, owned.LabelArchitecture: state.Image.Architecture, owned.LabelRevision: job.Deployment.ID + "/" + job.Action.ID}
	labels[owned.LabelPlan] = job.Deployment.PlanID
	if cfg.CloudPlacement != nil && cfg.CloudPlacement.PoolID != "" {
		labels["loomplane.pool"] = cfg.CloudPlacement.PoolID
	}

	if cfg.Workload == provider.Worker {
		labels[owned.LabelHealth] = ""
	}
	if cfg.Catalog != nil {
		sp.Args = cfg.Catalog.RuntimeArgs(false)
		sp.Volumes = []cpprovider.VolumeSpec{databaseVolume(cfg.Catalog)}
		databaseLabels(labels, cfg.Catalog, databaseName(job.Action.Name))
		labels[owned.LabelArchitecture] = cfg.Catalog.Architecture
		sp.Resources = cpprovider.ResourceSpec{CPUMillis: 1000, MemoryMB: 512}
		sp.Ports = []cpprovider.PortSpec{{Container: cfg.Catalog.Port(), Protocol: "tcp"}}
	}
	labels[labelImage] = image
	if image == "" && cfg.BuildTarget != nil {
		labels[labelImage] = cfg.BuildTarget.Image
	}
	settings, _ := json.Marshal(cfg.TargetSettings) //nolint:errcheck // string maps always encode
	labels[owned.LabelSettings] = string(settings)
	if cfg.Endpoint != nil {
		labels[owned.LabelHostPort] = strconv.Itoa(cfg.Endpoint.HostPort)
		labels[owned.LabelHostname] = cfg.Endpoint.Hostname
		labels[owned.LabelIngressClass] = cfg.Endpoint.IngressClass
		labels[owned.LabelTLSSecret] = cfg.Endpoint.TLSSecret
	}
	if cfg.Scale != nil {
		labels[owned.LabelReplicas] = strconv.Itoa(cfg.Scale.Min)
		labels[owned.LabelScaleMin] = strconv.Itoa(cfg.Scale.Min)
		labels[owned.LabelScaleMax] = strconv.Itoa(cfg.Scale.Max)
	}
	if job.Action.Provider == "server" {
		cpu, memory, err := planning.ServerResources(cfg.Size)
		if err == nil {
			sp.Resources.CPUMillis = int(cpu)
			sp.Resources.MemoryMB = int(memory >> 20)
		}
	}
	if cfg.Size != nil {
		sp.Resources.CPUMillis = int(cfg.Size.CPU * 1000)
		sp.Resources.MemoryMB = cfg.Size.Memory
	}
	if labels[owned.LabelScaleMin] != labels[owned.LabelScaleMax] {
		labels[owned.LabelManageHPA] = "true"
		if sp.Resources.CPUMillis == 0 {
			sp.Resources.CPUMillis = 100
		}
	}
	labels[owned.LabelManageIngress] = strconv.FormatBool(cfg.Public)
	if job.Binding != nil {
		var old planning.ServiceConfig
		if json.Unmarshal(job.Binding.Config, &old) == nil {
			if old.Public {
				labels[owned.LabelManageIngress] = "true"
			}
			if old.Scale != nil && old.Scale.Min != old.Scale.Max {
				labels[owned.LabelManageHPA] = "true"
			}
		}
	}
	if cfg.BuildTarget != nil {
		labels[labelRegistry] = cfg.BuildTarget.Registry
		registrySettings, _ := json.Marshal(cfg.BuildTarget.RegistrySettings) //nolint:errcheck // string maps always encode
		labels[labelRegistrySettings] = string(registrySettings)
	}
	return sp, labels
}

func (e *OwnedTarget) registeredBackend(key string) (owned.Backend, error) {
	p, err := e.Ctrl.Providers.Get(key)
	if err != nil {
		return nil, err
	}
	target, ok := p.(*owned.Provider)
	if !ok {
		return nil, errors.New("the registered target is not an owned provider")
	}
	return target.Backend, nil
}
func (e *OwnedTarget) validateTarget(ctx context.Context, labels map[string]string) error {
	if e.Backend != nil {
		return nil
	}
	org, connection := labels[owned.LabelOrg], labels[owned.LabelConnection]
	if e.Cloud == connections.Server {
		c, err := e.serverSession(org, connection)(ctx, labels)
		if err != nil {
			return err
		}
		return c.Close()
	}
	_, err := e.kubernetesSession(org, connection)(ctx, labels)
	return err
}

func (e *OwnedTarget) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID == "" {
		return nil
	}
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return err
	}
	if job.Deployment == nil || job.Deployment.OrgID != b.OrgID {
		return ctrlplane.ErrForbidden
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	key := e.Register(b.OrgID, b.ConnectionID)
	var state ownedState
	if len(job.Resume) > 0 {
		if decodeErr := json.Unmarshal(job.Resume, &state); decodeErr != nil {
			return decodeErr
		}
	}
	save := func() error {
		if job.Checkpoint == nil {
			return errors.New("cleanup needs a durable checkpoint")
		}
		raw, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr
		}
		if saveErr := job.Checkpoint(raw); saveErr != nil {
			return saveErr
		}
		job.Resume = raw
		return nil
	}
	inst, err := e.Ctrl.Instances.Get(ctx, iid)
	if err != nil && !errors.Is(err, ctrlplane.ErrNotFound) {
		return err
	}
	ref := state.Removals[b.ExternalID]
	if refErr := validateRuntimeRef(b.OrgID, b.EnvironmentID, b.Name, ref); refErr != nil {
		return refErr
	}
	if inst != nil {
		if inst.ProviderName != key || inst.TenantID != b.OrgID || inst.Labels[owned.LabelProject] != b.ProjectID || inst.Labels[owned.LabelEnvironment] != b.EnvironmentID {
			return errors.New("the binding does not own this infrastructure instance")
		}
		ref = inst.Labels[owned.LabelRuntime]
		if refErr := validateRuntimeRef(b.OrgID, b.EnvironmentID, b.Name, ref); refErr != nil {
			return refErr
		}
		if ref != "" {
			if state.Removals == nil {
				state.Removals = map[string]string{}
			}
			state.Removals[b.ExternalID] = ref
			if saveErr := save(); saveErr != nil {
				return saveErr
			}
		}
		if deleteErr := e.Ctrl.Instances.Delete(ctx, iid); deleteErr != nil {
			return deleteErr
		}
	}
	if ref != "" {
		if e.Secrets == nil {
			return ErrNoCredential
		}
		if deleteErr := e.deleteRuntime(ctx, b.OrgID, b.EnvironmentID, b.Name, ref); deleteErr != nil {
			return deleteErr
		}
		delete(state.Removals, b.ExternalID)
		return save()
	}
	return nil
}

func (e *OwnedTarget) Observe(ctx context.Context, c *connections.Connection, _ []byte, b *planning.Binding) (*Observation, error) {
	if c == nil || c.ID != b.ConnectionID || c.OrgID != b.OrgID || c.Cloud != e.Cloud {
		return nil, errors.New("the binding target is outside its recorded organization")
	}
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return nil, err
	}
	var cfg planning.ServiceConfig
	if checkErr := json.Unmarshal(b.Config, &cfg); checkErr != nil {
		return nil, checkErr
	}
	if b.Subject == planning.ResourceSubject {
		var resource planning.ResourceConfig
		if decodeErr := json.Unmarshal(b.Config, &resource); decodeErr != nil {
			return nil, decodeErr
		}
		converted, convertErr := databaseService(&resource)
		if convertErr != nil {
			return nil, convertErr
		}
		cfg = *converted
	}
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}, Connection: c, Action: planning.Action{ID: "service:" + b.Name, Name: b.Name, Service: &cfg}}
	_, labels := ownedSpec(job, "", ownedState{})
	key := e.Register(b.OrgID, b.ConnectionID)
	backend, err := e.registeredBackend(key)
	if err != nil {
		return nil, err
	}
	status, err := backend.Status(ctx, iid, labels)
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: status.Metadata["exists"] == "true", Revision: status.Metadata["revision"]}, nil
}
func (e *OwnedTarget) Locate(b *planning.Binding) string {
	return "the " + e.Cloud + " workload " + b.ExternalID + " through connection " + b.ConnectionID
}

func validateRuntimeRef(org, environment, service, ref string) error {
	if ref == "" {
		return nil
	}
	prefix := resourceKey(org, environment, service, "")
	if org == "" || environment == "" || service == "" || !strings.HasPrefix(ref, prefix+"runtime_") || strings.Contains(strings.TrimPrefix(ref, prefix), "/") {
		return errors.New("runtime credential is outside the recorded service scope")
	}
	return nil
}
func (e *OwnedTarget) deleteRuntime(ctx context.Context, org, environment, service, ref string) error {
	if err := validateRuntimeRef(org, environment, service, ref); err != nil {
		return err
	}
	if e.Secrets == nil {
		return ErrNoCredential
	}
	return e.Secrets.Delete(ctx, ref)
}
