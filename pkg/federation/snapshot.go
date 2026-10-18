package federation

import "time"

// Metadata is the immutable identity and current version of a scoped object.
type Metadata struct {
	Scope
	ID         string    `json:"id"`
	Generation int64     `json:"generation"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Readiness is an observed endpoint condition, never an access decision.
type Readiness string

// Endpoint conditions fail closed until Ready is explicitly observed.
const (
	Unknown  Readiness = "unknown"
	Ready    Readiness = "ready"
	Draining Readiness = "draining"
	Unready  Readiness = "unready"
)

// NetworkLink describes a directed path. Credentials stay on connections.
type NetworkLink struct {
	Metadata
	SourceDestinationID string               `json:"source_destination_id"`
	TargetDestinationID string               `json:"target_destination_id"`
	Ownership           Ownership            `json:"ownership"`
	Transport           string               `json:"transport"`
	CIDRs               []string             `json:"cidrs"`
	DNSStrategy         string               `json:"dns_strategy"`
	MTU                 int                  `json:"mtu"`
	Components          []ComponentReference `json:"components"`
}

// ComponentReference retains the credential owner of an external resource.
type ComponentReference struct {
	ConnectionID string `json:"connection_id"`
	ExternalID   string `json:"external_id"`
}

// ServiceExport publishes a service contract without authorizing any caller.
type ServiceExport struct {
	Metadata
	ServiceID       string          `json:"service_id"`
	DestinationIDs  []string        `json:"destination_ids"`
	Port            uint16          `json:"port"`
	Protocol        Protocol        `json:"protocol"`
	IdentityProfile IdentityProfile `json:"identity_profile"`
}

// WorkloadSelector is an exact service identity within a scoped destination.
type WorkloadSelector struct {
	Scope
	DestinationID string `json:"destination_id"`
	ServiceID     string `json:"service_id"`
}

// AccessGrant records explicit authorization; proposal alone is not approval.
type AccessGrant struct {
	Metadata
	Source           WorkloadSelector `json:"source"`
	ExportID         string           `json:"export_id"`
	ExportRevision   string           `json:"export_revision"`
	Port             uint16           `json:"port"`
	Protocol         Protocol         `json:"protocol"`
	HTTPMethods      []string         `json:"http_methods,omitempty"`
	HTTPPaths        []string         `json:"http_paths,omitempty"`
	GRPCMethods      []string         `json:"grpc_methods,omitempty"`
	OwnerID          string           `json:"owner_id"`
	ApprovedRevision string           `json:"approved_revision,omitempty"`
	ExpiresAt        *time.Time       `json:"expires_at,omitempty"`
	RevokedAt        *time.Time       `json:"revoked_at,omitempty"`
}

// Endpoint is a leased observation of a service placement.
type Endpoint struct {
	Metadata
	DestinationID string    `json:"destination_id"`
	ExportID      string    `json:"export_id"`
	PlacementID   string    `json:"placement_id"`
	BindingID     string    `json:"binding_id"`
	ObserverID    string    `json:"observer_id"`
	Address       string    `json:"address"`
	Port          uint16    `json:"port"`
	Zone          string    `json:"zone"`
	Readiness     Readiness `json:"readiness"`
	ObservedAt    time.Time `json:"observed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// AgentIdentity binds an agent to a destination and restore/revocation epoch.
type AgentIdentity struct {
	Scope
	AgentID       string `json:"agent_id"`
	DestinationID string `json:"destination_id"`
	URI           string `json:"uri"`
	Epoch         uint64 `json:"epoch"`
}

// ObservationBatch fences a destination observer's monotonic report.
type ObservationBatch struct {
	Agent     AgentIdentity `json:"agent"`
	Sequence  uint64        `json:"sequence"`
	Endpoints []Endpoint    `json:"endpoints"`
}

// Snapshot is an expiring, signed routing and policy envelope. Consumers must
// verify its signature, scope, epoch and revision before applying its payload.
type Snapshot struct {
	Scope
	DestinationID string       `json:"destination_id"`
	Epoch         uint64       `json:"epoch"`
	Sequence      uint64       `json:"sequence"`
	Revision      string       `json:"revision"`
	IssuedAt      time.Time    `json:"issued_at"`
	ExpiresAt     time.Time    `json:"expires_at"`
	Endpoints     []Endpoint   `json:"endpoints"`
	Policy        PolicyBundle `json:"policy"`
	KeyID         string       `json:"key_id"`
	Signature     []byte       `json:"signature"`
}

// SnapshotRequest identifies the agent's last applied snapshot.
type SnapshotRequest struct {
	Agent         AgentIdentity `json:"agent"`
	AfterSequence uint64        `json:"after_sequence"`
}

// Acknowledgment records installation of one exact snapshot revision.
type Acknowledgment struct {
	Agent     AgentIdentity `json:"agent"`
	Sequence  uint64        `json:"sequence"`
	Revision  string        `json:"revision"`
	AppliedAt time.Time     `json:"applied_at"`
}

// EnrollmentRequest names a destination to enroll; it cannot select its scope.
type EnrollmentRequest struct {
	DestinationID string `json:"destination_id"`
}

// Enrollment describes an expiring enrollment handle; secrets are delivered separately.
type Enrollment struct {
	Metadata
	DestinationID string     `json:"destination_id"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
}

// ProbeRequest names an exact exported service and caller for a reachability check.
type ProbeRequest struct {
	Source   WorkloadSelector `json:"source"`
	ExportID string           `json:"export_id"`
	Protocol Protocol         `json:"protocol"`
	Port     uint16           `json:"port"`
}

// ProbeResult records scoped evidence without granting authority.
type ProbeResult struct {
	Request    ProbeRequest `json:"request"`
	Successful bool         `json:"successful"`
	CheckedAt  time.Time    `json:"checked_at"`
	ExpiresAt  time.Time    `json:"expires_at"`
}

// Placement binds an immutable artifact to one destination's local controller.
type Placement struct {
	Metadata
	ServiceID     string `json:"service_id"`
	DestinationID string `json:"destination_id"`
	Artifact      string `json:"artifact"`
	Replicas      int    `json:"replicas"`
	TrafficWeight int    `json:"traffic_weight"`
}

// CleanupOperation retains external ownership until absence is verified.
type CleanupOperation struct {
	Metadata
	Components  []ComponentReference `json:"components"`
	Attempts    int                  `json:"attempts"`
	RetryAt     *time.Time           `json:"retry_at,omitempty"`
	FailureCode string               `json:"failure_code,omitempty"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
}

// PolicyInput contains scoped exports and explicit grants for compilation.
type PolicyInput struct {
	Scope
	Exports []ServiceExport `json:"exports"`
	Grants  []AccessGrant   `json:"grants"`
}

// PolicyBundle carries a versioned policy with a bounded lifetime.
type PolicyBundle struct {
	Revision  string        `json:"revision"`
	ExpiresAt time.Time     `json:"expires_at"`
	Grants    []AccessGrant `json:"grants"`
}
