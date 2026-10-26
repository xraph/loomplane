package cloudinfra

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func gpuCount(kind string, gpus *int64) string {
	if gpus == nil {
		return kind + ":unknown"
	}
	b, _ := json.Marshal(*gpus)
	return kind + ":" + string(b)
}

func TestServerGPUFacts(t *testing.T) {
	l4 := provider.GPUDevice{Kind: "nvidia-l4"}
	created := &Operation{Input: ServerInput{Kind: "create", Size: "gpu-l4"}, CapacityTarget: &ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 1 << 30, GPUKind: "nvidia-l4", GPUs: 1}}
	cases := []struct {
		name    string
		server  Server
		created *Operation
		want    string
	}{
		{"devices from health", Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusReady, GPUs: []provider.GPUDevice{l4, l4}}}, nil, "nvidia-l4:2"},
		{"devices without a toolkit still count", Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusToolkitMissing, GPUs: []provider.GPUDevice{l4}}}, nil, "nvidia-l4:1"},
		{"mixed kinds are unknown", Server{Health: RuntimeHealth{GPUs: []provider.GPUDevice{l4, {Kind: "nvidia-t4"}}}}, nil, ":unknown"},
		{"an unnamed device is unknown", Server{Health: RuntimeHealth{GPUs: []provider.GPUDevice{{}}}}, nil, ":unknown"},
		{"no GPU found", Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusNone}}, created, ":0"},
		{"before enrolment the create target counts", Server{Size: "gpu-l4"}, created, "nvidia-l4:1"},
		{"a resized server doesn't trust its create target", Server{Size: "other", Health: RuntimeHealth{GPURuntime: provider.GPUStatusUnknown}}, created, ":unknown"},
		{"an adoption target says nothing about GPUs", Server{Size: "gpu-l4"}, &Operation{Input: ServerInput{Kind: "adopt", Size: "gpu-l4"}, CapacityTarget: &ResourceVector{Servers: 1}}, ":unknown"},
		{"a CPU create target counts as none", Server{Size: "small"}, &Operation{Input: ServerInput{Kind: "create", Size: "small"}, CapacityTarget: &ResourceVector{Servers: 1, CPUMillis: 1000}}, ":0"},
		{"a reference GPU size counts", Server{Size: "gpu-h100x1-80gb", Health: RuntimeHealth{GPURuntime: provider.GPUStatusDriverMissing}}, nil, "nvidia-h100-80gb:1"},
		{"an unread host is unknown", Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusUnknown}}, nil, ":unknown"},
		{"a host checked before GPU probing is unknown", Server{}, nil, ":unknown"},
	}
	for _, tc := range cases {
		if got := gpuCount(serverGPUFacts(&tc.server, tc.created)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestInventoryGPUCounts(t *testing.T) {
	two, one, zero := number(2), number(1), number(0)
	records := []CapacityRecord{
		{Kind: "server", ServerID: "a", GPUKind: "nvidia-l4", GPUs: two},
		{Kind: "server", ServerID: "a", GPUKind: "nvidia-l4", GPUs: two}, // a provider alias of the same host
		{Kind: "server", ServerID: "b", GPUKind: "nvidia-h100-80gb", GPUs: one},
		{Kind: "server", ServerID: "c", GPUs: zero},
		{Kind: "volume", ResourceID: "volume:v"}, // volumes hold no GPUs
	}
	reservations := []scopedCapacityReservation{
		{cloud: "cloud", reservation: infrastructureReservation{Delta: ResourceVector{Servers: 1, GPUKind: "nvidia-l4", GPUs: 1}}},
		{cloud: "other", reservation: infrastructureReservation{Delta: ResourceVector{Servers: 1, GPUKind: "nvidia-l4", GPUs: 8}}},
		{cloud: "cloud", reservation: infrastructureReservation{Delta: ResourceVector{Servers: 1}}},
	}
	got, err := cloudGPUs(records, reservations, "cloud", "nvidia-l4")
	if err != nil || got == nil || *got != 3 {
		t.Fatalf("L4s: %v %v", got, err)
	}
	if got, err = cloudGPUs(records, reservations, "cloud", "nvidia-a100-80gb"); err != nil || got == nil || *got != 0 {
		t.Fatalf("a kind the Cloud doesn't have: %v %v", got, err)
	}
	// One unread server makes every kind unknown.
	unknown := append(append([]CapacityRecord{}, records...), CapacityRecord{Kind: "server", ServerID: "d"})
	if got, err = cloudGPUs(unknown, reservations, "cloud", "nvidia-l4"); err != nil || got != nil {
		t.Fatalf("unknown server read as a count: %v %v", got, err)
	}
	bad := []CapacityRecord{{Kind: "server", ServerID: "e", GPUKind: "nvidia-l4"}}
	if _, err = cloudGPUs(bad, nil, "cloud", "nvidia-l4"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kind without a count: %v", err)
	}
}

func TestSummarizeCapacityGPUs(t *testing.T) {
	now := time.Now()
	policy := CapacityPolicy{Revision: 1, GPULimits: []GPULimit{{Kind: "nvidia-l4", Maximum: 4}, {Kind: "nvidia-h100-80gb", Maximum: 1}}}
	l4 := CapacityRecord{Kind: "server", ServerID: "a", GPUKind: "nvidia-l4", GPUs: number(2), CheckedAt: now, Resources: capacityResources(ResourceVector{Servers: 1})}
	usage, err := summarizeCapacity([]CapacityRecord{l4}, nil, policy, now)
	if err != nil || len(usage.GPUs) != 2 {
		t.Fatalf("GPU usage: %+v %v", usage, err)
	}
	h, l := usage.GPUs[0], usage.GPUs[1]
	if h.Kind != "nvidia-h100-80gb" || h.Provisioned == nil || *h.Provisioned != 0 || *h.Limit != 1 || l.Kind != "nvidia-l4" || *l.Provisioned != 2 || *l.Limit != 4 {
		t.Fatalf("rows: %+v %+v", h, l)
	}
	unread := CapacityRecord{Kind: "server", ServerID: "b", CheckedAt: now, Resources: capacityResources(ResourceVector{Servers: 1})}
	if usage, err = summarizeCapacity([]CapacityRecord{l4, unread}, nil, policy, now); err != nil || usage.GPUs[0].Provisioned != nil || usage.GPUs[1].Provisioned != nil {
		t.Fatalf("an unread server left a count: %+v %v", usage.GPUs, err)
	}
	// A CPU-only Cloud reports no GPU rows, as before.
	cpu := []CapacityRecord{{Kind: "server", ServerID: "c", GPUs: number(0), CheckedAt: now, Resources: capacityResources(ResourceVector{Servers: 1})}}
	if usage, err = summarizeCapacity(cpu, nil, CapacityPolicy{}, now); err != nil || usage.GPUs != nil {
		t.Fatalf("CPU Cloud GPU rows: %+v %v", usage.GPUs, err)
	}
}

func TestCapacityRecordCPUJSON(t *testing.T) {
	raw, err := json.Marshal(CapacityRecord{Kind: "volume"})
	if err != nil || strings.Contains(string(raw), "gpu") {
		t.Fatalf("volume record: %s %v", raw, err)
	}
	var old CapacityRecord
	if err = json.Unmarshal([]byte(`{"kind":"server","server_id":"a"}`), &old); err != nil || old.GPUs != nil {
		t.Fatalf("a record from before GPUs must read as unknown: %+v %v", old, err)
	}
}

func TestGPUWorkFitsPool(t *testing.T) {
	cases := []struct {
		pool, kind string
		gpus       int64
		want       error
		text       string
	}{
		{"", "", 0, nil, ""},
		{"nvidia-l4", "nvidia-l4", 1, nil, ""},
		{"nvidia-l4", "", 0, ErrInvalid, "a GPU pool takes only GPU workloads"},
		{"", "nvidia-l4", 1, ErrInvalid, "a CPU pool takes no GPU workloads"},
		{"nvidia-l4", "nvidia-t4", 1, ErrConflict, "nvidia-l4 GPUs; the workload asks for nvidia-t4"},
	}
	for _, tc := range cases {
		err := gpuWorkFitsPool(tc.pool, tc.kind, tc.gpus)
		if tc.want == nil && err != nil || tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text)) {
			t.Errorf("%q in %q pool: %v", tc.kind, tc.pool, err)
		}
	}
}

func TestCheckPoolGPULimit(t *testing.T) {
	p := &CapacityPolicy{Revision: 3}
	d := CapacityDemand{CloudID: "cloud", PoolID: "pool"}
	capped := PoolExpansionPolicy{PoolID: "pool", AcceleratorKind: "nvidia-h100-80gb", Limits: &ResourceVector{Servers: 4, GPUKind: "nvidia-h100-80gb", GPUs: 2}}
	if b := checkPoolGPULimit(p, d, capped, "nvidia-h100-80gb", number(1), 1); b != nil {
		t.Fatalf("second of two: %+v", b)
	}
	b := checkPoolGPULimit(p, d, capped, "nvidia-h100-80gb", number(2), 1)
	if b == nil || b.Reason != "hard_limit" || b.Resource != "gpus:nvidia-h100-80gb" || b.PoolID != "pool" || b.Limit != 2 || b.Counted != 2 || b.Requested != 1 || b.PolicyRevision != 3 || b.Retryable {
		t.Fatalf("third of two: %+v", b)
	}
	if b = checkPoolGPULimit(p, d, capped, "nvidia-h100-80gb", number(3), 0); b != nil {
		t.Fatalf("keeping members after a lowered ceiling: %+v", b)
	}
	if b = checkPoolGPULimit(p, d, capped, "nvidia-h100-80gb", nil, 1); b == nil || b.Reason != "stale_inventory" || b.Resource != "gpus:nvidia-h100-80gb" || !b.Retryable {
		t.Fatalf("unknown count: %+v", b)
	}
	uncapped := capped
	uncapped.Limits = &ResourceVector{Servers: 4}
	if b = checkPoolGPULimit(p, d, uncapped, "nvidia-h100-80gb", nil, 8); b != nil {
		t.Fatalf("a pool without a GPU ceiling leaves it to the Cloud: %+v", b)
	}
	if b = checkPoolGPULimit(p, d, PoolExpansionPolicy{PoolID: "pool"}, "nvidia-h100-80gb", nil, 1); b != nil {
		t.Fatalf("no pool limits: %+v", b)
	}
	if b = checkPoolGPULimit(p, d, capped, "nvidia-l4", number(9), 1); b != nil {
		t.Fatalf("a ceiling covers only its own kind: %+v", b)
	}
}

func TestPoolInventoryGPUs(t *testing.T) {
	records := []CapacityRecord{
		{Kind: "server", ServerID: "a", GPUKind: "nvidia-h100-80gb", GPUs: number(1)},
		{Kind: "server", ServerID: "b", GPUKind: "nvidia-h100-80gb", GPUs: number(2)},
		{Kind: "server", ServerID: "c", GPUKind: "nvidia-l4", GPUs: number(4)},
		{Kind: "server", ServerID: "unread"},
	}
	if got := poolInventoryGPUs(records, map[string]bool{"a": true, "b": true, "c": true}, "nvidia-h100-80gb"); got == nil || *got != 3 {
		t.Fatalf("pool H100s: %v", got)
	}
	if got := poolInventoryGPUs(records, map[string]bool{}, "nvidia-h100-80gb"); got == nil || *got != 0 {
		t.Fatalf("empty pool: %v", got)
	}
	if got := poolInventoryGPUs(records, map[string]bool{"a": true, "unread": true}, "nvidia-h100-80gb"); got != nil {
		t.Fatalf("an unread member read as a count: %v", *got)
	}
	if got := poolInventoryGPUs(records, map[string]bool{"a": true, "missing": true}, "nvidia-h100-80gb"); got != nil {
		t.Fatalf("a member without inventory read as a count: %v", *got)
	}
	if got, err := addGPUCount(nil, 1); got != nil || err != nil {
		t.Fatalf("unknown plus one: %v %v", got, err)
	}
	if got, err := addGPUCount(number(2), 1); err != nil || *got != 3 {
		t.Fatalf("two plus one: %v %v", got, err)
	}
	if _, err := addGPUCount(number(math.MaxInt64), 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow: %v", err)
	}
}
