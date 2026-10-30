package cloudinfra

import (
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type AttachmentInput struct {
	RuntimeAccess       *provider.RuntimeAccessProfile `json:"runtime_access,omitempty"`
	TargetSlug          string                         `json:"target_slug"`
	CloudRevision       int64                          `json:"cloud_revision"`
	ResourceRevision    int64                          `json:"resource_revision"`
	GrantRevision       int64                          `json:"grant_revision"`
	TrustFingerprint    string                         `json:"trust_fingerprint"`
	EndpointFingerprint string                         `json:"endpoint_fingerprint"`
	Prerequisites       AttachmentPrerequisites        `json:"prerequisites"`
	ProjectID           string                         `json:"project_id"`
	Namespace           string                         `json:"namespace"`
	NamespaceUID        string                         `json:"namespace_uid,omitempty"`
	ServiceAccountName  string                         `json:"service_account_name"`
	AdoptNamespace      bool                           `json:"adopt_namespace"`
}

type ClusterInput struct {
	RuntimeAccess                 *provider.RuntimeAccessInput `json:"runtime_access,omitempty"`
	ScalingObservation            *ClusterScalingObservation   `json:"scaling_observation,omitempty"`
	Scaling                       *ClusterScalingPolicy        `json:"scaling,omitempty"`
	BudgetReview                  *BudgetReview                `json:"budget_review,omitempty"`
	EnvelopeReview                *ClusterEnvelopeReview       `json:"envelope_review,omitempty"`
	costEvidence                  *MoneyEstimate
	PriceDigest                   string                   `json:"price_digest,omitempty"`
	Retirement                    *ClusterRetirementInput  `json:"retirement,omitempty"`
	Maintenance                   *ClusterMaintenanceInput `json:"maintenance,omitempty"`
	ManagementQualificationDigest string                   `json:"management_qualification_digest"`
	ManagementEvidenceID          string                   `json:"management_evidence_id"`
	PolicyRevision                int64                    `json:"policy_revision"`
	// ManagementReachable is caller acknowledgement, never path evidence.
	ManagementReachable     bool                 `json:"management_reachable"`
	AcknowledgeUnknownCosts bool                 `json:"acknowledge_unknown_costs"`
	AcknowledgeNodeCosts    bool                 `json:"acknowledge_node_costs"`
	WorkerImage             string               `json:"worker_image"`
	Kind                    string               `json:"kind"`
	Provider                string               `json:"provider"`
	ConnectionID            string               `json:"connection_id"`
	AccountID               string               `json:"account_id"`
	ResourceID              string               `json:"resource_id,omitempty"`
	ResourceRevision        int64                `json:"resource_revision"`
	ConnectionGeneration    int64                `json:"connection_generation"`
	CloudRevision           int64                `json:"cloud_revision"`
	OptionsDigest           string               `json:"options_digest"`
	OptionsCheckedAt        time.Time            `json:"options_checked_at"`
	Spec                    provider.ClusterSpec `json:"spec"`
	Ref                     provider.ClusterRef  `json:"ref"`
	Attachment              *AttachmentInput     `json:"attachment,omitempty"`
}

type ClusterStep struct {
	Name              string     `json:"name"`
	State             string     `json:"state"`
	RequestDigest     string     `json:"request_digest"`
	RemoteOperationID string     `json:"remote_operation_id,omitempty"`
	RemoteResourceID  string     `json:"remote_resource_id,omitempty"`
	AttemptedAt       *time.Time `json:"attempted_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type ClusterOperation struct {
	RuntimeAccess               *kubernetes.RuntimeAccessState `json:"runtime_access,omitempty"`
	MoneyEstimate               *MoneyEstimate                 `json:"money_estimate,omitempty"`
	Enrollment                  *ClusterEnrollment             `json:"enrollment,omitempty"`
	EstimatedNodeHourlyMicroUSD int64                          `json:"estimated_node_hourly_micro_usd"`
	UnknownCosts                []string                       `json:"unknown_costs"`
	CapacityTarget              ResourceVector                 `json:"capacity_target"`
	Attempt                     int                            `json:"attempt"`
	CancelRequested             bool                           `json:"cancel_requested"`
	NextAttemptAt               time.Time                      `json:"next_attempt_at,omitempty,omitzero"`
	ID                          string                         `json:"id"`
	OrgID                       string                         `json:"org_id"`
	CloudID                     string                         `json:"cloud_id"`
	ResourceID                  string                         `json:"resource_id,omitempty"`
	Kind                        string                         `json:"kind"`
	SchemaVersion               int                            `json:"schema_version"`
	Input                       ClusterInput                   `json:"input"`
	Digest                      string                         `json:"digest"`
	Revision                    int64                          `json:"revision"`
	Status                      string                         `json:"status"`
	Principal                   security.Principal             `json:"-"`
	Receipts                    []provider.ClusterMutation     `json:"receipts,omitempty"`
	Steps                       []ClusterStep                  `json:"steps"`
	ReservationIDs              []string                       `json:"reservation_ids,omitempty"`
	LeaseOwner                  string                         `json:"lease_owner,omitempty"`
	FencingEpoch                int64                          `json:"fencing_epoch"`
	LeaseExpiresAt              *time.Time                     `json:"lease_expires_at,omitempty"`
	ExpiresAt                   time.Time                      `json:"expires_at"`
	CreatedAt                   time.Time                      `json:"created_at"`
	UpdatedAt                   time.Time                      `json:"updated_at"`
	Error                       string                         `json:"error,omitempty"`
}

// AttachmentPrerequisites records the reviewed features enrollment must verify.
type AttachmentPrerequisites struct {
	RegistryHostname         string `json:"registry_hostname,omitempty"`
	IngressAddress           string `json:"ingress_address,omitempty"`
	StorageClass             string `json:"storage_class,omitempty"`
	RequirePersistentStorage bool   `json:"require_persistent_storage"`
	RequireBuilds            bool   `json:"require_builds"`
	RequireIngress           bool   `json:"require_ingress"`
	RequireTLS               bool   `json:"require_tls"`
}

// ClusterEnrollment records materialized identities separately from approval input.
type ClusterEnrollment struct {
	RetiredVaultKeys    []string                   `json:"retired_vault_keys,omitempty"`
	AttachmentID        string                     `json:"attachment_id"`
	RuntimeConnectionID string                     `json:"runtime_connection_id"`
	TargetID            string                     `json:"target_id"`
	ResourceRevision    int64                      `json:"resource_revision"`
	State               kubernetes.EnrollmentState `json:"state"`
	VaultKey            string                     `json:"vault_key,omitempty"`
	CredentialExpiry    time.Time                  `json:"credential_expiry,omitempty,omitzero"`
}
