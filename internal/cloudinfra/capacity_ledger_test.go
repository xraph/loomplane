package cloudinfra

import (
	"math"
	"testing"
)

func TestCapacityLedgerLimitArithmetic(t *testing.T) {
	p := &CapacityPolicy{Revision: 7}
	d := CapacityDemand{CloudID: "cloud"}
	for _, tc := range []struct {
		name   string
		used   CapacityResources
		delta  ResourceVector
		limit  CapacityResources
		reason string
	}{
		{"exact", capacityResources(ResourceVector{CPUMillis: 1000}), ResourceVector{CPUMillis: 1000}, CapacityResources{CPUMillis: number(2000)}, ""},
		{"overflow", capacityResources(ResourceVector{CPUMillis: math.MaxInt64}), ResourceVector{CPUMillis: 1}, CapacityResources{CPUMillis: number(math.MaxInt64)}, "hard_limit"},
		{"unknown", CapacityResources{}, ResourceVector{CPUMillis: 1}, CapacityResources{CPUMillis: number(10)}, "stale_inventory"},
		{"optional quota", CapacityResources{}, ResourceVector{CPUMillis: 1}, CapacityResources{}, ""},
		{"zero ceiling", capacityResources(ResourceVector{}), ResourceVector{Servers: 1}, CapacityResources{Servers: number(0)}, "hard_limit"},
		{"existing over lowered ceiling", capacityResources(ResourceVector{CPUMillis: 2000}), ResourceVector{}, CapacityResources{CPUMillis: number(1000)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := checkCapacityLimit(p, d, tc.used, tc.delta, tc.limit, "hard_limit")
			if tc.reason == "" {
				if b != nil {
					t.Fatalf("unexpected block %+v", b)
				}
			} else if b == nil || b.Reason != tc.reason {
				t.Fatalf("got %+v, want %s", b, tc.reason)
			}
		})
	}
}
func TestCapacityLedgerPositiveResizeDelta(t *testing.T) {
	old := ResourceVector{Servers: 1, CPUMillis: 1000, MemoryBytes: 4 << 30, StorageBytes: 40_000_000_000}
	target := ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 60_000_000_000}
	want := ResourceVector{CPUMillis: 1000, StorageBytes: 20_000_000_000}
	if got := positiveCapacityDelta(target, old); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
