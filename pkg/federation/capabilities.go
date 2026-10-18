package federation

import "time"

// Capability is an independently qualified destination operation.
type Capability string

// Capability names stay stable across drivers and stored snapshots.
const (
	WorkspaceAuth       Capability = "auth.workspace"
	Observe             Capability = "lifecycle.observe"
	ImmutableArtifact   Capability = "lifecycle.immutable"
	LifecycleControl    Capability = "lifecycle.control"
	SecretDelivery      Capability = "secrets.deliver"
	ReadLogs            Capability = "logs.read"
	WorkloadAttestation Capability = "identity.workload"
	GatewayAttestation  Capability = "identity.gateway"
	StableDNS           Capability = "dns.stable"
	ImportedNetwork     Capability = "network.imported"
	IPsecNetwork        Capability = "network.ipsec"
	RedundantNetwork    Capability = "network.redundant"
	BackendIsolation    Capability = "network.backend_isolation"
	PolicyEnforcement   Capability = "policy.enforce"
	HTTPProtocol        Capability = "protocol.http"
	GRPCProtocol        Capability = "protocol.grpc"
	TCPProtocol         Capability = "protocol.tcp"
)

// CapabilityState records qualification, not provider marketing support.
type CapabilityState string

// Only Supported may authorize an operation, and only in a fresh snapshot.
const (
	Supported   CapabilityState = "supported"
	Unsupported CapabilityState = "unsupported"
	Unverified  CapabilityState = "unverified"
)

// CapabilityResult records why a feature is available or unavailable.
type CapabilityResult struct {
	State  CapabilityState `json:"state"`
	Reason string          `json:"reason,omitempty"`
}

// CapabilitySet is a bounded observation pinned to one physical identity.
// A missing capability, revision or time window always denies availability.
type CapabilitySet struct {
	Revision         string                          `json:"revision"`
	IdentityRevision string                          `json:"identity_revision"`
	CheckedAt        time.Time                       `json:"checked_at"`
	ExpiresAt        time.Time                       `json:"expires_at"`
	Results          map[Capability]CapabilityResult `json:"results"`
}

// Fresh reports whether this observation still belongs to the expected identity.
func (c CapabilitySet) Fresh(identity string, now time.Time) bool {
	return identity != "" && c.IdentityRevision == identity && c.Revision != "" && !c.CheckedAt.IsZero() && !c.CheckedAt.After(now) && c.ExpiresAt.After(now) && c.ExpiresAt.After(c.CheckedAt)
}

// Supports fails closed for stale, unknown or differently scoped observations.
func (c CapabilitySet) Supports(capability Capability, identity string, now time.Time) bool {
	return c.Fresh(identity, now) && c.Results[capability].State == Supported
}
