package kubernetes

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/gpusizes"
	"github.com/xraph/loomplane/pkg/provider"
)

const (
	gpuResource       = "nvidia.com/gpu"
	gpuMemoryLabel    = "nvidia.com/gpu.memory"
	instanceTypeLabel = "node.kubernetes.io/instance-type"
	nodeListPageSize  = 500
	nodeListMaxPages  = 10
)

// gpuProductLabels are checked in order. The first one a node carries names
// its GPU and becomes the node selector.
var gpuProductLabels = []string{
	"cloud.google.com/gke-accelerator",
	"nvidia.com/gpu.product",
	"k8s.amazonaws.com/accelerator",
	"doks.digitalocean.com/gpu-model",
}

// GPUs lists the cluster's GPU nodes when the credential may list nodes.
func (c *Client) GPUs(ctx context.Context) (provider.GPUInventory, error) {
	now := time.Now().UTC()
	allowed, err := c.allowedIn(ctx, "", "", "nodes", "list")
	if err != nil {
		return provider.GPUInventory{}, err
	}
	if !allowed {
		return provider.GPUInventory{
			Status:    provider.GPUStatusUnlisted,
			Message:   "Loomplane can't list this cluster's nodes, so it can't see GPUs. Grant this connection list on nodes through a ClusterRole, then check it again.",
			CheckedAt: now,
		}, nil
	}
	var nodes []corev1.Node
	opts := metav1.ListOptions{Limit: nodeListPageSize}
	for page := 0; page < nodeListMaxPages; page++ {
		list, listErr := c.Kube.CoreV1().Nodes().List(ctx, opts)
		if listErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return provider.GPUInventory{}, ctxErr
			}
			return provider.GPUInventory{
				Status:    provider.GPUStatusUnknown,
				Message:   "Loomplane couldn't read this cluster's nodes. Check the connection again.",
				CheckedAt: now,
			}, nil
		}
		nodes = append(nodes, list.Items...)
		opts.Continue = list.Continue
		if opts.Continue == "" {
			break
		}
	}
	if opts.Continue != "" {
		return provider.GPUInventory{
			Status:    provider.GPUStatusUnknown,
			Message:   "This cluster has more nodes than Loomplane reads in one check, so it can't tell which have GPUs.",
			CheckedAt: now,
		}, nil
	}
	inv := gpuInventory(nodes)
	inv.CheckedAt = now
	return inv, nil
}

type gpuGroupKey struct{ kind, selKey, selValue string }

type gpuGroupAcc struct {
	group     provider.GPUNodeGroup
	instances map[string]int
	tolers    map[provider.GPUToleration]bool
	memoryMiB int
}

// gpuTolerations are the tolerations a node's GPU taints need. Kubernetes
// and cloud controllers set node.kubernetes.io/ and
// node.cloudprovider.kubernetes.io/ taints for state (cordoned, not ready,
// unreachable, uninitialized), so tolerating them would land GPU pods on a
// cordoned node and keep them on a dead one. They are left out.
func gpuTolerations(taints []corev1.Taint) map[provider.GPUToleration]bool {
	out := map[provider.GPUToleration]bool{}
	for _, taint := range taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if strings.HasPrefix(taint.Key, "node.kubernetes.io/") || strings.HasPrefix(taint.Key, "node.cloudprovider.kubernetes.io/") {
			continue
		}
		t := provider.GPUToleration{Key: taint.Key, Operator: "Equal", Value: taint.Value, Effect: string(taint.Effect)}
		if taint.Value == "" {
			t.Operator = "Exists"
		}
		out[t] = true
	}
	return out
}

// gpuInventory groups nodes by accelerator kind. A group tolerates only
// the taints every one of its nodes carries, so a taint on one node never
// spreads to the rest. Pure, for tests.
func gpuInventory(nodes []corev1.Node) provider.GPUInventory {
	groups := map[gpuGroupKey]*gpuGroupAcc{}
	var order []gpuGroupKey
	gpuNodes, allocNodes := 0, 0
	for i := range nodes {
		node := &nodes[i]
		alloc := int64(0)
		if q, ok := node.Status.Allocatable[gpuResource]; ok && q.Value() > 0 {
			alloc = q.Value()
		}
		memoryMiB := 0
		if n, convErr := strconv.Atoi(node.Labels[gpuMemoryLabel]); convErr == nil {
			memoryMiB = n
		}
		kind, selKey, selValue, product := identifyGPU(node.Labels, memoryMiB)
		if alloc == 0 && selKey == "" {
			continue
		}
		gpuNodes++
		if alloc > 0 {
			allocNodes++
		}
		key := gpuGroupKey{kind, selKey, selValue}
		acc := groups[key]
		if acc == nil {
			acc = &gpuGroupAcc{instances: map[string]int{}}
			acc.group.Kind = kind
			acc.group.Product = product
			if selKey != "" {
				acc.group.NodeSelector = map[string]string{selKey: selValue}
			}
			groups[key] = acc
			order = append(order, key)
		}
		nodeTolers := gpuTolerations(node.Spec.Taints)
		if acc.group.Nodes == 0 {
			acc.tolers = nodeTolers
		} else {
			for t := range acc.tolers {
				if !nodeTolers[t] {
					delete(acc.tolers, t)
				}
			}
		}
		acc.group.Nodes++
		acc.group.Total += int(alloc)
		acc.group.PerNode = max(acc.group.PerNode, int(alloc))
		acc.memoryMiB = max(acc.memoryMiB, memoryMiB)
		if it := node.Labels[instanceTypeLabel]; it != "" {
			acc.instances[it]++
		}
	}
	inv := provider.GPUInventory{Status: provider.GPUStatusNone}
	ready := false
	for _, key := range order {
		acc := groups[key]
		g := acc.group
		if k, ok := provider.LookupAcceleratorKind(g.Kind); ok {
			g.MemoryGiB = k.MemoryGiB
		} else {
			g.MemoryGiB = acc.memoryMiB / 1024
		}
		g.InstanceType = commonInstanceType(acc.instances)
		if size, _, ok := gpusizes.Lookup(g.InstanceType); ok {
			g.HourlyUSD, g.HourlyPriceKnown = size.HourlyUSD, size.HourlyPriceKnown
		}
		acc.tolers[provider.GPUToleration{Key: gpuResource, Operator: "Exists", Effect: "NoSchedule"}] = true
		g.Tolerations = sortedTolerations(acc.tolers)
		if g.PerNode > 0 && len(g.NodeSelector) > 0 {
			ready = true
		}
		inv.Nodes = append(inv.Nodes, g)
	}
	switch {
	case ready:
		inv.Status = provider.GPUStatusReady
	case allocNodes > 0:
		inv.Status = provider.GPUStatusUnknown
		inv.Message = "This cluster has GPUs, but no node label says which kind. Add the nvidia.com/gpu.product label (GPU feature discovery adds it) so Loomplane can place work on them."
	case gpuNodes > 0:
		inv.Status = provider.GPUStatusNoDevicePlugin
		inv.Message = "These nodes have NVIDIA GPUs but nothing advertises nvidia.com/gpu. Install the NVIDIA device plugin or GPU Operator, then check the connection again."
	}
	return inv
}

// identifyGPU reads the first identifying label a node carries.
func identifyGPU(labels map[string]string, memoryMiB int) (kind, selKey, selValue, product string) {
	for _, key := range gpuProductLabels {
		if v := labels[key]; v != "" {
			return provider.NormalizeAcceleratorKind(v, memoryMiB), key, v, v
		}
	}
	if v := labels[instanceTypeLabel]; v != "" {
		if size, _, ok := gpusizes.Lookup(v); ok && size.Accelerator != nil {
			return size.Accelerator.Kind, instanceTypeLabel, v, ""
		}
	}
	return "", "", "", ""
}

func commonInstanceType(counts map[string]int) string {
	best, bestN := "", 0
	for it, n := range counts {
		if n > bestN || n == bestN && it < best {
			best, bestN = it, n
		}
	}
	return best
}

func sortedTolerations(set map[provider.GPUToleration]bool) []provider.GPUToleration {
	out := make([]provider.GPUToleration, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		kx := fmt.Sprint(x.Key, "\x00", x.Operator, "\x00", x.Value, "\x00", x.Effect)
		ky := fmt.Sprint(y.Key, "\x00", y.Operator, "\x00", y.Value, "\x00", y.Effect)
		return kx < ky
	})
	return out
}
