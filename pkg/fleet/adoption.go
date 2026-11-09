package fleet

import "time"

// AdoptionResourceInput selects an inventory incarnation and its intended use.
// Ownership is explicit: owned, attached or shared. It does not prove ownership.
type AdoptionResourceInput struct {
	InventoryID string `json:"inventory_id"`
	ExternalID  string `json:"external_id"`
	Incarnation string `json:"incarnation"`
	Revision    int64  `json:"revision"`
	Ownership   string `json:"ownership"`
}

// AdoptionInput requests read-only review of an existing workload and its data.
// Controller and authority IDs are declarations to verify, never credentials.
type AdoptionInput struct {
	CloudID            string                  `json:"cloud_id"`
	ClusterID          string                  `json:"cluster_id"`
	AttachmentID       string                  `json:"attachment_id"`
	AttachmentRevision int64                   `json:"attachment_revision"`
	Namespace          string                  `json:"namespace"`
	NamespaceUID       string                  `json:"namespace_uid"`
	OldControllerID    string                  `json:"old_controller_id"`
	OldAuthorityID     string                  `json:"old_authority_id"`
	OwnershipEpoch     int64                   `json:"ownership_epoch"`
	Resources          []AdoptionResourceInput `json:"resources"`
}

// AdoptionBlocker explains why the reviewed workload cannot be transferred.
type AdoptionBlocker struct {
	Code       string `json:"code"`
	ResourceID string `json:"resource_id,omitempty"`
	Message    string `json:"message"`
}

// AdoptionResourceReview preserves observed identity without exposing access secrets.
type AdoptionResourceReview struct {
	InventoryID       string    `json:"inventory_id"`
	ExternalID        string    `json:"external_id"`
	Incarnation       string    `json:"incarnation"`
	Revision          int64     `json:"revision"`
	Ownership         string    `json:"ownership"`
	ObservationState  string    `json:"observation_state"`
	ObservedAt        time.Time `json:"observed_at"`
	PreserveOnFailure bool      `json:"preserve_on_failure"`
}

// AdoptionReview is a read-only snapshot, not a persisted approval or transfer lease.
// Revision is the fleet revision used during review; ID binds the inventory digest.
type AdoptionReview struct {
	ID                      string                   `json:"id"`
	FleetID                 string                   `json:"fleet_id"`
	Revision                int64                    `json:"revision"`
	InventoryState          string                   `json:"inventory_state"`
	InventoryDigest         string                   `json:"inventory_digest"`
	CanApply                bool                     `json:"can_apply"`
	RequiresRecreateCutover bool                     `json:"requires_recreate_cutover"`
	Resources               []AdoptionResourceReview `json:"resources"`
	Blockers                []AdoptionBlocker        `json:"blockers"`
	ReviewedAt              time.Time                `json:"reviewed_at"`
}
