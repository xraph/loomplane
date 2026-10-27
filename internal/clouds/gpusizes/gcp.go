package gpusizes

import "github.com/xraph/loomplane/pkg/provider"

// Reference prices: Google Cloud default (on-demand) price for the machine
// type including its GPUs, region us-central1 (Iowa).
var gcpSizes = []provider.ServerSize{
	row("g2-standard-4", "nvidia-l4", 1, 4, 16, 0.706832276),
	row("g2-standard-8", "nvidia-l4", 1, 8, 32, 0.853624312),
	row("g2-standard-24", "nvidia-l4", 2, 24, 96, 2.000832696),
	row("g2-standard-48", "nvidia-l4", 4, 48, 192, 4.001665392),
	row("g2-standard-96", "nvidia-l4", 8, 96, 384, 8.003330784),
	row("a2-highgpu-1g", "nvidia-a100-40gb", 1, 12, 85, 3.673385),
	row("a2-highgpu-8g", "nvidia-a100-40gb", 8, 96, 680, 29.38708),
	row("a2-ultragpu-1g", "nvidia-a100-80gb", 1, 12, 170, 5.06879789),
	row("a2-ultragpu-8g", "nvidia-a100-80gb", 8, 96, 1360, 40.550383123),
	row("a3-highgpu-8g", "nvidia-h100-80gb", 8, 208, 1872, 88.490000119),
}
