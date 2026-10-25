package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	VendorNVIDIA = "nvidia"
	VendorAMD    = "amd"
)

// Accelerator is the GPUs one server size or Kubernetes node carries.
type Accelerator struct {
	Kind      string `json:"kind"`       // canonical kind, such as "nvidia-l4"
	Vendor    string `json:"vendor"`     // VendorNVIDIA or VendorAMD
	Count     int    `json:"count"`      // devices per server or node
	MemoryGiB int    `json:"memory_gib"` // per device
}

// AcceleratorKind is one row of the canonical kind table.
type AcceleratorKind struct {
	ID        string `json:"id"`
	Vendor    string `json:"vendor"`
	Title     string `json:"title"` // "NVIDIA L4"
	MemoryGiB int    `json:"memory_gib"`
}

var acceleratorKinds = []AcceleratorKind{
	{"amd-mi300x", VendorAMD, "AMD Instinct MI300X", 192},
	{"nvidia-a100-40gb", VendorNVIDIA, "NVIDIA A100 40GB", 40},
	{"nvidia-a100-80gb", VendorNVIDIA, "NVIDIA A100 80GB", 80},
	{"nvidia-a10", VendorNVIDIA, "NVIDIA A10", 24},
	{"nvidia-a10g", VendorNVIDIA, "NVIDIA A10G", 24},
	{"nvidia-h100-80gb", VendorNVIDIA, "NVIDIA H100 80GB", 80},
	{"nvidia-h100-nvl-94gb", VendorNVIDIA, "NVIDIA H100 NVL", 94},
	{"nvidia-h200-141gb", VendorNVIDIA, "NVIDIA H200", 141},
	{"nvidia-l40s", VendorNVIDIA, "NVIDIA L40S", 48},
	{"nvidia-l4", VendorNVIDIA, "NVIDIA L4", 24},
	{"nvidia-rtx-4000-ada", VendorNVIDIA, "NVIDIA RTX 4000 Ada", 20},
	{"nvidia-rtx-6000-ada", VendorNVIDIA, "NVIDIA RTX 6000 Ada", 48},
	{"nvidia-t4", VendorNVIDIA, "NVIDIA T4", 16},
	{"nvidia-v100-16gb", VendorNVIDIA, "NVIDIA V100 16GB", 16},
	{"nvidia-v100-32gb", VendorNVIDIA, "NVIDIA V100 32GB", 32},
}

func init() {
	sort.Slice(acceleratorKinds, func(a, b int) bool { return acceleratorKinds[a].ID < acceleratorKinds[b].ID })
}

// AcceleratorKinds returns the canonical kind table sorted by ID.
func AcceleratorKinds() []AcceleratorKind {
	return append([]AcceleratorKind(nil), acceleratorKinds...)
}

func LookupAcceleratorKind(id string) (AcceleratorKind, bool) {
	for _, k := range acceleratorKinds {
		if k.ID == id {
			return k, true
		}
	}
	return AcceleratorKind{}, false
}

// NormalizeAcceleratorKind maps a vendor product string (and the reported
// memory, when known) to a canonical kind ID. It returns "" for an empty product.
func NormalizeAcceleratorKind(product string, memoryMiB int) string {
	tokens := strings.FieldsFunc(strings.ToLower(product), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) == 0 {
		return ""
	}
	squashed := strings.Join(tokens, "")
	has := func(s string) bool { return strings.Contains(squashed, s) }
	hasToken := func(s string) bool {
		for _, t := range tokens {
			if t == s {
				return true
			}
		}
		return false
	}
	gib := memoryMiB / 1024
	switch {
	case has("mi300x"):
		return "amd-mi300x"
	case has("h200"):
		return "nvidia-h200-141gb"
	case has("h100"):
		if has("nvl") || gib >= 90 {
			return "nvidia-h100-nvl-94gb"
		}
		return "nvidia-h100-80gb"
	case has("a100"):
		if has("80gb") || gib >= 60 {
			return "nvidia-a100-80gb"
		}
		return "nvidia-a100-40gb"
	case has("l40s"):
		return "nvidia-l40s"
	case has("6000ada"):
		return "nvidia-rtx-6000-ada"
	case has("4000ada"):
		return "nvidia-rtx-4000-ada"
	case has("a10g"):
		return "nvidia-a10g"
	case hasToken("a10"):
		return "nvidia-a10"
	case hasToken("l4"):
		return "nvidia-l4"
	case hasToken("t4"):
		return "nvidia-t4"
	case has("v100"):
		if gib >= 30 || has("32gb") {
			return "nvidia-v100-32gb"
		}
		return "nvidia-v100-16gb"
	}
	vendor := VendorNVIDIA
	for _, t := range tokens {
		if t == "amd" || t == "instinct" {
			vendor = VendorAMD
		}
	}
	rest := []string{vendor}
	for _, t := range tokens {
		if t == "nvidia" || t == "tesla" || t == "amd" {
			continue
		}
		rest = append(rest, t)
	}
	return strings.Join(rest, "-")
}

// GPURequest asks for Count devices of Kind on one host or node.
type GPURequest struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type GPUToleration struct {
	Key      string `json:"key"`
	Operator string `json:"operator"` // "Equal" or "Exists"
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect"`
}

// GPUPlacement is a reviewed request plus how a cluster schedules it.
// Servers leave NodeSelector and Tolerations empty.
type GPUPlacement struct {
	GPURequest
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	Tolerations  []GPUToleration   `json:"tolerations,omitempty"`
}

type GPUDevice struct {
	Index     int    `json:"index"`
	UUID      string `json:"uuid"`
	Kind      string `json:"kind"`
	Product   string `json:"product"`
	MemoryMiB int    `json:"memory_mib"`
}

type GPUNodeGroup struct {
	Kind             string            `json:"kind"` // "" when no label identifies the GPU
	Product          string            `json:"product,omitempty"`
	MemoryGiB        int               `json:"memory_gib"`
	Nodes            int               `json:"nodes"`
	PerNode          int               `json:"per_node"` // largest allocatable nvidia.com/gpu on one node
	Total            int               `json:"total"`
	InstanceType     string            `json:"instance_type,omitempty"`
	NodeSelector     map[string]string `json:"node_selector,omitempty"`
	Tolerations      []GPUToleration   `json:"tolerations,omitempty"`
	HourlyUSD        float64           `json:"hourly_usd"` // per node, list price estimate
	HourlyPriceKnown bool              `json:"hourly_price_known"`
}

const (
	GPUStatusReady          = "ready"
	GPUStatusNone           = "none"
	GPUStatusDriverMissing  = "driver_missing"
	GPUStatusToolkitMissing = "toolkit_missing"
	GPUStatusNoDevicePlugin = "no_device_plugin"
	GPUStatusUnlisted       = "unlisted"
	GPUStatusUnknown        = "unknown"
)

// GPUInventory is what a server or cluster connection reports.
type GPUInventory struct {
	Status    string         `json:"status"`
	Message   string         `json:"message,omitempty"`
	Devices   []GPUDevice    `json:"devices,omitempty"` // servers
	Nodes     []GPUNodeGroup `json:"nodes,omitempty"`   // clusters
	CheckedAt time.Time      `json:"checked_at"`
}

const (
	GPUDetailStatus    = "gpu"  // connection Details key: the status
	GPUDetailInventory = "gpus" // connection Details key: the inventory as JSON
)

// Details encodes the inventory for a connection's Details map.
func (i GPUInventory) Details() map[string]string {
	d := map[string]string{GPUDetailStatus: i.Status}
	if raw, err := json.Marshal(i); err == nil {
		d[GPUDetailInventory] = string(raw)
	}
	return d
}

// ParseGPUInventory returns false when the inventory is absent or doesn't decode.
func ParseGPUInventory(details map[string]string) (GPUInventory, bool) {
	raw, ok := details[GPUDetailInventory]
	if !ok {
		return GPUInventory{}, false
	}
	var inv GPUInventory
	if err := json.Unmarshal([]byte(raw), &inv); err != nil {
		return GPUInventory{}, false
	}
	return inv, true
}

const (
	GPUUnavailable       = "gpu-unavailable"
	GPUKindUnavailable   = "gpu-kind-unavailable"
	GPUCountUnavailable  = "gpu-count-unavailable"
	GPUVendorUnsupported = "gpu-vendor-unsupported"
)

type GPUUnavailableError struct{ Code, Message string }

func (e *GPUUnavailableError) Error() string { return e.Message }

var ErrGPURequestInvalid = errors.New("a GPU request needs a known NVIDIA kind and 1 to 8 devices")

// Place reports where req fits on one host or node, or why it doesn't. It
// never says yes on a guess: unknown inventory, an unlisted cluster or a
// missing toolkit all mean it doesn't fit.
func (i GPUInventory) Place(req GPURequest) (*GPUPlacement, error) {
	if req.Kind == "" || req.Count < 1 || req.Count > 8 {
		return nil, ErrGPURequestInvalid
	}
	if i.Status != GPUStatusReady {
		msg := i.Message
		if msg == "" {
			msg = "Loomplane couldn't read this target's GPUs. Check the connection again."
		}
		return nil, &GPUUnavailableError{Code: GPUUnavailable, Message: msg}
	}
	title := req.Kind
	vendor := ""
	if k, ok := LookupAcceleratorKind(req.Kind); ok {
		title, vendor = k.Title, k.Vendor
	} else if strings.HasPrefix(req.Kind, VendorAMD+"-") {
		vendor = VendorAMD
	}
	if vendor == VendorAMD {
		return nil, &GPUUnavailableError{Code: GPUVendorUnsupported, Message: "AMD GPUs aren't supported for workloads yet."}
	}
	noKind := &GPUUnavailableError{Code: GPUKindUnavailable, Message: fmt.Sprintf("This target has no %s GPUs.", title)}
	if len(i.Nodes) == 0 {
		have := 0
		for _, d := range i.Devices {
			if d.Kind == req.Kind {
				have++
			}
		}
		if have == 0 {
			return nil, noKind
		}
		if have < req.Count {
			return nil, countShort(req.Count, title, have)
		}
		return &GPUPlacement{GPURequest: req}, nil
	}
	var best *GPUNodeGroup
	for n := range i.Nodes {
		g := &i.Nodes[n]
		if g.Kind != req.Kind || len(g.NodeSelector) == 0 {
			continue
		}
		if best == nil || g.PerNode > best.PerNode {
			best = g
		}
	}
	if best == nil {
		return nil, noKind
	}
	if best.PerNode < req.Count {
		return nil, countShort(req.Count, title, best.PerNode)
	}
	p := &GPUPlacement{GPURequest: req, NodeSelector: make(map[string]string, len(best.NodeSelector))}
	for k, v := range best.NodeSelector {
		p.NodeSelector[k] = v
	}
	if len(best.Tolerations) > 0 {
		p.Tolerations = append([]GPUToleration(nil), best.Tolerations...)
	}
	return p, nil
}

func countShort(want int, title string, most int) error {
	return &GPUUnavailableError{Code: GPUCountUnavailable, Message: fmt.Sprintf("No single server or node here has %d %s GPUs; the most on one is %d.", want, title, most)}
}
