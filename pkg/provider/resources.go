package provider

// ResourceObservation contains provider metadata only. Access credentials never belong here.
type ResourceObservation struct {
	Scope       string `json:"scope,omitempty"`
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Version     string `json:"version"`
	Region      string `json:"region"`
	Status      string `json:"status"`
	Host        string `json:"host"`
	Database    string `json:"database"`
	Port        int    `json:"port"`
}

// ResourceInventory is authoritative only after every page in Scope succeeds.
type ResourceInventory struct {
	Scope     string                `json:"scope"`
	Resources []ResourceObservation `json:"resources"`
	Complete  bool                  `json:"complete"`
}
