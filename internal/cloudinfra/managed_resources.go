package cloudinfra

import (
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// ManagedResource retains physical identity after deletion.
type ManagedResource struct {
	RetainedResources  []provider.AssociatedResource `json:"retained_resources,omitempty"`
	SchemaVersion      int                           `json:"schema_version"`
	CreatedAt          time.Time                     `json:"created_at"`
	UpdatedAt          time.Time                     `json:"updated_at"`
	ID                 string                        `json:"id"`
	OrgID              string                        `json:"org_id"`
	CloudID            string                        `json:"cloud_id,omitempty"`
	Kind               string                        `json:"kind"`
	Provider           string                        `json:"provider"`
	AccountID          string                        `json:"account_id"`
	ConnectionID       string                        `json:"connection_id,omitempty"`
	ExternalID         string                        `json:"external_id"`
	Incarnation        string                        `json:"incarnation"`
	Ownership          string                        `json:"ownership"`
	LifecycleAuthority []string                      `json:"lifecycle_authority"`
	Revision           int64                         `json:"revision"`
	DesiredRevision    int64                         `json:"desired_revision"`
	ObservedRevision   int64                         `json:"observed_revision"`
	Status             string                        `json:"status"`
	CreationReceipt    *provider.ClusterMutation     `json:"creation_receipt,omitempty"`
	Observation        *provider.ClusterObservation  `json:"observation,omitempty"`
	ObservedAt         time.Time                     `json:"observed_at"`
	DeletedAt          *time.Time                    `json:"deleted_at,omitempty"`
}

type ResourceRelation struct {
	OrgID         string `json:"org_id"`
	SourceID      string `json:"source_id"`
	DestinationID string `json:"destination_id"`
	Kind          string `json:"kind"`
	Revision      int64  `json:"revision"`
}

type RuntimeAttachment struct {
	RetirementOperationID string                      `json:"retirement_operation_id,omitempty"`
	OperationID           string                      `json:"operation_id"`
	VaultKey              string                      `json:"vault_key"`
	SchemaVersion         int                         `json:"schema_version"`
	CreatedAt             time.Time                   `json:"created_at"`
	UpdatedAt             time.Time                   `json:"updated_at"`
	Ownership             string                      `json:"ownership"`
	ServiceAccountName    string                      `json:"service_account_name"`
	CredentialVersion     int64                       `json:"credential_version"`
	RBACObjects           []RBACObjectReceipt         `json:"rbac_objects,omitempty"`
	ID                    string                      `json:"id"`
	OrgID                 string                      `json:"org_id"`
	CloudID               string                      `json:"cloud_id"`
	ClusterID             string                      `json:"cluster_id"`
	ProjectID             string                      `json:"project_id"`
	Namespace             string                      `json:"namespace"`
	NamespaceUID          string                      `json:"namespace_uid"`
	ServiceAccountUID     string                      `json:"service_account_uid"`
	RuntimeConnectionID   string                      `json:"runtime_connection_id"`
	EndpointFingerprint   string                      `json:"endpoint_fingerprint"`
	TrustFingerprint      string                      `json:"trust_fingerprint"`
	TrustRevision         int64                       `json:"trust_revision"`
	Revision              int64                       `json:"revision"`
	CredentialExpiry      time.Time                   `json:"credential_expiry"`
	Conditions            []provider.ClusterCondition `json:"conditions"`
	DeletedAt             *time.Time                  `json:"deleted_at,omitempty"`
}

type ResourcePage struct {
	Items      []*ManagedResource `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func resourcePageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// RBACObjectReceipt pins the objects enrollment may later reconcile or remove.
type RBACObjectReceipt struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}
