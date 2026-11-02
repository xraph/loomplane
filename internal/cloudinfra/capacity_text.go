package cloudinfra

import (
	"fmt"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// CapacityResourceName names a capacity block's resource for a sentence:
// "NVIDIA L40S GPUs" for "gpus:nvidia-l40s", "servers" for "servers".
// What a person can't act on, like the request or the inventory, reads as
// "servers and their resources".
func CapacityResourceName(resource string) string {
	if kind, ok := strings.CutPrefix(resource, "gpus:"); ok {
		if k, found := provider.LookupAcceleratorKind(kind); found {
			return k.Title + " GPUs"
		}
		return kind + " GPUs"
	}
	switch resource {
	case "servers", "minimum_servers":
		return "servers"
	case "cpu_millis":
		return "CPU millicores"
	case "memory_bytes":
		return "memory bytes"
	case "storage_bytes":
		return "storage bytes"
	case "frontends":
		return "frontends"
	case "gpu_runtime":
		return "GPU runtimes"
	case "pool":
		return "the pool's servers"
	}
	return "servers and their resources"
}

// Summary says what a capacity block means in a phrase, with readable
// names and no codes: "it would pass the limit of 2 NVIDIA L40S GPUs".
func (b CapacityBlock) Summary() string {
	what := CapacityResourceName(b.Resource)
	switch b.Reason {
	case "hard_limit":
		if b.Limit <= 0 {
			return "it has no ceiling for " + what + ", so it allows none"
		}
		return fmt.Sprintf("it would pass the limit of %d %s (%d counted, %d more needed)", b.Limit, what, b.Counted, b.Requested)
	case "provider_quota":
		return fmt.Sprintf("the provider account's quota of %d %s is full (%d counted, %d more needed)", b.Limit, what, b.Counted, b.Requested)
	case "stale_inventory":
		return "its count of " + what + " is out of date"
	case "expansion_approval", "review_required":
		return "it waits for an admin to approve it"
	case "provisioning":
		return "it waits for capacity to grow"
	case "placement_constraint":
		return "no pool can place it"
	}
	return "a limit on " + what + " applies"
}
