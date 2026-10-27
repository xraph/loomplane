package gpusizes

import "github.com/xraph/loomplane/pkg/provider"

// Reference prices: AWS on-demand Linux, shared tenancy, region us-east-1.
// p5e.48xlarge is left out because AWS publishes no on-demand price for it.
var awsSizes = []provider.ServerSize{
	row("g4dn.xlarge", "nvidia-t4", 1, 4, 16, 0.526),
	row("g4dn.12xlarge", "nvidia-t4", 4, 48, 192, 3.912),
	row("g5.xlarge", "nvidia-a10g", 1, 4, 16, 1.006),
	row("g5.2xlarge", "nvidia-a10g", 1, 8, 32, 1.212),
	row("g5.12xlarge", "nvidia-a10g", 4, 48, 192, 5.672),
	row("g5.48xlarge", "nvidia-a10g", 8, 192, 768, 16.288),
	row("g6.xlarge", "nvidia-l4", 1, 4, 16, 0.8048),
	row("g6.12xlarge", "nvidia-l4", 4, 48, 192, 4.6016),
	row("g6.48xlarge", "nvidia-l4", 8, 192, 768, 13.3504),
	row("g6e.xlarge", "nvidia-l40s", 1, 4, 32, 1.861),
	row("g6e.12xlarge", "nvidia-l40s", 4, 48, 384, 10.49264),
	row("g6e.48xlarge", "nvidia-l40s", 8, 192, 1536, 30.13118),
	row("p4d.24xlarge", "nvidia-a100-40gb", 8, 96, 1152, 21.957642),
	row("p4de.24xlarge", "nvidia-a100-80gb", 8, 96, 1152, 27.44705),
	row("p5.48xlarge", "nvidia-h100-80gb", 8, 192, 2048, 55.04),
}
