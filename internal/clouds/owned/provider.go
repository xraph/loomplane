package owned

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
)

const (
	LabelOrg                  = "loomplane.org"
	LabelPlan                 = "loomplane.plan"
	LabelProject              = "loomplane.project"
	LabelEnvironment          = "loomplane.environment"
	LabelAction               = "loomplane.action"
	LabelService              = "loomplane.service"
	LabelOperation            = "loomplane.operation"
	LabelConnection           = "loomplane.connection"
	LabelSettings             = "loomplane.target.settings"
	LabelRuntime              = "loomplane.runtime.ref"
	LabelImage                = "loomplane.image.ref"
	LabelPublic               = "loomplane.public"
	LabelHostPort             = "loomplane.host.port"
	LabelHealth               = "loomplane.health.path"
	LabelHostname             = "loomplane.hostname"
	LabelIngressClass         = "loomplane.ingress.class"
	LabelTLSSecret            = "loomplane.tls.secret" //nolint:gosec // names a selected Secret, never its value
	LabelReplicas             = "loomplane.replicas"
	LabelScaleMin             = "loomplane.scale.min"
	LabelScaleMax             = "loomplane.scale.max"
	LabelArchitecture         = "loomplane.architecture"
	LabelRevision             = "loomplane.revision"
	LabelReceiptRelease       = "loomplane.runtime.receipt.release"
	LabelReceiptServicePrefix = "loomplane.runtime.receipt.service."
	LabelInstance             = "loomplane.instance"
	LabelManageIngress        = "loomplane.manage.ingress"
	LabelManageHPA            = "loomplane.manage.hpa"
)

// A composed catalog resource's component carries its kind, its resource's
// manifest name and its own name. LabelHealthPort is the container port
// its readiness probe answers on, when it publishes more than one.
// LabelRuntimeFiles is "true" on a component whose runtime values include
// rendered files. LabelKubernetesRules is the JSON array of
// recipes.KubernetesRule a process gets through its own ServiceAccount on
// Kubernetes. LabelHealthProtocol is "redis" when readiness is a RESP PING;
// absent, it is HTTP. LabelFSGroup is the group that owns a component's
// volume on Kubernetes.
const (
	LabelCatalogKind      = "loomplane.catalog.kind"
	LabelCatalogResource  = "loomplane.catalog.resource"
	LabelCatalogComponent = "loomplane.catalog.component"
	LabelHealthPort       = "loomplane.health.port"
	LabelRuntimeFiles     = "loomplane.runtime.files"
	LabelKubernetesRules  = "loomplane.kubernetes.rules"
	LabelHealthProtocol   = "loomplane.health.protocol"
	LabelFSGroup          = "loomplane.fs.group"
)

// LabelGPU is the reviewed provider.GPUPlacement, as JSON, of a workload
// that uses GPUs. LabelShmMB is the size of its /dev/shm in MiB.
const (
	LabelGPU   = "loomplane.gpu"    // JSON provider.GPUPlacement
	LabelShmMB = "loomplane.shm.mb" // decimal MiB
)

type Instances interface {
	Get(context.Context, id.ID) (*instance.Instance, error)
}

type instanceUpdater interface {
	Update(context.Context, id.ID, instance.UpdateRequest) (*instance.Instance, error)
}

type RuntimeValues struct {
	Env      map[string]string
	PullAuth string
	// Files are rendered files by absolute path, and FileModes their
	// modes; a file without one is 0o444. They're written into the
	// container before it starts.
	Files     map[string][]byte
	FileModes map[string]fs.FileMode
}

// DefaultFileMode is a rendered file's mode when it has none.
const DefaultFileMode fs.FileMode = 0o444

// FileMode is the mode a rendered file is written with.
func (v RuntimeValues) FileMode(path string) fs.FileMode {
	if mode := v.FileModes[path].Perm(); mode != 0 {
		return mode
	}
	return DefaultFileMode
}

type Values func(context.Context, map[string]string) (RuntimeValues, error)

// Backend uses an explicitly selected connection for every operation. Its
// runtime values are resolved in memory; Ctrl Plane stores only references.
type Backend interface {
	Apply(context.Context, id.ID, cpprovider.ServiceSpec, map[string]string) (*cpprovider.ProvisionResult, error)
	Remove(context.Context, id.ID, map[string]string) error
	Status(context.Context, id.ID, map[string]string) (*cpprovider.InstanceStatus, error)
	SetRunning(context.Context, id.ID, map[string]string, bool) error
	Scale(context.Context, id.ID, map[string]string, cpprovider.ResourceSpec) error
}

// JobBackend runs a one-shot container to completion: a composed
// resource's migration, for one deployment's action. name identifies the
// run on the target, so a retry finds what an earlier attempt left. A
// finished run is a JobResult whatever its exit code; an error is a run
// that couldn't start or didn't finish within timeout. What it created is
// removed either way.
type JobBackend interface {
	RunJob(ctx context.Context, name string, spec cpprovider.ServiceSpec, labels map[string]string, timeout time.Duration) (JobResult, error)
}

// JobResult is how a job ended: its exit code and the last JobOutputLimit
// bytes of what it wrote, with every secret value replaced by "[redacted]".
type JobResult struct {
	ExitCode int
	Output   string
}

type Provider struct {
	Key       string
	Instances Instances
	Backend   Backend
	AutoScale bool
	Wait      time.Duration
}

var _ cpprovider.Provider = (*Provider)(nil)
var _ cpprovider.ReadinessWaiter = (*Provider)(nil)

func (p *Provider) Info() cpprovider.ProviderInfo {
	return cpprovider.ProviderInfo{Name: p.Key, Version: "v1"}
}
func (p *Provider) Capabilities() []cpprovider.Capability {
	caps := []cpprovider.Capability{cpprovider.CapProvision, cpprovider.CapDeploy}
	if p.AutoScale {
		caps = append(caps, cpprovider.CapAutoScale)
	}
	return caps
}
func (p *Provider) ReadinessPolicy() cpprovider.ReadinessPolicy {
	wait := p.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	return cpprovider.ReadinessPolicy{Timeout: wait, Interval: time.Second}
}
func (p *Provider) Provision(ctx context.Context, r cpprovider.ProvisionRequest) (*cpprovider.ProvisionResult, error) {
	if len(r.Services) != 1 || r.Labels[LabelOrg] != r.TenantID {
		return nil, fmt.Errorf("owned target needs one service in its recorded organization")
	}
	return p.Backend.Apply(ctx, r.InstanceID, r.Services[0], maps.Clone(r.Labels))
}
func (p *Provider) Deploy(ctx context.Context, r cpprovider.DeployRequest) (*cpprovider.DeployResult, error) {
	updater, ok := p.Instances.(instanceUpdater)
	if !ok {
		return nil, fmt.Errorf("owned deployment requires persistent runtime receipts")
	}
	inst, err := p.Instances.Get(ctx, r.InstanceID)
	if err != nil {
		return nil, err
	}
	if len(inst.Services) != 1 {
		return nil, fmt.Errorf("owned target needs one service")
	}
	spec := inst.Services[0]
	for _, service := range r.Services {
		if service.Name == spec.Name {
			spec.Image = service.Image
			if service.Env != nil {
				spec.Env = service.Env
			}
		}
	}
	labels := maps.Clone(inst.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelRevision] = r.ReleaseID.String()
	result, err := p.Backend.Apply(ctx, r.InstanceID, spec, labels)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ServiceRefs[spec.Name] == "" {
		return nil, fmt.Errorf("owned deployment returned no exact service reference")
	}
	deadline := time.Now().Add(p.ReadinessPolicy().Timeout)
	for {
		status, statusErr := p.Backend.Status(ctx, r.InstanceID, labels)
		if statusErr != nil {
			return nil, statusErr
		}
		if status.Ready {
			// Controller Deploy does not refresh ServiceRefs. Record the ready
			// runtime separately; consumers also require its deployment to succeed.
			for key := range labels {
				if strings.HasPrefix(key, LabelReceiptServicePrefix) {
					delete(labels, key)
				}
			}
			labels[LabelReceiptRelease] = r.ReleaseID.String()
			for name, ref := range result.ServiceRefs {
				labels[LabelReceiptServicePrefix+name] = ref
			}
			if _, err = updater.Update(ctx, r.InstanceID, instance.UpdateRequest{Labels: labels}); err != nil {
				return nil, fmt.Errorf("persist owned runtime receipt: %w", err)
			}
			return &cpprovider.DeployResult{ProviderRef: result.ProviderRef, Status: "succeeded", Endpoints: result.Endpoints}, nil
		}
		if status.State == cpprovider.StateFailed || time.Now().After(deadline) {
			return nil, fmt.Errorf("owned target did not become ready: %s", status.Message)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (p *Provider) Deprovision(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.Remove(ctx, iid, inst.Labels)
}
func (p *Provider) Status(ctx context.Context, iid id.ID) (*cpprovider.InstanceStatus, error) {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return nil, err
	}
	return p.Backend.Status(ctx, iid, inst.Labels)
}
func (p *Provider) Start(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.SetRunning(ctx, iid, inst.Labels, true)
}
func (p *Provider) Stop(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.SetRunning(ctx, iid, inst.Labels, false)
}
func (p *Provider) Restart(ctx context.Context, iid id.ID) error {
	if err := p.Stop(ctx, iid); err != nil {
		return err
	}
	return p.Start(ctx, iid)
}
func (p *Provider) Scale(ctx context.Context, iid id.ID, spec cpprovider.ResourceSpec) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.Scale(ctx, iid, inst.Labels, spec)
}
func (*Provider) Rollback(context.Context, id.ID, id.ID) error { return ctrlplane.ErrProviderUnavail }
func (*Provider) Resources(context.Context, id.ID) (*cpprovider.ResourceUsage, error) {
	return nil, ctrlplane.ErrProviderUnavail
}
func (*Provider) Logs(context.Context, id.ID, cpprovider.LogOptions) (io.ReadCloser, error) {
	return nil, ctrlplane.ErrProviderUnavail
}
func (*Provider) Exec(context.Context, id.ID, cpprovider.ExecRequest) (*cpprovider.ExecResult, error) {
	return nil, ctrlplane.ErrProviderUnavail
}

func ForOperation(labels map[string]string, operation string) map[string]string {
	out := maps.Clone(labels)
	out[LabelOperation] = operation
	return out
}
