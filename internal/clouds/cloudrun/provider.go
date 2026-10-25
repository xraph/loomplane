// Package cloudrun is a Ctrl Plane provider for Google Cloud Run. Cloud
// Run runs long-running containers that serve requests, which is the model
// Ctrl Plane's Provider interface describes, so Cloud Run services are
// Ctrl Plane instances and their rollouts are Ctrl Plane deployments.
//
// Ctrl Plane takes credentials only when a provider is built, so there's
// one Provider per Google Cloud connection and region, registered under a
// key that is also its Info().Name: Ctrl Plane looks an instance's
// provider up by the name it recorded, not by the key it was registered
// under. The provider reads the connection's key on every call, so a
// replaced or removed connection takes effect at once.
package cloudrun

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

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

// Labels on a Ctrl Plane instance that carry Cloud Run settings Ctrl
// Plane's ServiceSpec has no field for.
const (
	LabelPublic   = "loomplane-public"
	LabelMinScale = "loomplane-min-instances"
	LabelMaxScale = "loomplane-max-instances"
	LabelCPU      = "loomplane-cpu"
	// LabelSecret prefixes a label naming the Secret Manager version a
	// variable is read from: "loomplane-secret.KEY" = "SECRET:VERSION".
	// Secret values never pass through Ctrl Plane, which stores what it's
	// given; only these references do.
	LabelSecret = "loomplane-secret."
	// LabelCloudSQL lists the Cloud SQL instances a service connects to,
	// by connection name, comma separated. Cloud Run mounts their sockets
	// at /cloudsql.
	LabelCloudSQL = "loomplane-cloudsql"
	// LabelVPC names the VPC network a service's private traffic goes
	// through, for a service that reaches Memorystore.
	LabelVPC = "loomplane-vpc"
)

// KeyPrefix starts every Cloud Run provider's registry key.
const KeyPrefix = "cloud-run."

// Key is the registry key, and name, of the provider for a connection in
// a region.
func Key(connectionID, region string) string {
	return KeyPrefix + connectionID + "." + region
}

// SessionFunc authorizes as the connection's service account, reading its
// key from Vault.
type SessionFunc func(ctx context.Context) (*gcp.Session, error)

// Instances reads Ctrl Plane instances. instance.Service is the real one.
type Instances interface {
	Get(ctx context.Context, instanceID id.ID) (*instance.Instance, error)
}

// Provider runs Ctrl Plane instances as Cloud Run services, in one
// project and region.
type Provider struct {
	key       string
	region    string
	session   SessionFunc
	instances Instances
}

var _ cpprovider.Provider = (*Provider)(nil)

// New returns the provider for a connection in a region. instances reads
// back what an instance should run, since Ctrl Plane's Deploy carries
// only images and variables.
func New(connectionID, region string, session SessionFunc, instances Instances) *Provider {
	return &Provider{key: Key(connectionID, region), region: region, session: session, instances: instances}
}

// ServiceName is the Cloud Run service an instance runs as. Cloud Run
// names are lower case letters, digits and hyphens.
func ServiceName(instanceID id.ID) string {
	return strings.ReplaceAll(strings.ToLower(instanceID.String()), "_", "-")
}

// Info implements cpprovider.Provider.
func (p *Provider) Info() cpprovider.ProviderInfo {
	return cpprovider.ProviderInfo{Name: p.key, Version: "v2", Region: p.region}
}

// Capabilities implements cpprovider.Provider.
func (p *Provider) Capabilities() []cpprovider.Capability {
	return []cpprovider.Capability{cpprovider.CapProvision, cpprovider.CapDeploy, cpprovider.CapAutoScale, cpprovider.CapTLS}
}

// Provision implements cpprovider.Provider: it creates the service, or
// brings an existing one to the spec, and waits until it serves.
func (p *Provider) Provision(ctx context.Context, req cpprovider.ProvisionRequest) (*cpprovider.ProvisionResult, error) {
	if len(req.Services) != 1 {
		return nil, fmt.Errorf("cloud run runs one container per service, not %d", len(req.Services))
	}
	svc, err := p.apply(ctx, req.InstanceID, req.Services[0], req.Labels)
	if err != nil {
		return nil, err
	}
	return &cpprovider.ProvisionResult{
		ProviderRef: ServiceName(req.InstanceID),
		ServiceRefs: map[string]string{req.Services[0].Name: ServiceName(req.InstanceID)},
		Endpoints:   endpoints(svc, req.Labels),
	}, nil
}

// Deploy implements cpprovider.Provider: it rolls out new images and
// variables on top of what the instance should run, which it reads back
// from Ctrl Plane, and waits until the new revision serves.
func (p *Provider) Deploy(ctx context.Context, req cpprovider.DeployRequest) (*cpprovider.DeployResult, error) {
	inst, err := p.instances.Get(ctx, req.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("read the instance: %w", err)
	}
	if len(inst.Services) != 1 {
		return nil, fmt.Errorf("instance %s has %d services; cloud run runs one", req.InstanceID, len(inst.Services))
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
	if _, err := p.apply(ctx, req.InstanceID, spec, inst.Labels); err != nil {
		return nil, err
	}
	return &cpprovider.DeployResult{ProviderRef: ServiceName(req.InstanceID), Status: "succeeded"}, nil
}

func (p *Provider) apply(ctx context.Context, instanceID id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (*gcp.RunService, error) {
	s, err := p.session(ctx)
	if err != nil {
		return nil, err
	}
	name := ServiceName(instanceID)
	container := gcp.RunContainer{Image: spec.Image}
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		container.Env = append(container.Env, gcp.RunEnv{Name: k, Value: spec.Env[k]})
	}
	for _, l := range slices.Sorted(maps.Keys(labels)) {
		key, ok := strings.CutPrefix(l, LabelSecret)
		if !ok {
			continue
		}
		secret, version, ok := strings.Cut(labels[l], ":")
		if !ok {
			return nil, fmt.Errorf("label %s names no secret version: %q", l, labels[l])
		}
		ref := &gcp.RunValueSource{}
		ref.SecretKeyRef.Secret, ref.SecretKeyRef.Version = secret, version
		container.Env = append(container.Env, gcp.RunEnv{Name: key, ValueSource: ref})
	}
	if len(spec.Ports) > 0 && spec.Ports[0].Container > 0 {
		container.Ports = []gcp.RunPort{{ContainerPort: spec.Ports[0].Container}}
	}
	limits := map[string]string{}
	if cpu := labels[LabelCPU]; cpu != "" {
		limits["cpu"] = cpu
	}
	if spec.Resources.MemoryMB > 0 {
		limits["memory"] = strconv.Itoa(spec.Resources.MemoryMB) + "Mi"
	}
	if len(limits) > 0 {
		container.Resources = &gcp.RunResources{Limits: limits}
	}
	var volumes []gcp.RunVolume
	if instances := labels[LabelCloudSQL]; instances != "" {
		v := gcp.RunVolume{Name: "cloudsql"}
		v.CloudSQLInstance = &struct {
			Instances []string `json:"instances"`
		}{Instances: strings.Split(instances, ",")}
		volumes = append(volumes, v)
		container.Mounts = []gcp.RunVolumeMount{{Name: "cloudsql", MountPath: "/cloudsql"}}
	}
	run := gcp.RunService{
		Labels:   map[string]string{"managed-by": "loomplane"},
		Ingress:  "INGRESS_TRAFFIC_ALL",
		Template: gcp.RunTemplate{Containers: []gcp.RunContainer{container}, Volumes: volumes},
	}
	// Private addresses only go through the network; everything else
	// leaves the way it always does.
	if network := labels[LabelVPC]; network != "" {
		run.Template.VPCAccess = &gcp.RunVPCAccess{
			NetworkInterfaces: []gcp.RunNetworkInterface{{Network: network, Subnetwork: network}},
			Egress:            "PRIVATE_RANGES_ONLY",
		}
	}
	minScale, _ := strconv.Atoi(labels[LabelMinScale]) //nolint:errcheck // absent or malformed means Cloud Run's default
	maxScale, _ := strconv.Atoi(labels[LabelMaxScale]) //nolint:errcheck // likewise
	if minScale > 0 || maxScale > 0 {
		run.Template.Scaling = &gcp.RunScaling{Min: minScale, Max: maxScale}
	}
	if transportErr := applyPSCTransport(&run.Template, labels, s.Key().ProjectID, p.region); transportErr != nil {
		return nil, transportErr
	}
	svc, err := s.ApplyService(ctx, p.region, name, run)
	if err != nil {
		return nil, err
	}
	// A private service takes calls from the project's own services only,
	// which run as its runtime account.
	invoker := gcp.Public
	if labels[LabelPublic] != "true" {
		if invoker, err = s.RuntimeAccount(ctx); err != nil {
			return nil, fmt.Errorf("find the runtime service account: %w", err)
		}
	}
	if err := s.SetInvokers(ctx, p.region, name, invoker); err != nil {
		return nil, err
	}
	return svc, nil
}

func endpoints(svc *gcp.RunService, labels map[string]string) []cpprovider.Endpoint {
	if svc == nil || svc.URI == "" {
		return nil
	}
	return []cpprovider.Endpoint{{URL: svc.URI, Port: 443, Protocol: "https", Public: labels[LabelPublic] == "true"}}
}

// Deprovision implements cpprovider.Provider.
func (p *Provider) Deprovision(ctx context.Context, instanceID id.ID) error {
	s, err := p.session(ctx)
	if err != nil {
		return err
	}
	return s.DeleteService(ctx, p.region, ServiceName(instanceID))
}

// Status implements cpprovider.Provider.
func (p *Provider) Status(ctx context.Context, instanceID id.ID) (*cpprovider.InstanceStatus, error) {
	s, err := p.session(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.GetService(ctx, p.region, ServiceName(instanceID))
	if gcp.IsNotFound(err) {
		return &cpprovider.InstanceStatus{State: cpprovider.StateDestroyed}, nil
	}
	if err != nil {
		return nil, err
	}
	st := &cpprovider.InstanceStatus{State: cpprovider.StateProvisioning}
	if c := svc.TerminalCondition; c != nil {
		st.Message = c.Message
		switch c.State {
		case "CONDITION_SUCCEEDED":
			st.State, st.Ready = cpprovider.StateRunning, true
		case "CONDITION_FAILED":
			st.State = cpprovider.StateFailed
		}
	}
	if svc.URI != "" {
		st.Endpoints = []cpprovider.Endpoint{{URL: svc.URI, Port: 443, Protocol: "https"}}
	}
	return st, nil
}

// Cloud Run starts, stops and scales services itself, by traffic, and
// keeps its own logs; Loomplane doesn't drive those through Ctrl Plane.

// Start implements cpprovider.Provider.
func (p *Provider) Start(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Stop implements cpprovider.Provider.
func (p *Provider) Stop(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Restart implements cpprovider.Provider.
func (p *Provider) Restart(context.Context, id.ID) error { return ctrlplane.ErrProviderUnavail }

// Rollback implements cpprovider.Provider. Ctrl Plane rolls back by
// deploying an earlier release, which goes through Deploy.
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
