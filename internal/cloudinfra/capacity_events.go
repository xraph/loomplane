package cloudinfra

// CapacityChange tells a Cloud manager that a capacity policy or an
// expansion moved. It names what changed and carries no policy body,
// approver or authority: fetch the policy, usage or expansions for those.
type CapacityChange struct {
	OrgID   string `json:"org_id"`
	CloudID string `json:"cloud_id"`
	// Kind is "policy" or "expansion".
	Kind string `json:"kind"`
	// EntityID is the Cloud ID for a policy and the expansion ID otherwise.
	EntityID string `json:"entity_id"`
	Revision int64  `json:"revision"`
	// Status is the expansion's status. A policy has none.
	Status string `json:"status,omitempty"`
}
