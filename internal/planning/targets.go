package planning

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// OwnedCloud identifies targets with a separate connection for each service.
func OwnedCloud(cloud string) bool { return cloud == "server" || cloud == "kubernetes" }

// ReviewedConnectionSettings copies only placement settings. Credential and
// permission-check metadata can change without changing a destination.
func ReviewedConnectionSettings(cloud string, details map[string]string) map[string]string {
	var keys []string
	switch cloud {
	case "server":
		keys = []string{"host", "port", "user", "host_key", "docker_socket", "advertise_host", "architecture", "docker_id"}
	case "kubernetes":
		keys = []string{"endpoint", "context", "namespace", "namespace_uid", "cluster_ca", "tls_server_name", "ingress_class", "ingress_address", "ingress_domain", "tls_secret", "image_pull_secret", "architecture", "storage_class", "storage_class_uid", "storage_provisioner", "storage_binding", "storage_reclaim", "storage_expansion", "persistent_storage", "registry_hostname"}
	case "registry":
		keys = []string{"endpoint", "repository", "auth_endpoint", "registry_ca"}
	}
	if details["managed_attachment_id"] != "" {
		keys = append(keys, "managed_attachment_id", "managed_project_id", "managed_resource_id")
	}
	var out map[string]string
	for _, k := range keys {
		if v := details[k]; v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = v
		}
	}
	return out
}

func (p *planner) targetConnection(name, path, role, cloud, id string) *ConnectionOption {
	code, message := "", ""
	pullOnly := role == "registry-pull"
	role = strings.TrimSuffix(role, "-pull")
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == id })
	switch {
	case id == "":
		code, message = role+"-required", fmt.Sprintf("Choose a %s connection for %s.", cloudTitle(cloud), name)
	case i < 0:
		code, message = role+"-gone", fmt.Sprintf("%s names a %s connection that isn't in this organization. Choose an existing connection.", name, role)
	case p.in.Connections[i].Cloud != cloud:
		code, message = role+"-cloud", fmt.Sprintf("%s needs a %s %s, but %s is a %s connection.", name, cloudTitle(cloud), role, p.in.Connections[i].Name, cloudTitle(p.in.Connections[i].Cloud))
	case p.in.Connections[i].Details["managed_attachment_id"] != "" && (p.in.ProjectID == "" || p.in.Connections[i].Details["managed_project_id"] != p.in.ProjectID):
		code, message = role+"-project", "This managed namespace belongs to another project."
	case !p.in.Connections[i].Ready && (!pullOnly || !p.in.Connections[i].PullReady):
		code, message = role+"-not-ready", fmt.Sprintf("%s's %s connection %s isn't ready. Check it under Integrations.", name, role, p.in.Connections[i].Name)
	default:
		return &p.in.Connections[i]
	}
	p.problem(Finding{Code: code, Subject: ServiceSubject, Name: name, Path: path, Message: message})
	return nil
}

func (p *planner) configureTarget(name, base string, prov *provider.Provider, set manifest.Settings, cfg *ServiceConfig) {
	if placement, ok := p.in.CloudPlacements[name]; ok {
		c := p.targetConnection(name, base+".cloud_target", "target", prov.Cloud, placement.ConnectionID)
		if c != nil {
			cfg.TargetSettings = ReviewedConnectionSettings(c.Cloud, c.Details)
		}
	}
	if !OwnedCloud(prov.Cloud) {
		if set.Target != "" || set.Endpoint != nil || (set.Build != nil && (set.Build.Builder != "" || set.Build.Registry != "" || set.Build.Image != "")) {
			p.problem(Finding{Code: "target-unsupported", Subject: ServiceSubject, Name: name, Path: base,
				Message: "Target, endpoint and remote build settings need a server or Kubernetes provider."})
		}
		p.serviceGPU(name, base, prov, set, cfg, nil)
		return
	}
	cfg.Target, cfg.Endpoint = set.Target, set.Endpoint
	if cfg.Target == "" && cfg.CapacityIntent == nil {
		cfg.Target = p.in.Chosen[prov.Cloud]
		if cfg.Target == "" {
			if fallback, ok := p.connection(prov.Cloud); ok {
				cfg.Target = fallback.ID
			}
		}
	}
	var c *ConnectionOption
	if cfg.CapacityIntent == nil {
		c = p.targetConnection(name, base+".target", "target", prov.Cloud, cfg.Target)
	} else {
		cfg.TargetSettings = map[string]string{"architecture": cfg.CapacityIntent.Demand.Architecture}
	}
	if c != nil && c.Pooled {
		if pin, ok := p.in.CloudPlacements[name]; !ok || pin.PoolID == "" {
			p.problem(Finding{Code: "pool-target-required", Subject: ServiceSubject, Name: name, Path: base + ".target", Message: "This host belongs to shared capacity. Choose its Cloud pool target so approval reserves the workload."})
		}
	}
	if c != nil {
		cfg.TargetSettings = ReviewedConnectionSettings(c.Cloud, c.Details)
	}
	p.serviceGPU(name, base, prov, set, cfg, c)
	b := set.Build
	if b == nil {
		b = &manifest.Build{}
	}
	build := &BuildTarget{Builder: b.Builder, Registry: b.Registry, Image: b.Image}
	cfg.BuildTarget = build
	if cfg.ManagedKubernetesBuild() {
		p.warn(Finding{Code: "managed-kubernetes-build", Subject: ServiceSubject, Name: name, Path: base + ".build", Message: "This cluster builds with rootless BuildKit and shares a persistent registry across its connection. The first deployment provisions a 20 GiB retained volume, registry credentials and a TLS ingress. Your registry hostname must resolve to that ingress. Removing a service retains its images."})
		if c == nil || c.Details["managed_builds"] != "ready" {
			p.problem(Finding{Code: "kubernetes-build-unavailable", Subject: ServiceSubject, Name: name, Path: base + ".build", Message: "Check this cluster's managed build setup under Integrations. It needs retained storage, cert-manager, ingress, and build pod permissions."})
		}
	} else if build.Image == "" {
		if build.Builder == "" && prov.Cloud == "server" {
			build.Builder = cfg.Target
		}
		if builder := p.targetConnection(name, base+".build.builder", "builder", "server", build.Builder); builder != nil {
			build.BuilderSettings = ReviewedConnectionSettings(builder.Cloud, builder.Details)
			if cfg.CapacityIntent != nil && builder.Details["architecture"] != cfg.CapacityIntent.Demand.Architecture {
				p.problem(Finding{Code: "capacity-architecture", Subject: ServiceSubject, Name: name, Path: base + ".build.builder", Message: "The builder must match the approved pool architecture."})
			}
		}
		if prov.Cloud == "kubernetes" || build.Builder != cfg.Target {
			p.targetConnection(name, base+".build.registry", "registry", "registry", build.Registry)
		}
	}
	if build.Registry != "" {
		role := "registry"
		if build.Image != "" {
			role = "registry-pull"
		}
		if registry := p.targetConnection(name, base+".build.registry", role, "registry", build.Registry); registry != nil {
			build.RegistrySettings = ReviewedConnectionSettings(registry.Cloud, registry.Details)
		}
	}
	cfg.BuildTarget = build
	if cfg.Image == "" && cfg.Ecosystem != "go" && b.Image == "" {
		p.problem(Finding{Code: "dockerfile-required", Subject: ServiceSubject, Name: name, Path: base + ".build",
			Message: "This source needs a Dockerfile for a remote container build. You can also choose a digest-pinned image."})
	}
	if cfg.Image == "" && b.Context != "" {
		cfg.Context = b.Context
	}
	if prov.Cloud == "server" && cfg.Scale != nil && (cfg.Scale.Min != 1 || cfg.Scale.Max != 1) {
		p.problem(Finding{Code: "server-scale", Subject: ServiceSubject, Name: name, Path: base + ".scale", Message: "An existing server runs one container per service. Use min: 1 and max: 1, or choose Kubernetes for replicas."})
	}
	if prov.Cloud == "kubernetes" && cfg.Scale != nil && cfg.Scale.Min != cfg.Scale.Max && (c == nil || c.Details["hpa"] != "ready") {
		p.problem(Finding{Code: "hpa-unavailable", Subject: ServiceSubject, Name: name, Path: base + ".scale", Message: "Elastic Kubernetes scaling needs the metrics API and namespace HPA permissions. Check the cluster connection or choose a fixed replica count."})
	}
	if prov.Cloud == "kubernetes" && cfg.Scale != nil && cfg.Scale.Min != cfg.Scale.Max && cfg.Scale.Min == 0 {
		p.problem(Finding{Code: "hpa-minimum", Subject: ServiceSubject, Name: name, Path: base + ".scale.min", Message: "CPU-based Kubernetes autoscaling needs at least one replica. Choose min: 1 or a fixed replica count."})
	}
	if cfg.Public && prov.Cloud == "server" && (cfg.Endpoint == nil || cfg.Endpoint.HostPort == 0) {
		p.problem(Finding{Code: "host-port-required", Subject: ServiceSubject, Name: name, Path: base + ".endpoint.host_port", Message: "Choose the host port this server publishes for the service."})
	}
	if cfg.Public && prov.Cloud == "kubernetes" && (cfg.Endpoint == nil || cfg.Endpoint.Hostname == "") {
		p.problem(Finding{Code: "ingress-required", Subject: ServiceSubject, Name: name, Path: base + ".endpoint.hostname", Message: "Choose an ingress hostname for this public Kubernetes service."})
	}
	if cfg.Public && prov.Cloud == "kubernetes" && c != nil && c.Details["ingress"] != "ready" {
		p.problem(Finding{Code: "ingress-unavailable", Subject: ServiceSubject, Name: name, Path: base + ".endpoint", Message: "This connection needs namespace ingress permissions to deploy a public service. Check the connection after granting them."})
	}
}

// ManagedKubernetesBuild uses the cluster's managed registry when the service
// has no explicit external builder, registry or existing image.
func (c *ServiceConfig) ManagedKubernetesBuild() bool {
	return c != nil && c.Provider == "kubernetes" && c.TargetSettings["registry_hostname"] != "" && c.BuildTarget != nil && c.BuildTarget.Builder == "" && c.BuildTarget.Registry == "" && c.BuildTarget.Image == ""
}

// ImageRegistry identifies the registry across promotions without pretending
// that a cluster's managed registry is a separate credential connection.
func (c *ServiceConfig) ImageRegistry() string {
	if c.ManagedKubernetesBuild() {
		return "kubernetes/" + c.Target + "/" + c.TargetSettings["namespace_uid"] + "/" + c.TargetSettings["registry_hostname"]
	}
	if c != nil && c.BuildTarget != nil {
		return c.BuildTarget.Registry
	}
	return ""
}

func sameTargetNetwork(a, b *ServiceConfig, cloudA, cloudB string) bool {
	if a == nil || b == nil || cloudA != cloudB || !OwnedCloud(cloudA) || a.Target == "" || b.Target == "" {
		return false
	}
	if a.Target == b.Target {
		return true
	}
	if cloudA == "server" {
		return a.TargetSettings["docker_id"] != "" && a.TargetSettings["docker_id"] == b.TargetSettings["docker_id"]
	}
	return a.TargetSettings["endpoint"] != "" && a.TargetSettings["endpoint"] == b.TargetSettings["endpoint"] &&
		a.TargetSettings["namespace_uid"] != "" && a.TargetSettings["namespace_uid"] == b.TargetSettings["namespace_uid"]
}

func (p *planner) checkTargetPlacements() {
	ports := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		a := p.services[name]
		cfg := a.Service
		if cfg == nil || a.Provider != "server" || !cfg.Public || cfg.Endpoint == nil || cfg.Endpoint.HostPort == 0 {
			continue
		}
		scope := cfg.TargetSettings["docker_id"]
		if scope == "" {
			scope = cfg.Target
		}
		key := fmt.Sprintf("%s:%d", scope, cfg.Endpoint.HostPort)
		if other := ports[key]; other != "" {
			p.problem(Finding{Code: "target-port-conflict", Subject: ServiceSubject, Name: name, Path: "services." + name + ".endpoint.host_port",
				Message: fmt.Sprintf("%s and %s publish host port %d on the same server. Choose different ports.", name, other, cfg.Endpoint.HostPort)})
		}
		ports[key] = name
	}
}
