package cloudinfra

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/pkg/provider"
)

// Adapters maps implemented account operations independently of deployment
// targets. Register adapters at startup; lookups do not mutate the registry.
type Adapters struct {
	clusters     map[string]provider.ClusterReader
	servers      map[string]provider.ServerAdapter
	dns          map[string]provider.DNSAdapter
	registrars   map[string]provider.DomainRegistrar
	platforms    map[string]PlatformDomains
	capabilities map[string][]provider.InfrastructureCapability
}

// NewAdapters creates an empty registry for configured or fixture adapters.
func NewAdapters() *Adapters {
	return &Adapters{clusters: map[string]provider.ClusterReader{}, servers: map[string]provider.ServerAdapter{}, dns: map[string]provider.DNSAdapter{}, registrars: map[string]provider.DomainRegistrar{}, platforms: map[string]PlatformDomains{}, capabilities: map[string][]provider.InfrastructureCapability{}}
}

// Endpoints are the provider API addresses the application is configured
// with. An empty one means the provider's own.
type Endpoints struct {
	DigitalOcean, Vercel, Cloudflare string
	// CloudflareRegistrarSandbox sends Cloudflare registrar calls to its
	// sandbox, which never charges anyone.
	CloudflareRegistrarSandbox bool
	// Lock is the named lock every replica shares, as digitalocean.Client
	// takes it. The deploy runner's DigitalOcean client must share it too,
	// so a hostname change and a deploy take turns at an app's spec.
	Lock func(ctx context.Context, name string, fn func(context.Context) error) error
}

// BuiltinAdapters preserves the application's configured provider endpoints.
func BuiltinAdapters(e Endpoints) *Adapters {
	r := NewAdapters()
	d := digitalocean.New(e.DigitalOcean)
	d.Lock = e.Lock
	r.servers["digitalocean"] = d
	r.clusters["digitalocean"] = d
	r.dns["digitalocean"] = d
	r.platforms["digitalocean"] = newAppPlatformDomains(d)
	r.dns["vercel"] = vercel.New(e.Vercel)
	cf := cloudflare.New(e.Cloudflare, e.CloudflareRegistrarSandbox)
	r.dns["cloudflare"] = cf
	r.registrars["cloudflare"] = cf
	r.capabilities["digitalocean"] = digitalocean.InfrastructureCapabilities()
	r.capabilities["vercel"] = vercel.InfrastructureCapabilities()
	r.capabilities["cloudflare"] = cloudflare.InfrastructureCapabilities()
	return r
}

var serverOperations = []provider.InfrastructureCapability{provider.ServerDiscovery, provider.ServerProvisioning, provider.ServerResize, provider.ServerDeletion}
var dnsOperations = []provider.InfrastructureCapability{provider.DNSZoneDiscovery, provider.DNSZoneCreation, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSRecordDeletion}
var registrarOperations = []provider.InfrastructureCapability{provider.DomainRegistration}

func (r *Adapters) declare(name string, requested, allowed []provider.InfrastructureCapability) error {
	if len(requested) == 0 {
		requested = allowed
	}
	for _, capability := range requested {
		if !slices.Contains(allowed, capability) {
			return &provider.UnsupportedCapabilityError{Provider: name, Capability: capability}
		}
	}
	for _, capability := range requested {
		if !slices.Contains(r.capabilities[name], capability) {
			r.capabilities[name] = append(r.capabilities[name], capability)
		}
	}
	return nil
}

// RegisterServer adds one implementation with its supported operation subset.
// Omitting operations declares the complete ServerAdapter method set.
func (r *Adapters) RegisterServer(name string, adapter provider.ServerAdapter, operations ...provider.InfrastructureCapability) error {
	if name == "" || adapter == nil || r.servers[name] != nil {
		return fmt.Errorf("%w: duplicate or empty server adapter", provider.ErrInfrastructureInvalid)
	}
	if err := r.declare(name, operations, serverOperations); err != nil {
		return err
	}
	r.servers[name] = adapter
	return nil
}

// RegisterDNS adds independent DNS with its supported operation subset.
// Omitting operations declares the complete DNSAdapter method set.
func (r *Adapters) RegisterDNS(name string, adapter provider.DNSAdapter, operations ...provider.InfrastructureCapability) error {
	if name == "" || adapter == nil || r.dns[name] != nil {
		return fmt.Errorf("%w: duplicate or empty DNS adapter", provider.ErrInfrastructureInvalid)
	}
	if err := r.declare(name, operations, dnsOperations); err != nil {
		return err
	}
	r.dns[name] = adapter
	return nil
}

// RegisterRegistrar adds domain registration, a capability separate from DNS.
func (r *Adapters) RegisterRegistrar(name string, adapter provider.DomainRegistrar) error {
	if name == "" || adapter == nil || r.registrars[name] != nil {
		return fmt.Errorf("%w: duplicate or empty registrar adapter", provider.ErrInfrastructureInvalid)
	}
	if err := r.declare(name, nil, registrarOperations); err != nil {
		return err
	}
	r.registrars[name] = adapter
	return nil
}

// RegisterPlatform adds hostnames on a provider's app platform.
func (r *Adapters) RegisterPlatform(name string, adapter PlatformDomains) error {
	if name == "" || adapter == nil || r.platforms[name] != nil {
		return fmt.Errorf("%w: duplicate or empty platform adapter", provider.ErrInfrastructureInvalid)
	}
	r.platforms[name] = adapter
	return nil
}

// Require checks operation support during preview and again during execution.
// Installed code does not establish the connection's read or write permission.
func (r *Adapters) Require(name string, operation provider.InfrastructureCapability) error {
	if slices.Contains(r.capabilities[name], operation) {
		return nil
	}
	return &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
}

// Server returns the server API, optionally requiring specific operations.
func (r *Adapters) Server(name string, operations ...provider.InfrastructureCapability) (provider.ServerAdapter, error) {
	if adapter := r.servers[name]; adapter != nil {
		for _, operation := range operations {
			if !slices.Contains(serverOperations, operation) {
				return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
			}
			if err := r.Require(name, operation); err != nil {
				return nil, err
			}
		}
		return adapter, nil
	}
	operation := provider.ServerProvisioning
	if len(operations) > 0 {
		operation = operations[0]
	}
	return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
}

// DNS returns the DNS API, optionally requiring specific operations.
func (r *Adapters) DNS(name string, operations ...provider.InfrastructureCapability) (provider.DNSAdapter, error) {
	if adapter := r.dns[name]; adapter != nil {
		for _, operation := range operations {
			if !slices.Contains(dnsOperations, operation) {
				return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
			}
			if err := r.Require(name, operation); err != nil {
				return nil, err
			}
		}
		return adapter, nil
	}
	operation := provider.DNSManagement
	if len(operations) > 0 {
		operation = operations[0]
	}
	return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
}

// Registrar returns the domain registration API.
func (r *Adapters) Registrar(name string) (provider.DomainRegistrar, error) {
	if adapter := r.registrars[name]; adapter != nil {
		return adapter, nil
	}
	return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: provider.DomainRegistration}
}

// Platform returns the hostnames API of a provider's app platform.
func (r *Adapters) Platform(name string) (PlatformDomains, error) {
	if adapter := r.platforms[name]; adapter != nil {
		return adapter, nil
	}
	return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: "domains"}
}

// Capabilities returns implemented operations, sorted for public API output.
func (r *Adapters) Capabilities(name string) []provider.InfrastructureCapability {
	result := slices.Clone(r.capabilities[name])
	slices.Sort(result)
	return result
}

// Providers returns the implemented account providers in stable order.
func (r *Adapters) Providers() []string {
	names := map[string]bool{}
	for name := range r.clusters {
		names[name] = true
	}
	for name := range r.servers {
		names[name] = true
	}
	for name := range r.dns {
		names[name] = true
	}
	for name := range r.registrars {
		names[name] = true
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// RegisterCluster registers reads and explicitly declared optional operations.
// Implementing an optional method alone does not advertise that operation.
func (r *Adapters) RegisterCluster(name string, reader provider.ClusterReader, operations ...provider.InfrastructureCapability) error {
	if name == "" || reader == nil || r.clusters[name] != nil {
		return fmt.Errorf("%w: duplicate or empty cluster adapter", provider.ErrInfrastructureInvalid)
	}
	allowed := []provider.InfrastructureCapability{provider.ClusterDiscovery, provider.ClusterOptionsRead}
	for _, operation := range operations {
		supported := false
		switch operation {
		case provider.ClusterDiscovery, provider.ClusterOptionsRead:
			supported = true
		case provider.ClusterDeletion:
			_, supported = reader.(provider.ClusterRetirer)
		case provider.ClusterCreation:
			_, supported = reader.(provider.ClusterCreator)
		case provider.ClusterMutationObservation:
			_, supported = reader.(provider.ClusterObserver)
		case provider.ClusterConfigurationUpdate, provider.ClusterUpgrade:
			_, supported = reader.(provider.ClusterMaintainer)
		case provider.NodePoolAutoscaling:
			_, supported = reader.(provider.NodePoolScalingManager)
		case provider.NodePoolCreation, provider.NodePoolUpdate, provider.NodePoolDeletion:
			_, supported = reader.(provider.NodePoolManager)
		case provider.ClusterCapacityObservation:
			_, supported = reader.(provider.ClusterCapacityReader)
		case provider.ClusterBootstrapAccess:
			_, supported = reader.(provider.ClusterBootstrap)
		}
		if !supported {
			return &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
		}
		allowed = append(allowed, operation)
	}
	if err := r.declare(name, allowed, allowed); err != nil {
		return err
	}
	r.clusters[name] = reader
	return nil
}

// Cluster checks installed capability before callers resolve an optional interface.
func (r *Adapters) Cluster(name string, operation provider.InfrastructureCapability) (provider.ClusterReader, error) {
	reader := r.clusters[name]
	switch operation {
	case provider.NodePoolAutoscaling, provider.ClusterCapacityObservation, provider.ClusterDeletion, provider.ClusterDiscovery, provider.ClusterOptionsRead, provider.ClusterCreation, provider.ClusterMutationObservation, provider.ClusterBootstrapAccess, provider.ClusterConfigurationUpdate, provider.ClusterUpgrade, provider.NodePoolCreation, provider.NodePoolUpdate, provider.NodePoolDeletion:
		if reader != nil && slices.Contains(r.capabilities[name], operation) {
			return reader, nil
		}
	}
	return nil, &provider.UnsupportedCapabilityError{Provider: name, Capability: operation}
}
