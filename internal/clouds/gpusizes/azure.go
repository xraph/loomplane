package gpusizes

import "github.com/xraph/loomplane/pkg/provider"

// Reference prices: Azure Linux VM pay-as-you-go, region eastus.
var azureSizes = []provider.ServerSize{
	row("Standard_NC4as_T4_v3", "nvidia-t4", 1, 4, 28, 0.526),
	row("Standard_NC64as_T4_v3", "nvidia-t4", 4, 64, 440, 4.352),
	row("Standard_NV36ads_A10_v5", "nvidia-a10", 1, 36, 440, 3.2),
	row("Standard_NC24ads_A100_v4", "nvidia-a100-80gb", 1, 24, 220, 3.673),
	row("Standard_NC96ads_A100_v4", "nvidia-a100-80gb", 4, 96, 880, 14.692),
	row("Standard_ND96asr_v4", "nvidia-a100-40gb", 8, 96, 900, 27.197),
	row("Standard_ND96amsr_A100_v4", "nvidia-a100-80gb", 8, 96, 1900, 32.77),
	row("Standard_NC40ads_H100_v5", "nvidia-h100-nvl-94gb", 1, 40, 320, 6.98),
	row("Standard_ND96isr_H100_v5", "nvidia-h100-80gb", 8, 96, 1900, 98.32),
}
