package provider

// StoredVolume is safe inventory metadata, without host paths or credentials.
// Capacity is zero when the provider does not report a volume capacity.
type StoredVolume struct {
	ID            string `json:"id" forge:"id"`
	Name          string `json:"name"`
	ConnectionID  string `json:"connection_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	InstanceID    string `json:"instance_id"`
	Role          string `json:"role"`
	Provider      string `json:"provider"`
	Status        string `json:"status"`
	StorageClass  string `json:"storage_class,omitempty"`
	CapacityBytes int64  `json:"capacity_bytes"`
}
