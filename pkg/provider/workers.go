package provider

// WorkerSettings describes a reviewed, prebundled Cloudflare Worker module.
// Build scripts never run in the Loomplane control plane.
type WorkerSettings struct {
	Main               string               `json:"main" yaml:"main"`
	CompatibilityDate  string               `json:"compatibility_date" yaml:"compatibility_date"`
	CompatibilityFlags []string             `json:"compatibility_flags,omitempty" yaml:"compatibility_flags,omitempty"`
	DurableObjects     []DurableObjectClass `json:"durable_objects,omitempty" yaml:"durable_objects,omitempty"`
}

// DurableObjectClass declares an exported class and its native binding name.
type DurableObjectClass struct {
	Name      string `json:"name" yaml:"name"`
	ClassName string `json:"class_name" yaml:"class_name"`
}

// ResourceSetting is a public provisioning option, never a credential value.
type ResourceSetting struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Pattern  string `json:"pattern"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
}
