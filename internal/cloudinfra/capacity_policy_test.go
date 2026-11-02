package cloudinfra

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func capacityPolicyFixture() CapacityPolicy {
	return CapacityPolicy{Enabled: true, Limits: ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 100_000_000_000}, Pools: []PoolExpansionPolicy{{PoolID: "pool", ConnectionID: "account", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}, MaxServers: 4}}}
}

func TestCapacityPolicyValidation(t *testing.T) {
	options := map[string]provider.ServerOptions{"pool": {Regions: []provider.ServerRegion{{ID: "region"}}, Sizes: []provider.ServerSize{{ID: "small", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}}}
	type policyTest struct {
		name    string
		edit    func(*CapacityPolicy)
		invalid bool
	}
	tests := make([]policyTest, 0, 24)
	tests = append(tests, []policyTest{
		{"disabled legacy", func(p *CapacityPolicy) { *p = CapacityPolicy{} }, false},
		{"enabled", func(*CapacityPolicy) {}, false},
		{"pool no growth", func(p *CapacityPolicy) { p.Pools[0].MaxServers = 0 }, false},
		{"negative count", func(p *CapacityPolicy) { p.Limits.Servers = -1 }, true},
		{"minimum exceeds maximum", func(p *CapacityPolicy) { p.Pools[0].MinServers = 3; p.Pools[0].MaxServers = 2 }, true},
		{"pool exceeds cloud", func(p *CapacityPolicy) { p.Pools[0].Limits = &ResourceVector{Servers: 5} }, true},
		{"minimum cpu impossible", func(p *CapacityPolicy) { p.Pools[0].MinServers = 3; p.Limits.CPUMillis = 5000 }, true},
		{"minimum storage impossible", func(p *CapacityPolicy) { p.Pools[0].MinServers = 3; p.Limits.StorageBytes = 50_000_000_000 }, true},
		{"reserve exceeds size", func(p *CapacityPolicy) { p.Pools[0].SystemReserve.CPUMillis = 3000 }, true},
		{"missing size facts", func(p *CapacityPolicy) { p.Pools[0].AllowedSizes = []string{"unknown"} }, true},
		{"missing allowed sizes", func(p *CapacityPolicy) { p.Pools[0].AllowedSizes = nil }, true},
		{"duplicate pools", func(p *CapacityPolicy) { p.Pools = append(p.Pools, p.Pools[0]) }, true},
		{"negative frontend maximum", func(p *CapacityPolicy) {
			n := int64(-1)
			p.FrontendLimits = []FrontendLimit{{TargetID: "target", Maximum: &n}}
		}, true},
		{"overflow minimum", func(p *CapacityPolicy) {
			p.Limits = ResourceVector{Servers: math.MaxInt64, CPUMillis: math.MaxInt64, MemoryBytes: math.MaxInt64, StorageBytes: math.MaxInt64}
			p.Pools[0].MinServers = math.MaxInt64
			p.Pools[0].MaxServers = math.MaxInt64
		}, true},
	}...)
	for _, resource := range []string{"servers", "cpu", "memory", "storage"} {
		tests = append(tests, struct {
			name    string
			edit    func(*CapacityPolicy)
			invalid bool
		}{"missing " + resource, func(p *CapacityPolicy) {
			switch resource {
			case "servers":
				p.Limits.Servers = 0
			case "cpu":
				p.Limits.CPUMillis = 0
			case "memory":
				p.Limits.MemoryBytes = 0
			case "storage":
				p.Limits.StorageBytes = 0
			}
		}, true})
	}
	for _, maximum := range []*int64{nil, new(int64), func() *int64 { n := int64(1); return &n }()} {
		tests = append(tests, struct {
			name    string
			edit    func(*CapacityPolicy)
			invalid bool
		}{"frontend maximum", func(p *CapacityPolicy) {
			p.Enabled = false
			p.FrontendLimits = []FrontendLimit{{TargetID: "target", Maximum: maximum}}
		}, false})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := capacityPolicyFixture()
			tt.edit(&p)
			err := validateCapacityPolicy(p, options)
			if tt.invalid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("expected ErrInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var found CapacityPolicy
			if err = json.Unmarshal(raw, &found); err != nil || !reflect.DeepEqual(found, p) {
				t.Fatalf("round trip: %+v %v", found, err)
			}
		})
	}
	t.Run("NaN rejected during decoding", func(t *testing.T) {
		var in CapacityPolicyInput
		if err := json.Unmarshal([]byte(`{"policy":{"limits":{"cpu_millis":"NaN"}}}`), &in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected invalid input, got %v", err)
		}
	})
}

func TestCapacityPolicyMinimumVectors(t *testing.T) {
	catalog := provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}, Sizes: []provider.ServerSize{
		{ID: "a-cpu", CPUs: 4, MemoryMiB: 1024, DiskGB: 10, Regions: []string{"region"}},
		{ID: "b-memory", CPUs: 1, MemoryMiB: 4096, DiskGB: 10, Regions: []string{"region"}},
	}}
	p := capacityPolicyFixture()
	p.Limits = ResourceVector{Servers: 2, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 100_000_000_000}
	p.Pools[0].MaxServers = 2
	p.Pools[0].MinServers = 1
	p.Pools[0].AllowedSizes = []string{"a-cpu", "b-memory"}
	if err := validateCapacityPolicy(p, map[string]provider.ServerOptions{"pool": catalog}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted invented cross-size vector: %v", err)
	}
	p.Limits = ResourceVector{Servers: 2, CPUMillis: 5000, MemoryBytes: 5 << 30, StorageBytes: 100_000_000_000}
	other := p.Pools[0]
	other.PoolID = "pool2"
	p.Pools = append(p.Pools, other)
	options := map[string]provider.ServerOptions{"pool": catalog, "pool2": catalog}
	if err := validateCapacityPolicyChoices(&p, options); err != nil {
		t.Fatal(err)
	}
	if p.Pools[0].BootstrapSize != "a-cpu" || p.Pools[1].BootstrapSize != "b-memory" {
		t.Fatalf("aggregate fitting choices: %+v", p.Pools)
	}
	p.Limits.CPUMillis = 4000
	p.Limits.MemoryBytes = 4 << 30
	if err := validateCapacityPolicy(p, options); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted aggregate minimum overage: %v", err)
	}
}

func TestCapacityPolicyMixedOnlyAndSearchBound(t *testing.T) {
	catalog := provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}, Sizes: []provider.ServerSize{
		{ID: "a-cpu", CPUs: 4, MemoryMiB: 1024, DiskGB: 10, Regions: []string{"region"}},
		{ID: "b-memory", CPUs: 1, MemoryMiB: 4096, DiskGB: 10, Regions: []string{"region"}},
	}}
	t.Run("mixed-only allocation stays unproven", func(t *testing.T) {
		p := capacityPolicyFixture()
		p.Limits = ResourceVector{Servers: 2, CPUMillis: 5000, MemoryBytes: 5 << 30, StorageBytes: 20_000_000_000}
		p.Pools[0].MinServers = 2
		p.Pools[0].MaxServers = 2
		p.Pools[0].AllowedSizes = []string{"a-cpu", "b-memory"}
		if err := validateCapacityPolicy(p, map[string]provider.ServerOptions{"pool": catalog}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "homogeneous") {
			t.Fatalf("mixed-only validation: %v", err)
		}
	})
	t.Run("bounded search rejects unproven feasible allocation", func(t *testing.T) {
		// Thirteen earlier pools offer equal CPU-heavy choices before a memory-heavy
		// alternative. Only all memory-heavy choices leave room for the final pool.
		// That valid assignment lies beyond 10,000 deterministic candidate visits.
		p := capacityPolicyFixture()
		p.Pools = nil
		p.Limits = ResourceVector{Servers: 14, CPUMillis: 65000, MemoryBytes: 100 << 30, StorageBytes: 1_000_000_000_000}
		options := map[string]provider.ServerOptions{}
		for i := range 14 {
			id := fmt.Sprintf("pool-%02d", i)
			pool := PoolExpansionPolicy{PoolID: id, ConnectionID: "account", Region: "region", Image: "ubuntu", Architecture: "amd64", MinServers: 1, MaxServers: 1, AllowedSizes: []string{"a-cpu", "b-memory"}}
			if i == 13 {
				pool.AllowedSizes = []string{"z-final"}
			}
			p.Pools = append(p.Pools, pool)
			options[id] = catalog
			if i == 13 {
				options[id] = provider.ServerOptions{Regions: catalog.Regions, Images: catalog.Images, Sizes: []provider.ServerSize{{ID: "z-final", CPUs: 52, MemoryMiB: 1024, DiskGB: 10, Regions: []string{"region"}}}}
			}
		}
		if err := validateCapacityPolicy(p, options); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "homogeneous") {
			t.Fatalf("search-bound validation: %v", err)
		}
		// The reverse catalog order is irrelevant; stable size IDs drive the bound.
		for i := range 13 {
			p.Pools[i].AllowedSizes = []string{"b-memory"}
		}
		if err := validateCapacityPolicy(p, options); err != nil {
			t.Fatalf("known homogeneous allocation: %v", err)
		}
	})
}
