// Package ecs is a Ctrl Plane provider for Amazon ECS on Fargate. A
// Fargate service runs long-running containers, which is the model Ctrl
// Plane's Provider interface describes, so ECS services are Ctrl Plane
// instances and their rollouts are Ctrl Plane deployments.
//
// Like Cloud Run's, there's one Provider per AWS connection and region,
// registered under a key that is also its Info().Name, and it reads the
// connection's key on every call. Everything a task needs that Ctrl
// Plane's ServiceSpec has no field for (the cluster, network, roles, log
// group and load balancer target) travels as instance labels, which the
// deployment executor sets up before it calls Ctrl Plane.
package ecs

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/aws"
)

// Labels on a Ctrl Plane instance.
const (
	LabelCluster     = "loomplane-ecs-cluster"
	LabelSubnets     = "loomplane-ecs-subnets"
	LabelGroup       = "loomplane-ecs-group"
	LabelRole        = "loomplane-ecs-execution-role"
	LabelLogGroup    = "loomplane-ecs-log-group"
	LabelTargetGroup = "loomplane-ecs-target-group"
	LabelListener    = "loomplane-ecs-listener"
	LabelDesired     = "loomplane-ecs-desired"
	LabelCPU         = "loomplane-ecs-cpu"
	// LabelNamespace is the Cloud Map namespace the service joins through
	// Service Connect, and LabelAlias the name it's reached by there.
	LabelNamespace = "loomplane-ecs-namespace"
	LabelAlias     = "loomplane-ecs-alias"
	// LabelSecret prefixes a label naming the Secrets Manager secret a
	// variable is read from: "loomplane-secret.KEY" = ARN.
	LabelSecret = "loomplane-secret."
)

// Fargate's smallest task, used when the manifest sets no size.
const (
	defaultCPU    = 256
	defaultMemory = 512
)

// KeyPrefix starts every ECS provider's registry key.
const KeyPrefix = "ecs-fargate."

// Key is the registry key, and name, of the provider for a connection in
// a region.
func Key(connectionID, region string) string { return KeyPrefix + connectionID + "." + region }

// SessionFunc acts as the connection's access key, read from Vault.
type SessionFunc func(ctx context.Context) (*aws.Session, error)

// Instances reads Ctrl Plane instances.
type Instances interface {
	Get(ctx context.Context, instanceID id.ID) (*instance.Instance, error)
}

// Provider runs Ctrl Plane instances as ECS services on Fargate.
type Provider struct {
	key       string
	region    string
	session   SessionFunc
	instances Instances
}

var _ cpprovider.Provider = (*Provider)(nil)

// New returns the provider for a connection in a region.
func New(connectionID, region string, session SessionFunc, instances Instances) *Provider {
	return &Provider{key: Key(connectionID, region), region: region, session: session, instances: instances}
}

// ServiceName is the ECS service, and task family, an instance runs as.
func ServiceName(instanceID id.ID) string {
	return strings.ReplaceAll(strings.ToLower(instanceID.String()), "_", "-")
}

// Info implements cpprovider.Provider.
func (p *Provider) Info() cpprovider.ProviderInfo {
	return cpprovider.ProviderInfo{Name: p.key, Version: "fargate", Region: p.region}
}

// Capabilities implements cpprovider.Provider.
func (p *Provider) Capabilities() []cpprovider.Capability {
	return []cpprovider.Capability{cpprovider.CapProvision, cpprovider.CapDeploy, cpprovider.CapRolling}
}

// Provision implements cpprovider.Provider.
func (p *Provider) Provision(ctx context.Context, req cpprovider.ProvisionRequest) (*cpprovider.ProvisionResult, error) {
	if len(req.Services) != 1 {
		return nil, fmt.Errorf("loomplane runs one container per ECS service, not %d", len(req.Services))
	}
	if err := p.apply(ctx, req.InstanceID, req.Services[0], req.Labels); err != nil {
		return nil, err
	}
	return &cpprovider.ProvisionResult{
		ProviderRef: ServiceName(req.InstanceID),
		ServiceRefs: map[string]string{req.Services[0].Name: ServiceName(req.InstanceID)},
	}, nil
}

// Deploy implements cpprovider.Provider: new images and variables on top
// of what the instance should run, read back from Ctrl Plane.
func (p *Provider) Deploy(ctx context.Context, req cpprovider.DeployRequest) (*cpprovider.DeployResult, error) {
	inst, err := p.instances.Get(ctx, req.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("read the instance: %w", err)
	}
	if len(inst.Services) != 1 {
		return nil, fmt.Errorf("instance %s has %d services; loomplane runs one", req.InstanceID, len(inst.Services))
	}
	spec := inst.Services[0]
	for _, d := range req.Services {
		if d.Name == spec.Name {
			spec.Image = d.Image
			if d.Env != nil {
				spec.Env = d.Env
			}
		}
	}
	if err := p.apply(ctx, req.InstanceID, spec, inst.Labels); err != nil {
		return nil, err
	}
	return &cpprovider.DeployResult{ProviderRef: ServiceName(req.InstanceID), Status: "succeeded"}, nil
}

func (p *Provider) apply(ctx context.Context, instanceID id.ID, spec cpprovider.ServiceSpec, labels map[string]string) error {
	s, err := p.session(ctx)
	if err != nil {
		return err
	}
	name := ServiceName(instanceID)
	var port int32
	if len(spec.Ports) > 0 {
		port = int32(spec.Ports[0].Container) //nolint:gosec // G115: a port, validated by the manifest
	}
	cpu, _ := strconv.Atoi(labels[LabelCPU]) //nolint:errcheck // absent means the default
	if cpu == 0 {
		cpu = defaultCPU
	}
	memory := spec.Resources.MemoryMB
	if memory == 0 {
		memory = defaultMemory
	}
	secrets := map[string]string{}
	for _, l := range slices.Sorted(maps.Keys(labels)) {
		if key, ok := strings.CutPrefix(l, LabelSecret); ok {
			secrets[key] = labels[l]
		}
	}
	task, err := s.RegisterTask(ctx, aws.Task{
		Family: name, Image: spec.Image, Port: port, CPU: cpu, MemoryMB: memory, Env: spec.Env, Secrets: secrets,
		ExecutionRole: labels[LabelRole], LogGroup: labels[LabelLogGroup],
	})
	if err != nil {
		return err
	}
	desired, _ := strconv.Atoi(labels[LabelDesired]) //nolint:errcheck // absent means one
	if desired == 0 {
		desired = 1
	}
	return s.ApplyService(ctx, aws.Service{
		Cluster: labels[LabelCluster], Name: name, Task: task, Desired: int32(desired), //nolint:gosec // G115: a small count
		Subnets: strings.Split(labels[LabelSubnets], ","), Group: labels[LabelGroup],
		TargetGroup: labels[LabelTargetGroup], Port: port,
		Namespace: labels[LabelNamespace], Alias: labels[LabelAlias],
	})
}

// Deprovision implements cpprovider.Provider. The listener and target
// group are the service's own, so they go with it.
func (p *Provider) Deprovision(ctx context.Context, instanceID id.ID) error {
	inst, err := p.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("read the instance: %w", err)
	}
	s, err := p.session(ctx)
	if err != nil {
		return err
	}
	if err := s.DeleteService(ctx, inst.Labels[LabelCluster], ServiceName(instanceID)); err != nil {
		return err
	}
	return s.RemoveListener(ctx, inst.Labels[LabelListener], inst.Labels[LabelTargetGroup])
}

// Status implements cpprovider.Provider.
func (p *Provider) Status(ctx context.Context, instanceID id.ID) (*cpprovider.InstanceStatus, error) {
	inst, err := p.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("read the instance: %w", err)
	}
	s, err := p.session(ctx)
	if err != nil {
		return nil, err
	}
	image, err := s.ServiceImage(ctx, inst.Labels[LabelCluster], ServiceName(instanceID))
	if err != nil {
		return nil, err
	}
	if image == "" {
		return &cpprovider.InstanceStatus{State: cpprovider.StateDestroyed}, nil
	}
	return &cpprovider.InstanceStatus{State: cpprovider.StateRunning, Ready: true}, nil
}

// ECS restarts and scales tasks itself; Loomplane doesn't drive those
// through Ctrl Plane.

// Start implements cpprovider.Provider.
func (p *Provider) Start(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Stop implements cpprovider.Provider.
func (p *Provider) Stop(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Restart implements cpprovider.Provider.
func (p *Provider) Restart(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Rollback implements cpprovider.Provider.
func (p *Provider) Rollback(context.Context, id.ID, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Scale implements cpprovider.Provider.
func (p *Provider) Scale(context.Context, id.ID, cpprovider.ResourceSpec) error {
	return ctrlplane.ErrProviderUnavail
}

// Resources implements cpprovider.Provider.
func (p *Provider) Resources(context.Context, id.ID) (*cpprovider.ResourceUsage, error) {
	return &cpprovider.ResourceUsage{}, nil
}

// Logs implements cpprovider.Provider.
func (p *Provider) Logs(context.Context, id.ID, cpprovider.LogOptions) (io.ReadCloser, error) {
	return nil, ctrlplane.ErrProviderUnavail
}

// Exec implements cpprovider.Provider.
func (p *Provider) Exec(context.Context, id.ID, cpprovider.ExecRequest) (*cpprovider.ExecResult, error) {
	return nil, ctrlplane.ErrProviderUnavail
}
