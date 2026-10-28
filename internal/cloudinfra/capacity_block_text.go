package cloudinfra

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// RolloutOffText is why a deployment waits while automatic expansion is
// off on this server and it needs a server Loomplane won't add. The
// dashboard and loomctl say the same.
const RolloutOffText = "Automatic expansion is off on this Loomplane server, so no server will be added. Add or adopt one yourself."

var blockLabels = map[string]string{
	"servers":       "Servers",
	"cpu_millis":    "CPU",
	"memory_bytes":  "Memory",
	"storage_bytes": "Storage",
	"frontends":     "Frontends",
}

func blockLabel(resource string) string {
	if kind, ok := strings.CutPrefix(resource, "gpus:"); ok {
		if k, found := provider.LookupAcceleratorKind(kind); found {
			kind = k.Title
		}
		return kind + " GPUs"
	}
	if label, ok := blockLabels[resource]; ok {
		return label
	}
	w := strings.ReplaceAll(resource, "_", " ")
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// trimAmount keeps up to digits decimals and drops trailing zeros.
func trimAmount(v float64, digits int) string {
	scale := math.Pow(10, float64(digits))
	return strconv.FormatFloat(math.Round(v*scale)/scale, 'f', -1, 64)
}

// capacityAmount is an amount of a resource in people's units: vCPU for
// millicores, GiB (or MiB under one GiB) for bytes, a count otherwise.
func capacityAmount(resource string, n int64) string {
	switch {
	case resource == "cpu_millis":
		return trimAmount(float64(n)/1000, 2) + " vCPU"
	case strings.HasSuffix(resource, "_bytes"):
		if n != 0 && n > -(1<<30) && n < 1<<30 {
			return trimAmount(float64(n)/(1<<20), 0) + " MiB"
		}
		return trimAmount(float64(n)/(1<<30), 1) + " GiB"
	}
	return strconv.FormatInt(n, 10)
}

// Sentence says in one sentence why capacity wasn't granted. The dashboard's
// blockText and loomctl's blockLine say the same thing.
func (b CapacityBlock) Sentence() string {
	label := blockLabel(b.Resource)
	switch b.Reason {
	case "hard_limit":
		scope := "this Cloud's"
		if b.PoolID != "" {
			scope = "this pool's"
		}
		return fmt.Sprintf("%s is at %s limit: %s of %s counted, %s more needed.", label, scope, capacityAmount(b.Resource, b.Counted), capacityAmount(b.Resource, b.Limit), capacityAmount(b.Resource, b.Requested))
	case "expansion_approval":
		return "Waiting for a Cloud manager to approve a new server."
	case "provisioning":
		if b.Resource == "gpu_runtime" {
			return "Waiting for the new GPU server's NVIDIA runtime."
		}
		return "A new server is being created."
	case "stale_inventory":
		return fmt.Sprintf("Loomplane can't confirm current %s usage, so it won't add capacity until a refresh succeeds.", label)
	case "provider_quota":
		return "The provider account's quota is full."
	case "provider_failure":
		return "The provider couldn't create the server. Loomplane will retry."
	case "placement_constraint":
		return "No allowed server size fits this workload."
	case "review_required":
		if b.Resource == "rollout" {
			return RolloutOffText
		}
		return "This deployment's plan changed. Review it again."
	}
	return strings.ReplaceAll(b.Reason, "_", " ")
}
