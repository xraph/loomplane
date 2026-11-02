package gpusizes

import "github.com/xraph/loomplane/pkg/provider"

// Reference prices: DigitalOcean on-demand list price per Droplet. These rows
// are a fallback for DOKS node labels. Live Droplet sizes come from the API.
// The 8-GPU price is eight times the listed per-GPU price.
var digitalOceanSizes = []provider.ServerSize{
	row("gpu-h100x1-80gb", "nvidia-h100-80gb", 1, 20, 240, 4.41),
	row("gpu-h100x8-640gb", "nvidia-h100-80gb", 8, 160, 1920, 35.28),
	row("gpu-l40sx1-48gb", "nvidia-l40s", 1, 8, 64, 1.57),
	row("gpu-4000adax1-20gb", "nvidia-rtx-4000-ada", 1, 8, 32, 0.76),
	row("gpu-6000adax1-48gb", "nvidia-rtx-6000-ada", 1, 8, 64, 1.57),
	row("gpu-mi300x1-192gb", "amd-mi300x", 1, 20, 240, 2.59),
}
