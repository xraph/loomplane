package provider

import "context"

// ServerOptionsAdapter exposes account inventory for provider-neutral reviews.
// Prices are estimates from the provider catalog, not a billing guarantee.
type ServerOptionsAdapter interface {
	ServerOptions(context.Context, InfrastructureCredential) (ServerOptions, error)
}
type ServerOptions struct {
	Regions []ServerRegion `json:"regions"`
	Sizes   []ServerSize   `json:"sizes"`
	Images  []ServerImage  `json:"images"`
}
type ServerRegion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type ServerSize struct {
	Architectures    []string     `json:"architectures,omitempty"`
	HourlyPriceKnown bool         `json:"hourly_price_known,omitempty"`
	ID               string       `json:"id"`
	CPUs             int          `json:"cpus"`
	MemoryMiB        int          `json:"memory_mib"`
	DiskGB           int          `json:"disk_gb"`
	HourlyUSD        float64      `json:"hourly_usd"`
	MonthlyUSD       float64      `json:"monthly_usd"`
	Regions          []string     `json:"regions"`
	Accelerator      *Accelerator `json:"accelerator,omitempty"`
}
type ServerImage struct {
	MinDiskGB    int      `json:"min_disk_gb,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Regions      []string `json:"regions"`
	Bootstrap    bool     `json:"bootstrap"`
	Accelerators []string `json:"accelerators,omitempty"` // vendors whose drivers the image ships
}
