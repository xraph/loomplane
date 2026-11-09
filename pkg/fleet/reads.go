package fleet

// ListInstancesInput selects a bounded page in immutable instance ID order.
type ListInstancesInput struct {
	After       string `json:"after,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Status      string `json:"status,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`
}

// InstancePage includes a continuation only when another matching row exists.
type InstancePage struct {
	Items      []Instance `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}
