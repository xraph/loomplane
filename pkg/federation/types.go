// Package federation defines portable contracts for destination federation.
// Provider support and observed destination capabilities are separate facts.
package federation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Scope identifies the tenant, project and environment owning an object.
type Scope struct {
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
}

// Ownership records who may remove a physical resource.
type Ownership string

// Imported resources remain under their existing controller's ownership.
const Imported Ownership = "imported"

// PhysicalIdentity is verified metadata, never supplied by an API caller.
// NamespaceUID identifies a Kubernetes namespace even through connection aliases.
type PhysicalIdentity struct {
	Provider     string `json:"provider"`
	Account      string `json:"account"`
	ResourceID   string `json:"resource_id"`
	ClusterUID   string `json:"cluster_uid,omitempty"`
	NamespaceUID string `json:"namespace_uid,omitempty"`
}

// Revision returns a stable identity key, independent of names and credentials.
func (p PhysicalIdentity) Revision() string {
	if p.Provider == "" || p.Account == "" || p.ResourceID == "" {
		return ""
	}
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Destination is a scoped logical alias for a verified physical runtime.
type Destination struct {
	Scope
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	ConnectionID string           `json:"connection_id"`
	Ownership    Ownership        `json:"ownership"`
	Identity     PhysicalIdentity `json:"identity"`
	Generation   int64            `json:"generation"`
	Capabilities CapabilitySet    `json:"capabilities"`
	Quarantined  bool             `json:"quarantined"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

// Protocol is an application protocol supported by the federation contract.
type Protocol string

// Supported protocol names. Availability still requires observed evidence.
const (
	HTTP Protocol = "http"
	GRPC Protocol = "grpc"
	TCP  Protocol = "tcp"
)

// IdentityProfile distinguishes workload proof from a shared gateway principal.
type IdentityProfile string

// Gateway identity cannot satisfy a requirement for an individual workload.
const (
	WorkloadIdentity IdentityProfile = "workload"
	GatewayIdentity  IdentityProfile = "gateway"
)
